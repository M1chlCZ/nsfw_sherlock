package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"nsfw_sherlock/engine"
)

type summary struct {
	Images    int     `json:"images"`
	Positive  int     `json:"positive"`
	Negative  int     `json:"negative"`
	TP        int     `json:"tp"`
	FP        int     `json:"fp"`
	TN        int     `json:"tn"`
	FN        int     `json:"fn"`
	Precision float64 `json:"precision"`
	Recall    float64 `json:"recall"`
	F1        float64 `json:"f1"`
	Accuracy  float64 `json:"accuracy"`
	P50Ms     float64 `json:"p50Ms"`
	P95Ms     float64 `json:"p95Ms"`
	MeanMs    float64 `json:"meanMs"`
	Skipped   int     `json:"skipped"`
}

type sample struct {
	path     string
	label    string
	positive bool
}

func main() {
	os.Exit(run())
}

func run() int {
	defaultProfile := os.Getenv("NSFW_PROFILE")
	if defaultProfile == "" {
		defaultProfile = "balanced"
	}
	defaultManifest := os.Getenv("MANIFEST")
	if defaultManifest == "" {
		defaultManifest = "models/manifest.json"
	}
	dir := flag.String("dir", "", "labeled image directory with a <label>/<file> layout")
	manifest := flag.String("manifest", defaultManifest, "path to the model manifest")
	profile := flag.String("profile", defaultProfile, "manifest profile to load")
	models := flag.String("models", "./assets/models", "directory holding the ONNX model files")
	sessionPool := flag.Int("session-pool", 2, "ONNX sessions to create per model")
	jsonOut := flag.Bool("json", false, "print only the JSON summary")
	limit := flag.Int("limit", 0, "maximum images per class, 0 for all")
	ortLib := flag.String("ort", os.Getenv("ORT_LIB"), "path to the ONNX Runtime shared library")
	flag.Parse()

	if *dir == "" {
		fmt.Fprintln(os.Stderr, "bench: -dir is required")
		flag.Usage()
		return 2
	}
	if *limit < 0 {
		fmt.Fprintln(os.Stderr, "bench: -limit must not be negative")
		return 2
	}

	samples, err := collect(*dir, *limit)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if len(samples) == 0 {
		fmt.Fprintf(os.Stderr, "bench: no images found under %s\n", *dir)
		return 1
	}

	if err := engine.InitRuntime(*ortLib); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	loaded, err := engine.LoadManifest(*manifest)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	eng, err := engine.NewEngine(loaded, engine.Options{
		Profile:   *profile,
		ModelsDir: *models,
		PoolSize:  *sessionPool,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer func() {
		if err := eng.Close(); err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
	}()

	result, latencies, err := evaluate(eng, samples, *jsonOut)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	result.Images = len(latencies)
	result.Positive = result.TP + result.FN
	result.Negative = result.TN + result.FP
	result.Precision = ratio(result.TP, result.TP+result.FP)
	result.Recall = ratio(result.TP, result.TP+result.FN)
	result.F1 = ratioFloat(2*result.Precision*result.Recall, result.Precision+result.Recall)
	result.Accuracy = ratio(result.TP+result.TN, result.Images)
	if len(latencies) > 0 {
		sort.Float64s(latencies)
		var total float64
		for _, ms := range latencies {
			total += ms
		}
		result.MeanMs = total / float64(len(latencies))
		result.P50Ms = percentile(latencies, 0.50)
		result.P95Ms = percentile(latencies, 0.95)
	}

	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(result); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return 0
	}
	fmt.Printf("summary images=%d positive=%d negative=%d tp=%d fp=%d tn=%d fn=%d precision=%.4f recall=%.4f f1=%.4f accuracy=%.4f p50=%.1fms p95=%.1fms mean=%.1fms skipped=%d\n",
		result.Images, result.Positive, result.Negative, result.TP, result.FP, result.TN, result.FN,
		result.Precision, result.Recall, result.F1, result.Accuracy,
		result.P50Ms, result.P95Ms, result.MeanMs, result.Skipped)
	return 0
}

func evaluate(eng *engine.Engine, samples []sample, jsonOut bool) (summary, []float64, error) {
	var result summary
	latencies := make([]float64, 0, len(samples))
	for _, s := range samples {
		data, err := os.ReadFile(s.path)
		if err != nil {
			result.Skipped++
			fmt.Fprintf(os.Stderr, "bench: skip %s: %v\n", s.path, err)
			continue
		}
		start := time.Now()
		analysis, err := eng.Analyze(context.Background(), data)
		elapsed := time.Since(start)
		if err != nil {
			if errors.Is(err, engine.ErrUnsupportedImage) || errors.Is(err, engine.ErrImageTooLarge) {
				result.Skipped++
				fmt.Fprintf(os.Stderr, "bench: skip %s: %v\n", s.path, err)
				continue
			}
			return result, nil, fmt.Errorf("bench: analyze %s: %w", s.path, err)
		}
		ms := float64(elapsed.Microseconds()) / 1000
		latencies = append(latencies, ms)
		predicted := analysis.LegacyNSFW()
		switch {
		case s.positive && predicted:
			result.TP++
		case s.positive:
			result.FN++
		case predicted:
			result.FP++
		default:
			result.TN++
		}
		if !jsonOut {
			outcome := "sfw"
			if predicted {
				outcome = "nsfw"
			}
			fmt.Printf("label=%s predicted=%s verdict=%s nsfw=%.4f ms=%.1f\n",
				s.label, outcome, analysis.Verdict, analysis.NSFW, ms)
		}
	}
	for _, warning := range eng.Warnings() {
		fmt.Fprintln(os.Stderr, warning)
	}
	return result, latencies, nil
}

func collect(root string, limit int) ([]sample, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("bench: read directory %s: %w", root, err)
	}
	var samples []sample
	classes := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		label := entry.Name()
		if label == "" {
			continue
		}
		files, err := os.ReadDir(filepath.Join(root, label))
		if err != nil {
			return nil, fmt.Errorf("bench: read class directory %s: %w", entry.Name(), err)
		}
		classes++
		count := 0
		for _, file := range files {
			if file.IsDir() {
				continue
			}
			samples = append(samples, sample{
				path:     filepath.Join(root, label, file.Name()),
				label:    label,
				positive: strings.EqualFold(label, "nsfw"),
			})
			count++
			if limit > 0 && count >= limit {
				break
			}
		}
	}
	if classes == 0 {
		return nil, fmt.Errorf("bench: no label directories under %s", root)
	}
	return samples, nil
}

func ratio(num, den int) float64 {
	if den == 0 {
		return 0
	}
	return float64(num) / float64(den)
}

func ratioFloat(num, den float64) float64 {
	if den == 0 {
		return 0
	}
	return num / den
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(math.Ceil(p*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}
