package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	ort "github.com/yalue/onnxruntime_go"
)

const parityFixtureSchemaVersion = 2

const parityTensorStatsTolerance = 1e-2

const parityRunTimeout = 60 * time.Second

type parityFixture struct {
	SchemaVersion int                     `json:"schema_version"`
	Model         string                  `json:"model"`
	SHA256        string                  `json:"sha256"`
	Kind          string                  `json:"kind"`
	Labels        []string                `json:"labels"`
	Activation    string                  `json:"activation"`
	Preprocess    parityPreprocess        `json:"preprocess"`
	Detector      *parityDetectorSpec     `json:"detector"`
	Outputs       map[string]parityOutput `json:"outputs"`
	Detections    []parityDetection       `json:"detections"`
	Tolerance     parityTolerance         `json:"tolerance"`
}

type parityPreprocess struct {
	Width         int              `json:"width"`
	Height        int              `json:"height"`
	Mean          []float64        `json:"mean"`
	Std           []float64        `json:"std"`
	Normalize     *bool            `json:"normalize"`
	CropPct       float64          `json:"crop_pct"`
	Interpolation string           `json:"interpolation"`
	TensorStats   parityTensorData `json:"tensor_stats"`
	Image         parityImage      `json:"image"`
}

type parityTensorData struct {
	Min  float64 `json:"min"`
	Max  float64 `json:"max"`
	Mean float64 `json:"mean"`
}

type parityDetectorSpec struct {
	Classes        []string `json:"classes"`
	ScoreThreshold float64  `json:"score_threshold"`
	IoUThreshold   float64  `json:"iou_threshold"`
}

type parityImage struct {
	Generator string `json:"generator"`
	Path      string `json:"path"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
}

type parityOutput struct {
	Scores map[string]float64 `json:"scores"`
}

type parityDetection struct {
	Label string  `json:"label"`
	Score float64 `json:"score"`
	Box   struct {
		X float64 `json:"x"`
		Y float64 `json:"y"`
		W float64 `json:"w"`
		H float64 `json:"h"`
	} `json:"box"`
}

type parityTolerance struct {
	Score float64 `json:"score"`
	BoxPx float64 `json:"box_px"`
}

var (
	parityRuntimeOnce sync.Once
	parityRuntimeErr  error
)

func TestParity(t *testing.T) {
	strict := os.Getenv("PARITY_REQUIRE") == "1"
	manifestPath := os.Getenv("PARITY_MANIFEST")
	explicitManifest := manifestPath != ""
	if !explicitManifest {
		manifestPath = filepath.Join("..", "models", "manifest.json")
	}
	if _, err := os.Stat(manifestPath); err != nil {
		if explicitManifest || strict {
			t.Fatalf("manifest %s not present: %v", manifestPath, err)
		}
		t.Skipf("real manifest not present: %v", err)
	}
	manifest, err := LoadManifest(manifestPath)
	if err != nil {
		t.Fatalf("LoadManifest(%s) error = %v", manifestPath, err)
	}

	modelsDir := os.Getenv("MODELS_DIR")
	if modelsDir == "" {
		modelsDir = filepath.Join("..", "assets", "models")
	}

	for _, model := range manifest.Models {
		t.Run(model.ID, func(t *testing.T) {
			fixturePath := filepath.Join("..", "testdata", "parity", model.File+".json")
			fixtureInfo, fixtureErr := os.Stat(fixturePath)
			fixturePresent := fixtureErr == nil && !fixtureInfo.IsDir()
			enforce := strict && (model.Required || fixturePresent)

			modelPath := filepath.Join(modelsDir, model.File)
			if info, err := os.Stat(modelPath); err != nil || info.IsDir() {
				paritySkipOrFail(t, enforce, "model file %s not present in MODELS_DIR=%s; run scripts/fetch-models.sh", model.File, modelsDir)
			}

			fixture, err := readParityFixture(fixturePath)
			if err != nil {
				t.Fatalf("read fixture %s: %v", fixturePath, err)
			}
			if fixture.Model != model.File {
				t.Fatalf("fixture model = %q, manifest file = %q", fixture.Model, model.File)
			}
			if fixture.SHA256 != model.SHA256 {
				t.Fatalf("fixture sha256 = %q, manifest sha256 = %q", fixture.SHA256, model.SHA256)
			}
			if fixture.Kind != model.Kind {
				t.Fatalf("fixture kind = %q, manifest kind = %q", fixture.Kind, model.Kind)
			}
			if diffs := parityPreprocessDiffs(model, fixture.Preprocess); len(diffs) > 0 {
				t.Fatalf("effective preprocessing differs from the fixture reference: %s", strings.Join(diffs, "; "))
			}
			if fixture.Kind == KindDetector {
				if diffs := parityDetectorSpecDiffs(model, fixture.Detector); len(diffs) > 0 {
					t.Fatalf("detector spec differs from the fixture reference: %s", strings.Join(diffs, "; "))
				}
			}
			if len(fixture.Labels) > 0 && !slices.Equal(fixture.Labels, model.Labels) {
				t.Fatalf("fixture labels %v, manifest labels %v", fixture.Labels, model.Labels)
			}

			if err := parityInitRuntime(); err != nil {
				paritySkipOrFail(t, enforce, "onnxruntime unavailable: %v", err)
			}

			img, err := loadParityImage(fixture)
			if err != nil {
				t.Fatalf("fixture image: %v", err)
			}
			parityCheckTensorStats(t, model, fixture, img)

			pool, err := newModelPool(modelPath, model.InputNames, model.OutputNames, 1)
			if err != nil {
				t.Fatalf("load model %s: %v", model.File, err)
			}
			defer pool.close()

			ctx, cancel := context.WithTimeout(context.Background(), parityRunTimeout)
			defer cancel()

			switch model.Kind {
			case KindClassifier:
				parityClassifier(t, ctx, model, pool, fixture, img)
			case KindDetector:
				parityDetector(t, ctx, model, pool, fixture, img)
			default:
				t.Fatalf("unsupported model kind %q", model.Kind)
			}
		})
	}
}

func paritySkipOrFail(t *testing.T, enforce bool, format string, args ...any) {
	t.Helper()
	message := fmt.Sprintf(format, args...)
	if enforce {
		t.Fatalf("PARITY_REQUIRE=1: %s", message)
	}
	t.Skip(message)
}

func parityRequiredFields(fixture parityFixture) error {
	if fixture.SchemaVersion != parityFixtureSchemaVersion {
		return fmt.Errorf("schema_version = %d, want %d", fixture.SchemaVersion, parityFixtureSchemaVersion)
	}
	if fixture.Model == "" {
		return errors.New("missing model")
	}
	if fixture.SHA256 == "" {
		return errors.New("missing sha256")
	}
	switch fixture.Kind {
	case KindClassifier:
		if len(fixture.Labels) == 0 {
			return errors.New("classifier fixture missing labels")
		}
	case KindDetector:
		if fixture.Detections == nil {
			return errors.New("detector fixture missing detections")
		}
		if fixture.Detector == nil || len(fixture.Detector.Classes) == 0 {
			return errors.New("detector fixture missing detector classes")
		}
		if fixture.Detector.ScoreThreshold <= 0 || fixture.Detector.IoUThreshold <= 0 {
			return errors.New("detector fixture has non-positive thresholds")
		}
	default:
		return fmt.Errorf("unknown kind %q", fixture.Kind)
	}
	if fixture.Preprocess.Width <= 0 || fixture.Preprocess.Height <= 0 {
		return errors.New("preprocess missing width/height")
	}
	if len(fixture.Preprocess.Mean) != 3 {
		return fmt.Errorf("preprocess missing or invalid mean: has %d values, want 3", len(fixture.Preprocess.Mean))
	}
	if len(fixture.Preprocess.Std) != 3 {
		return fmt.Errorf("preprocess missing or invalid std: has %d values, want 3", len(fixture.Preprocess.Std))
	}
	if fixture.Preprocess.Normalize == nil {
		return errors.New("preprocess missing normalize")
	}
	if fixture.Preprocess.Interpolation == "" {
		return errors.New("preprocess missing interpolation")
	}
	if fixture.Tolerance.Score <= 0 {
		return errors.New("missing tolerance.score")
	}
	if fixture.Kind == KindDetector && fixture.Tolerance.BoxPx <= 0 {
		return errors.New("detector fixture missing tolerance.box_px")
	}
	return nil
}

func readParityFixture(path string) (parityFixture, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return parityFixture{}, err
	}
	var fixture parityFixture
	if err := json.Unmarshal(data, &fixture); err != nil {
		return parityFixture{}, fmt.Errorf("parse: %w", err)
	}
	if err := parityRequiredFields(fixture); err != nil {
		return parityFixture{}, err
	}
	return fixture, nil
}

func TestParityRequiredFields(t *testing.T) {
	const valid = `{
		"schema_version": 2,
		"model": "example.onnx",
		"sha256": "abc",
		"kind": "classifier",
		"labels": ["safe", "nsfw"],
		"preprocess": {
			"width": 224,
			"height": 224,
			"mean": [0.5, 0.5, 0.5],
			"std": [0.5, 0.5, 0.5],
			"normalize": true,
			"crop_pct": 0,
			"interpolation": "bicubic"
		},
		"tolerance": {"score": 0.05}
	}`
	cases := []struct {
		name    string
		fixture string
		want    string
	}{
		{name: "valid", fixture: valid},
		{
			name:    "missing mean",
			fixture: strings.Replace(valid, `"mean": [0.5, 0.5, 0.5],`, "", 1),
			want:    "preprocess missing or invalid mean",
		},
		{
			name:    "two std values",
			fixture: strings.Replace(valid, `"std": [0.5, 0.5, 0.5]`, `"std": [0.5, 0.5]`, 1),
			want:    "preprocess missing or invalid std",
		},
		{
			name:    "empty interpolation",
			fixture: strings.Replace(valid, `"interpolation": "bicubic"`, `"interpolation": ""`, 1),
			want:    "preprocess missing interpolation",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var fixture parityFixture
			if err := json.Unmarshal([]byte(tc.fixture), &fixture); err != nil {
				t.Fatalf("json.Unmarshal: %v", err)
			}
			err := parityRequiredFields(fixture)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("parityRequiredFields() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("parityRequiredFields() = %v, want error containing %q", err, tc.want)
			}
		})
	}
}

func loadParityImage(fixture parityFixture) (image.Image, error) {
	spec := fixture.Preprocess.Image
	switch spec.Generator {
	case "file":
		if spec.Path == "" {
			return nil, errors.New("file generator without path")
		}
		data, err := os.ReadFile(filepath.Join("..", filepath.FromSlash(spec.Path)))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", spec.Path, err)
		}
		img, err := decodeImage(data, defaultLimits)
		if err != nil {
			return nil, fmt.Errorf("decode %s: %w", spec.Path, err)
		}
		b := img.Bounds()
		if b.Dx() != spec.Width || b.Dy() != spec.Height {
			return nil, fmt.Errorf("decoded %s is %dx%d, fixture records %dx%d", spec.Path, b.Dx(), b.Dy(), spec.Width, spec.Height)
		}
		return img, nil
	case "gradient-checkerboard-v1":
		if spec.Width <= 0 || spec.Height <= 0 {
			return nil, fmt.Errorf("synthetic image size %dx%d is invalid", spec.Width, spec.Height)
		}
		return paritySyntheticImage(spec.Width, spec.Height), nil
	default:
		return nil, fmt.Errorf("unknown image generator %q", spec.Generator)
	}
}

func paritySyntheticImage(width, height int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	denomX := max(width-1, 1)
	denomY := max(height-1, 1)
	for y := 0; y < height; y++ {
		green := uint8(y * 255 / denomY)
		for x := 0; x < width; x++ {
			i := y*img.Stride + x*4
			img.Pix[i] = uint8(x * 255 / denomX)
			img.Pix[i+1] = green
			img.Pix[i+2] = uint8(((x/16 + y/16) % 2) * 255)
			img.Pix[i+3] = 0xff
		}
	}
	return img
}

func parityPreprocessDiffs(model Model, fixture parityPreprocess) []string {
	spec := inputSpecFromModel(model)
	var diffs []string
	add := func(format string, args ...any) {
		diffs = append(diffs, fmt.Sprintf(format, args...))
	}
	if spec.Width != fixture.Width {
		add("width: manifest %d, fixture %d", spec.Width, fixture.Width)
	}
	if spec.Height != fixture.Height {
		add("height: manifest %d, fixture %d", spec.Height, fixture.Height)
	}
	channels := func(field string, manifest [3]float32, reference []float64) {
		if len(reference) != 3 {
			return
		}
		for c := range 3 {
			if manifest[c] != float32(reference[c]) {
				add("%s[%d]: manifest %v, fixture %v", field, c, manifest[c], reference[c])
			}
		}
	}
	channels("mean", spec.Mean, fixture.Mean)
	channels("std", spec.Std, fixture.Std)
	if fixture.Normalize != nil && spec.Normalize != *fixture.Normalize {
		add("normalize: manifest %v, fixture %v", spec.Normalize, *fixture.Normalize)
	}
	if spec.CropPct != float32(fixture.CropPct) {
		add("crop_pct: manifest %v, fixture %v", spec.CropPct, fixture.CropPct)
	}
	if fixture.Interpolation != "" {
		if got, want := effectivePreprocessInterpolation(model, spec), fixture.Interpolation; got != want {
			add("interpolation: effective %q, fixture %q", got, want)
		}
	}
	return diffs
}

func effectivePreprocessInterpolation(model Model, spec InputSpec) string {
	if model.Kind == KindDetector {
		return InterpolationBilinear
	}
	if spec.Interpolation == "" {
		return InterpolationBicubic
	}
	return spec.Interpolation
}

func parityDetectorSpecDiffs(model Model, fixture *parityDetectorSpec) []string {
	if fixture == nil {
		return []string{"fixture missing detector spec"}
	}
	if model.Detector == nil {
		return []string{"manifest missing detector spec"}
	}
	var diffs []string
	add := func(format string, args ...any) {
		diffs = append(diffs, fmt.Sprintf(format, args...))
	}
	if !slices.Equal(model.Detector.Classes, fixture.Classes) {
		add("classes: manifest %v, fixture %v", model.Detector.Classes, fixture.Classes)
	}
	if model.Detector.ScoreThreshold != float32(fixture.ScoreThreshold) {
		add("score_threshold: manifest %v, fixture %v", model.Detector.ScoreThreshold, fixture.ScoreThreshold)
	}
	if model.Detector.IoUThreshold != float32(fixture.IoUThreshold) {
		add("iou_threshold: manifest %v, fixture %v", model.Detector.IoUThreshold, fixture.IoUThreshold)
	}
	return diffs
}

func parityCheckTensorStats(t *testing.T, model Model, fixture parityFixture, img image.Image) {
	t.Helper()
	var data []float32
	switch model.Kind {
	case KindClassifier:
		var err error
		data, err = Preprocess(img, inputSpecFromModel(model))
		if err != nil {
			t.Fatalf("classifier %s: preprocess fixture image: %v", model.ID, err)
		}
	case KindDetector:
		data, _ = preprocessDetector(img, model.Input.Width, model.Input.Height)
	default:
		t.Fatalf("unsupported model kind %q", model.Kind)
	}
	if len(data) == 0 {
		t.Fatalf("preprocessing %s produced an empty tensor", model.ID)
	}

	minimum, maximum := data[0], data[0]
	var sum float64
	for _, v := range data {
		minimum = min(minimum, v)
		maximum = max(maximum, v)
		sum += float64(v)
	}
	mean := sum / float64(len(data))

	var maxDelta float64
	for _, stat := range []struct {
		name      string
		got, want float64
	}{
		{"min", float64(minimum), fixture.Preprocess.TensorStats.Min},
		{"max", float64(maximum), fixture.Preprocess.TensorStats.Max},
		{"mean", mean, fixture.Preprocess.TensorStats.Mean},
	} {
		delta := math.Abs(stat.got - stat.want)
		maxDelta = math.Max(maxDelta, delta)
		if delta > parityTensorStatsTolerance {
			t.Errorf("preprocessing %s: tensor %s = %v, fixture %v, delta %.3g > %g",
				model.ID, stat.name, stat.got, stat.want, delta, parityTensorStatsTolerance)
		}
	}
	t.Logf("preprocessing %s: max tensor stat delta %.3g (bound %g)", model.ID, maxDelta, parityTensorStatsTolerance)
}

func parityInitRuntime() error {
	parityRuntimeOnce.Do(func() {
		parityRuntimeErr = initParityRuntime()
	})
	return parityRuntimeErr
}

func initParityRuntime() error {
	if ort.IsInitialized() {
		return nil
	}
	var candidates []string
	if path := os.Getenv("ORT_LIB"); path != "" {
		candidates = append(candidates, path)
	}
	if path := resolveLibPath(); path != "" {
		candidates = append(candidates, path)
	}
	if name := ortLibName(); name != "" {
		candidates = append(candidates, filepath.Join("..", "lib", name))
	}
	for _, path := range candidates {
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			continue
		}
		return InitRuntime(path)
	}
	return errors.New("no onnxruntime shared library found; set ORT_LIB or run scripts/fetch-models.sh --ort-only")
}

func parityClassifier(t *testing.T, ctx context.Context, model Model, pool *modelPool, fixture parityFixture, img image.Image) {
	t.Helper()
	if model.Output.Activation != fixture.Activation {
		t.Errorf("manifest activation %q, fixture activation %q", model.Output.Activation, fixture.Activation)
	}
	expected, ok := fixture.Outputs[model.OutputNames[0]]
	if !ok {
		t.Fatalf("fixture has no output %q (has %v)", model.OutputNames[0], outputNames(fixture.Outputs))
	}
	if len(expected.Scores) == 0 {
		t.Fatalf("fixture output %q has no expected label scores", model.OutputNames[0])
	}

	c, err := newClassifier(model, pool)
	if err != nil {
		t.Fatalf("newClassifier(%s): %v", model.ID, err)
	}
	got, err := c.run(ctx, img)
	if err != nil {
		t.Fatalf("classifier %s run: %v", model.ID, err)
	}

	var maxDelta float64
	for label, want := range expected.Scores {
		gotScore, ok := got[label]
		if !ok {
			t.Errorf("classifier %s: label %q missing from Go scores", model.ID, label)
			continue
		}
		delta := math.Abs(float64(gotScore) - want)
		maxDelta = math.Max(maxDelta, delta)
		if delta > fixture.Tolerance.Score {
			t.Errorf("classifier %s: label %q score = %.6f, fixture %.6f, delta %.6f > tolerance %.6f",
				model.ID, label, gotScore, want, delta, fixture.Tolerance.Score)
		}
	}
	for label := range got {
		if _, ok := expected.Scores[label]; !ok {
			t.Errorf("classifier %s: unexpected Go label %q", model.ID, label)
		}
	}

	gotArgmax := argmaxLabel(model.Labels, got)
	wantArgmax := argmaxLabel(model.Labels, expected.Scores)
	if gotArgmax != wantArgmax {
		t.Errorf("classifier %s: argmax = %q, fixture argmax = %q", model.ID, gotArgmax, wantArgmax)
	}
	t.Logf("classifier %s: max score delta %.6f (tolerance %.3f), argmax %q", model.ID, maxDelta, fixture.Tolerance.Score, gotArgmax)
}

func parityDetector(t *testing.T, ctx context.Context, model Model, pool *modelPool, fixture parityFixture, img image.Image) {
	t.Helper()
	threshold := float64(model.Detector.ScoreThreshold)
	if len(fixture.Detections) == 0 {
		t.Fatalf("detector %s: fixture records no reference detections", model.ID)
	}

	d, err := newDetector(model, pool)
	if err != nil {
		t.Fatalf("newDetector(%s): %v", model.ID, err)
	}
	got, err := d.run(ctx, img)
	if err != nil {
		t.Fatalf("detector %s run: %v", model.ID, err)
	}

	matched := make([]bool, len(got))
	var maxScoreDelta, maxBoxDelta, maxDimDelta float64
	for _, ref := range fixture.Detections {
		if ref.Score < threshold {
			continue
		}
		best := -1
		var bestDist, bestScoreDelta, bestDimDelta float64
		for i := range got {
			if matched[i] || got[i].Label != ref.Label {
				continue
			}
			dist := centerDistance(got[i], ref)
			wDelta := math.Abs(float64(got[i].Box.W) - ref.Box.W)
			hDelta := math.Abs(float64(got[i].Box.H) - ref.Box.H)
			dimDelta := math.Max(wDelta, hDelta)
			scoreDelta := math.Abs(float64(got[i].Score) - ref.Score)
			if dist > fixture.Tolerance.BoxPx || dimDelta > fixture.Tolerance.BoxPx || scoreDelta > fixture.Tolerance.Score {
				continue
			}
			if best < 0 || dist < bestDist {
				best, bestDist, bestScoreDelta, bestDimDelta = i, dist, scoreDelta, dimDelta
			}
		}
		if best < 0 {
			t.Errorf("detector %s: reference %s score %.6f box (%.3f,%.3f,%.3f,%.3f) has no same-label Go detection within center %.3f px, size %.3f px and score %.3f (Go: %s)",
				model.ID, ref.Label, ref.Score, ref.Box.X, ref.Box.Y, ref.Box.W, ref.Box.H,
				fixture.Tolerance.BoxPx, fixture.Tolerance.BoxPx, fixture.Tolerance.Score, formatDetections(got))
			continue
		}
		matched[best] = true
		maxScoreDelta = math.Max(maxScoreDelta, bestScoreDelta)
		maxBoxDelta = math.Max(maxBoxDelta, bestDist)
		maxDimDelta = math.Max(maxDimDelta, bestDimDelta)
	}

	for i, det := range got {
		if matched[i] || float64(det.Score) < threshold+fixture.Tolerance.Score {
			continue
		}
		nearest := math.Inf(1)
		for _, ref := range fixture.Detections {
			if ref.Label == det.Label {
				nearest = math.Min(nearest, centerDistance(det, ref))
			}
		}
		if nearest > fixture.Tolerance.BoxPx {
			t.Errorf("detector %s: extra Go detection %s score %.6f at (%.1f,%.1f,%.1f,%.1f), nearest reference %.3f px",
				model.ID, det.Label, det.Score, det.Box.X, det.Box.Y, det.Box.W, det.Box.H, nearest)
		}
	}

	t.Logf("detector %s: %d Go detections, %d reference detections, max score delta %.6f (tolerance %.3f), max box-center delta %.3f px, max box-size delta %.3f px (tolerance %.3f)",
		model.ID, len(got), len(fixture.Detections), maxScoreDelta, fixture.Tolerance.Score, maxBoxDelta, maxDimDelta, fixture.Tolerance.BoxPx)
}

func argmaxLabel[V ~float32 | ~float64](labels []string, scores map[string]V) string {
	best := ""
	var bestScore V
	found := false
	for _, label := range labels {
		score, ok := scores[label]
		if !ok {
			continue
		}
		if !found || score > bestScore {
			best, bestScore, found = label, score, true
		}
	}
	return best
}

func centerDistance(det Detection, ref parityDetection) float64 {
	dx := float64(det.Box.X+det.Box.W/2) - (ref.Box.X + ref.Box.W/2)
	dy := float64(det.Box.Y+det.Box.H/2) - (ref.Box.Y + ref.Box.H/2)
	return math.Hypot(dx, dy)
}

func outputNames(outputs map[string]parityOutput) []string {
	names := make([]string, 0, len(outputs))
	for name := range outputs {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func formatDetections(dets []Detection) string {
	out := make([]string, 0, len(dets))
	for _, det := range dets {
		out = append(out, fmt.Sprintf("%s %.4f (%.1f,%.1f,%.1f,%.1f)", det.Label, det.Score, det.Box.X, det.Box.Y, det.Box.W, det.Box.H))
	}
	return fmt.Sprint(out)
}
