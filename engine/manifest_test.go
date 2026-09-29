package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTempFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	return path
}

func loadSampleManifest(t *testing.T) *Manifest {
	t.Helper()
	m, err := LoadManifest(writeTempFile(t, "manifest.json", sampleManifestJSON))
	if err != nil {
		t.Fatalf("LoadManifest(sample) error = %v, want nil", err)
	}
	return m
}

var sampleManifestJSON = fmt.Sprintf(`{
  "version": 1,
  "profiles": {
    "balanced": {"models": ["a", "b"]},
    "fast": {"models": ["b"]}
  },
  "models": [
    {
      "id": "a",
      "kind": "classifier",
      "role": "photo",
      "file": "a.onnx",
      "sha256": "%s",
      "input_names": ["input"],
      "output_names": ["logits"],
      "source": {"type": "hf", "url": "https://example.com/a.onnx"},
      "input": {
        "width": 224,
        "height": 224,
        "mean": [0.485, 0.456, 0.406],
        "std": [0.229, 0.224, 0.225],
        "normalize": true,
        "resize": "stretch"
      },
      "labels": ["neutral", "nsfw"],
      "output": {"activation": "softmax"},
      "required": true
    },
    {
      "id": "b",
      "kind": "detector",
      "role": "regions",
      "file": "b.onnx",
      "sha256": "%s",
      "input_names": ["images"],
      "output_names": ["output0"],
      "source": {"type": "release", "url": "https://example.com/b.onnx"},
      "input": {
        "width": 640,
        "height": 640,
        "mean": [0, 0, 0],
        "std": [1, 1, 1],
        "normalize": false
      },
      "detector": {
        "classes": ["EXPOSED", "COVERED"],
        "score_threshold": 0.35,
        "iou_threshold": 0.5,
        "explicit_classes": ["EXPOSED"],
        "suggestive_classes": ["COVERED"]
      },
      "required": false
    }
  ]
}`, strings.Repeat("a", 64), strings.Repeat("b", 64))

func TestLoadManifestAndProfile(t *testing.T) {
	path := writeTempFile(t, "manifest.json", sampleManifestJSON)
	m, err := LoadManifest(path)
	if err != nil {
		t.Fatalf("LoadManifest() error = %v, want nil", err)
	}
	if m.Version != 1 {
		t.Errorf("Version = %d, want 1", m.Version)
	}

	a, err := m.Model("a")
	if err != nil {
		t.Fatalf("Model(a) error = %v, want nil", err)
	}
	wantMean := [3]float32{0.485, 0.456, 0.406}
	wantStd := [3]float32{0.229, 0.224, 0.225}
	if a.Input.Width != 224 || a.Input.Height != 224 || !a.Input.Normalize || a.Input.Resize != ResizeStretch {
		t.Errorf("Model(a).Input = %+v, want 224x224 normalize stretch", a.Input)
	}
	if a.Input.Mean != wantMean || a.Input.Std != wantStd {
		t.Errorf("Model(a).Input mean/std = %v/%v, want %v/%v", a.Input.Mean, a.Input.Std, wantMean, wantStd)
	}

	balanced, err := m.Profile("balanced")
	if err != nil {
		t.Fatalf("Profile(balanced) error = %v, want nil", err)
	}
	if got, want := strings.Join(balanced, ","), "a,b"; got != want {
		t.Errorf("Profile(balanced) = %v, want [a b]", balanced)
	}

	fast, err := m.Profile("fast")
	if err != nil {
		t.Fatalf("Profile(fast) error = %v, want nil", err)
	}
	if got, want := strings.Join(fast, ","), "b"; got != want {
		t.Errorf("Profile(fast) = %v, want [b]", fast)
	}

	if _, err := m.Profile("nope"); err == nil {
		t.Error("Profile(nope) error = nil, want error")
	}

	balanced[0] = "mutated"
	again, err := m.Profile("balanced")
	if err != nil {
		t.Fatalf("Profile(balanced) after mutation error = %v, want nil", err)
	}
	if again[0] != "a" {
		t.Errorf("Profile returned an aliased slice: got %q, want %q", again[0], "a")
	}
}

func TestManifestModelLookup(t *testing.T) {
	m := loadSampleManifest(t)

	a, err := m.Model("a")
	if err != nil {
		t.Fatalf("Model(a) error = %v, want nil", err)
	}
	if a.ID != "a" || a.Kind != "classifier" || a.Role != RolePhoto || a.File != "a.onnx" {
		t.Errorf("Model(a) = %+v, want id a classifier photo a.onnx", a)
	}
	if a.SHA256 != strings.Repeat("a", 64) {
		t.Errorf("Model(a).SHA256 = %q, want 64 a characters", a.SHA256)
	}
	if len(a.Labels) != 2 || a.Output.Activation != "softmax" || !a.Required {
		t.Errorf("Model(a) = %+v, want labels, softmax activation, required", a)
	}
	if len(a.InputNames) != 1 || a.InputNames[0] != "input" || len(a.OutputNames) != 1 || a.OutputNames[0] != "logits" {
		t.Errorf("Model(a) names = %v/%v, want [input]/[logits]", a.InputNames, a.OutputNames)
	}
	if a.Detector != nil {
		t.Errorf("Model(a).Detector = %+v, want nil", a.Detector)
	}

	b, err := m.Model("b")
	if err != nil {
		t.Fatalf("Model(b) error = %v, want nil", err)
	}
	if b.Kind != "detector" || b.Role != RoleRegions || b.Detector == nil {
		t.Fatalf("Model(b) = %+v, want detector regions with spec", b)
	}
	if b.SHA256 != strings.Repeat("b", 64) {
		t.Errorf("Model(b).SHA256 = %q, want 64 b characters", b.SHA256)
	}
	if b.Input.Width != 640 || b.Input.Height != 640 || b.Input.Normalize || b.Input.Resize != "" {
		t.Errorf("Model(b).Input = %+v, want 640x640 unnormalized empty resize", b.Input)
	}
	if len(b.InputNames) != 1 || b.InputNames[0] != "images" || len(b.OutputNames) != 1 ||
		b.OutputNames[0] != "output0" {
		t.Errorf("Model(b) names = %v/%v, want [images]/[output0]", b.InputNames, b.OutputNames)
	}
	if len(b.Detector.Classes) != 2 || len(b.Detector.ExplicitClasses) != 1 || len(b.Detector.SuggestiveClasses) != 1 {
		t.Errorf("Model(b).Detector = %+v, want 2 classes, 1 explicit, 1 suggestive", b.Detector)
	}
	if b.Detector.ScoreThreshold != 0.35 || b.Detector.IoUThreshold != 0.5 {
		t.Errorf(
			"Model(b).Detector thresholds = %v/%v, want 0.35/0.5",
			b.Detector.ScoreThreshold,
			b.Detector.IoUThreshold,
		)
	}

	if _, err := m.Model("missing"); err == nil {
		t.Error("Model(missing) error = nil, want error")
	}
}

func TestManifestValidation(t *testing.T) {
	validSHA := strings.Repeat("0", 64)
	replace := func(s, old, replacement string) string { return strings.Replace(s, old, replacement, 1) }
	manifestDoc := func(profiles, models string) string {
		return fmt.Sprintf(`{"version": 1, "profiles": %s, "models": [%s]}`, profiles, models)
	}
	classifierDoc := func(model string) string {
		return manifestDoc(`{"balanced": {"models": ["a"]}}`, model)
	}
	detectorDoc := func(model string) string {
		return manifestDoc(`{"balanced": {"models": ["b"]}}`, model)
	}

	validClassifier := fmt.Sprintf(`{
  "id": "a", "kind": "classifier", "role": "photo", "file": "a.onnx", "sha256": %q,
  "input_names": ["input"], "output_names": ["logits"],
  "source": {"type": "hf", "url": "https://example.com/a.onnx"},
  "input": {"width": 224, "height": 224, "mean": [0, 0, 0], "std": [1, 1, 1], "normalize": false, "resize": "stretch"},
  "labels": ["neutral", "nsfw"],
  "output": {"activation": "softmax"},
  "required": true
}`, validSHA)
	validDetector := fmt.Sprintf(`{
  "id": "b", "kind": "detector", "role": "regions", "file": "b.onnx", "sha256": %q,
  "input_names": ["images"], "output_names": ["output0"],
  "source": {"type": "release", "url": "https://example.com/b.onnx"},
  "input": {"width": 640, "height": 640, "mean": [0, 0, 0], "std": [1, 1, 1], "normalize": false},
  "detector": {"classes": ["X"], "score_threshold": 0.3, "iou_threshold": 0.5, "explicit_classes": ["X"], "suggestive_classes": []},
  "required": false
}`, validSHA)

	cases := []struct {
		name    string
		doc     string
		wantErr string
	}{
		{
			name: "unsupported version",
			doc: fmt.Sprintf(
				`{"version": 2, "profiles": {"balanced": {"models": ["a"]}}, "models": [%s]}`,
				validClassifier,
			),
			wantErr: "unsupported version",
		},
		{
			name:    "no models",
			doc:     `{"version": 1, "profiles": {}, "models": []}`,
			wantErr: "no models",
		},
		{
			name:    "no profiles",
			doc:     manifestDoc(`{}`, validClassifier),
			wantErr: "no profiles",
		},
		{
			name:    "empty model id",
			doc:     manifestDoc(`{"balanced": {"models": [""]}}`, replace(validClassifier, `"id": "a"`, `"id": ""`)),
			wantErr: "empty id",
		},
		{
			name:    "duplicate model ids",
			doc:     manifestDoc(`{"balanced": {"models": ["a"]}}`, validClassifier+","+validClassifier),
			wantErr: "duplicate model id",
		},
		{
			name:    "profile references unknown model",
			doc:     manifestDoc(`{"balanced": {"models": ["missing"]}}`, validClassifier),
			wantErr: "unknown model",
		},
		{
			name:    "profile has duplicate models",
			doc:     manifestDoc(`{"balanced": {"models": ["a", "a"]}}`, validClassifier),
			wantErr: "duplicate model",
		},
		{
			name:    "empty profile",
			doc:     manifestDoc(`{"balanced": {"models": []}}`, validClassifier),
			wantErr: "no models",
		},
		{
			name:    "invalid kind",
			doc:     classifierDoc(replace(validClassifier, `"kind": "classifier"`, `"kind": "banana"`)),
			wantErr: "invalid kind",
		},
		{
			name:    "classifier missing role",
			doc:     classifierDoc(replace(validClassifier, `"role": "photo",`, "")),
			wantErr: "missing role",
		},
		{
			name:    "classifier invalid role",
			doc:     classifierDoc(replace(validClassifier, `"role": "photo"`, `"role": "banana"`)),
			wantErr: `invalid role "banana"`,
		},
		{
			name:    "classifier detector role",
			doc:     classifierDoc(replace(validClassifier, `"role": "photo"`, `"role": "regions"`)),
			wantErr: `invalid role "regions"`,
		},
		{
			name:    "detector invalid role",
			doc:     detectorDoc(replace(validDetector, `"role": "regions"`, `"role": "photo"`)),
			wantErr: `invalid role "photo"`,
		},
		{
			name:    "classifier missing labels",
			doc:     classifierDoc(replace(validClassifier, `"labels": ["neutral", "nsfw"],`, "")),
			wantErr: "labels: no entries",
		},
		{
			name:    "classifier duplicate labels",
			doc:     classifierDoc(replace(validClassifier, `["neutral", "nsfw"]`, `["neutral", "neutral"]`)),
			wantErr: `labels: duplicate entry "neutral"`,
		},
		{
			name:    "classifier empty label",
			doc:     classifierDoc(replace(validClassifier, `["neutral", "nsfw"]`, `["neutral", ""]`)),
			wantErr: "labels: empty entry",
		},
		{
			name:    "photo classifier missing neutral label",
			doc:     classifierDoc(replace(validClassifier, `["neutral", "nsfw"]`, `["high"]`)),
			wantErr: "neutral label",
		},
		{
			name:    "photo classifier missing severity label",
			doc:     classifierDoc(replace(validClassifier, `["neutral", "nsfw"]`, `["neutral"]`)),
			wantErr: "severity label",
		},
		{
			name: "anime classifier missing required label",
			doc: classifierDoc(replace(
				replace(validClassifier, `"role": "photo"`, `"role": "anime"`),
				`["neutral", "nsfw"]`, `["safe", "r15"]`)),
			wantErr: `anime labels missing "r18"`,
		},
		{
			name: "compat classifier missing required label",
			doc: classifierDoc(replace(
				replace(validClassifier, `"role": "photo"`, `"role": "compat"`),
				`["neutral", "nsfw"]`, `["drawings", "hentai", "neutral", "porn"]`)),
			wantErr: `compat labels missing "sexy"`,
		},
		{
			name:    "classifier invalid activation",
			doc:     classifierDoc(replace(validClassifier, `"softmax"`, `"sigmoid"`)),
			wantErr: "invalid activation",
		},
		{
			name:    "classifier missing input names",
			doc:     classifierDoc(replace(validClassifier, `"input_names": ["input"], `, "")),
			wantErr: "input_names: no entries",
		},
		{
			name:    "classifier empty input name",
			doc:     classifierDoc(replace(validClassifier, `"input_names": ["input"]`, `"input_names": [""]`)),
			wantErr: "input_names: empty entry",
		},
		{
			name: "classifier duplicate input names",
			doc: classifierDoc(
				replace(validClassifier, `"input_names": ["input"]`, `"input_names": ["input", "input"]`),
			),
			wantErr: `input_names: duplicate entry "input"`,
		},
		{
			name: "classifier too many input names",
			doc: classifierDoc(
				replace(validClassifier, `"input_names": ["input"]`, `"input_names": ["input", "input2"]`),
			),
			wantErr: "input_names: must have exactly one entry",
		},
		{
			name:    "classifier missing output names",
			doc:     classifierDoc(replace(validClassifier, `"output_names": ["logits"],`, "")),
			wantErr: "output_names: no entries",
		},
		{
			name:    "classifier empty output name",
			doc:     classifierDoc(replace(validClassifier, `"output_names": ["logits"]`, `"output_names": [""]`)),
			wantErr: "output_names: empty entry",
		},
		{
			name: "classifier duplicate output names",
			doc: classifierDoc(
				replace(validClassifier, `"output_names": ["logits"]`, `"output_names": ["logits", "logits"]`),
			),
			wantErr: `output_names: duplicate entry "logits"`,
		},
		{
			name: "classifier too many output names",
			doc: classifierDoc(
				replace(validClassifier, `"output_names": ["logits"]`, `"output_names": ["logits", "logits2"]`),
			),
			wantErr: "output_names: must have exactly one entry",
		},
		{
			name: "detector too many output names",
			doc: detectorDoc(
				replace(validDetector, `"output_names": ["output0"]`, `"output_names": ["output0", "output1"]`),
			),
			wantErr: "output_names: must have exactly one entry",
		},
		{
			name:    "detector missing input names",
			doc:     detectorDoc(replace(validDetector, `"input_names": ["images"], `, "")),
			wantErr: "input_names: no entries",
		},
		{
			name:    "detector missing output names",
			doc:     detectorDoc(replace(validDetector, `"output_names": ["output0"],`, "")),
			wantErr: "output_names: no entries",
		},
		{
			name: "detector missing spec",
			doc: detectorDoc(
				replace(
					validDetector,
					`"detector": {"classes": ["X"], "score_threshold": 0.3, "iou_threshold": 0.5, "explicit_classes": ["X"], "suggestive_classes": []},`,
					"",
				),
			),
			wantErr: "missing detector block",
		},
		{
			name:    "detector missing classes",
			doc:     detectorDoc(replace(validDetector, `"classes": ["X"]`, `"classes": []`)),
			wantErr: "classes: no entries",
		},
		{
			name:    "detector duplicate classes",
			doc:     detectorDoc(replace(validDetector, `"classes": ["X"]`, `"classes": ["X", "X"]`)),
			wantErr: `classes: duplicate entry "X"`,
		},
		{
			name:    "detector empty class name",
			doc:     detectorDoc(replace(validDetector, `"classes": ["X"]`, `"classes": [""]`)),
			wantErr: "classes: empty entry",
		},
		{
			name:    "detector unknown explicit class",
			doc:     detectorDoc(replace(validDetector, `"explicit_classes": ["X"]`, `"explicit_classes": ["Y"]`)),
			wantErr: `explicit_classes: unknown class "Y"`,
		},
		{
			name:    "detector unknown suggestive class",
			doc:     detectorDoc(replace(validDetector, `"suggestive_classes": []`, `"suggestive_classes": ["Y"]`)),
			wantErr: `suggestive_classes: unknown class "Y"`,
		},
		{
			name: "detector duplicate explicit class",
			doc: detectorDoc(replace(
				replace(validDetector, `"classes": ["X"]`, `"classes": ["X", "Y"]`),
				`"explicit_classes": ["X"]`, `"explicit_classes": ["X", "X"]`)),
			wantErr: `explicit_classes: duplicate entry "X"`,
		},
		{
			name:    "detector empty suggestive class",
			doc:     detectorDoc(replace(validDetector, `"suggestive_classes": []`, `"suggestive_classes": [""]`)),
			wantErr: "suggestive_classes: empty entry",
		},
		{
			name:    "detector score threshold above one",
			doc:     detectorDoc(replace(validDetector, `"score_threshold": 0.3`, `"score_threshold": 1.5`)),
			wantErr: "score_threshold",
		},
		{
			name:    "detector score threshold below zero",
			doc:     detectorDoc(replace(validDetector, `"score_threshold": 0.3`, `"score_threshold": -0.1`)),
			wantErr: "score_threshold",
		},
		{
			name:    "detector iou threshold zero",
			doc:     detectorDoc(replace(validDetector, `"iou_threshold": 0.5`, `"iou_threshold": 0`)),
			wantErr: "iou_threshold",
		},
		{
			name:    "detector iou threshold above one",
			doc:     detectorDoc(replace(validDetector, `"iou_threshold": 0.5`, `"iou_threshold": 1.5`)),
			wantErr: "iou_threshold",
		},
		{
			name:    "empty file",
			doc:     classifierDoc(replace(validClassifier, `"file": "a.onnx"`, `"file": ""`)),
			wantErr: "empty file",
		},
		{
			name:    "file path traversal",
			doc:     classifierDoc(replace(validClassifier, `"file": "a.onnx"`, `"file": "../evil.onnx"`)),
			wantErr: "invalid file path",
		},
		{
			name:    "file with directory separator",
			doc:     classifierDoc(replace(validClassifier, `"file": "a.onnx"`, `"file": "nested/a.onnx"`)),
			wantErr: "invalid file path",
		},
		{
			name:    "file with dot slash prefix",
			doc:     classifierDoc(replace(validClassifier, `"file": "a.onnx"`, `"file": "./a.onnx"`)),
			wantErr: "invalid file path",
		},
		{
			name:    "file without onnx extension",
			doc:     classifierDoc(replace(validClassifier, `"file": "a.onnx"`, `"file": "a.bin"`)),
			wantErr: ".onnx extension",
		},
		{
			name:    "short sha256",
			doc:     classifierDoc(replace(validClassifier, validSHA, "00")),
			wantErr: "invalid sha256",
		},
		{
			name:    "non-hex sha256",
			doc:     classifierDoc(replace(validClassifier, validSHA, strings.Repeat("z", 64))),
			wantErr: "invalid sha256",
		},
		{
			name:    "zero input width",
			doc:     classifierDoc(replace(validClassifier, `"width": 224`, `"width": 0`)),
			wantErr: "invalid input size",
		},
		{
			name:    "zero input height",
			doc:     classifierDoc(replace(validClassifier, `"height": 224`, `"height": 0`)),
			wantErr: "invalid input size",
		},
		{
			name:    "negative input width",
			doc:     classifierDoc(replace(validClassifier, `"width": 224`, `"width": -1`)),
			wantErr: "invalid input size",
		},
		{
			name: "input width over max dimension",
			doc: classifierDoc(
				replace(validClassifier, `"width": 224`, fmt.Sprintf(`"width": %d`, MaxInputDimension+1)),
			),
			wantErr: "invalid input size",
		},
		{
			name: "input height over max dimension",
			doc: classifierDoc(
				replace(validClassifier, `"height": 224`, fmt.Sprintf(`"height": %d`, MaxInputDimension+1)),
			),
			wantErr: "invalid input size",
		},
		{
			name:    "unsupported resize mode",
			doc:     classifierDoc(replace(validClassifier, `"resize": "stretch"`, `"resize": "letterbox"`)),
			wantErr: "unsupported resize mode",
		},
		{
			name: "normalizing with zero std",
			doc: classifierDoc(replace(
				replace(validClassifier, `"normalize": false`, `"normalize": true`),
				`"std": [1, 1, 1]`, `"std": [1, 0, 1]`)),
			wantErr: "std[1]",
		},
		{
			name:    "invalid source type",
			doc:     classifierDoc(replace(validClassifier, `"type": "hf"`, `"type": "ftp"`)),
			wantErr: "invalid source type",
		},
		{
			name:    "hf source empty url",
			doc:     classifierDoc(replace(validClassifier, `"url": "https://example.com/a.onnx"`, `"url": ""`)),
			wantErr: "absolute https url",
		},
		{
			name: "hf source not https",
			doc: classifierDoc(
				replace(validClassifier, `"url": "https://example.com/a.onnx"`, `"url": "http://example.com/a.onnx"`),
			),
			wantErr: "absolute https url",
		},
		{
			name:    "hf source relative url",
			doc:     classifierDoc(replace(validClassifier, `"url": "https://example.com/a.onnx"`, `"url": "/a.onnx"`)),
			wantErr: "absolute https url",
		},
		{
			name:    "release source unparseable url",
			doc:     detectorDoc(replace(validDetector, `"url": "https://example.com/b.onnx"`, `"url": ":foo"`)),
			wantErr: "invalid url",
		},
		{
			name: "unknown json field",
			doc: fmt.Sprintf(
				`{"version": 1, "profiles": {"balanced": {"models": ["a"]}}, "models": [%s], "extra": true}`,
				validClassifier,
			),
			wantErr: "unknown field",
		},
		{
			name:    "trailing json data",
			doc:     manifestDoc(`{"balanced": {"models": ["a"]}}`, validClassifier) + ` {}`,
			wantErr: "trailing data",
		},
		{
			name:    "invalid json",
			doc:     `{"version": 1, "profiles":`,
			wantErr: "parse manifest",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := writeTempFile(t, "manifest.json", c.doc)
			_, err := LoadManifest(path)
			if err == nil {
				t.Fatalf("LoadManifest() error = nil, want error containing %q", c.wantErr)
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("LoadManifest() error = %v, want error containing %q", err, c.wantErr)
			}
		})
	}

	t.Run("input size at max dimension is valid", func(t *testing.T) {
		doc := classifierDoc(replace(validClassifier, `"width": 224`, fmt.Sprintf(`"width": %d`, MaxInputDimension)))
		path := writeTempFile(t, "manifest.json", doc)
		if _, err := LoadManifest(path); err != nil {
			t.Fatalf("LoadManifest(input at MaxInputDimension) error = %v, want nil", err)
		}
	})

	t.Run("classifier role label shapes are valid", func(t *testing.T) {
		cases := []struct {
			name   string
			role   string
			labels string
		}{
			{"binary photo", RolePhoto, `["normal", "nsfw"]`},
			{"severity photo", RolePhoto, `["neutral", "low", "medium", "high"]`},
			{"anime", RoleAnime, `["safe", "r15", "r18"]`},
			{"compat", RoleCompat, `["drawings", "hentai", "neutral", "porn", "sexy"]`},
		}
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				doc := classifierDoc(replace(
					replace(validClassifier, `"role": "photo"`, fmt.Sprintf(`"role": %q`, c.role)),
					`["neutral", "nsfw"]`, c.labels))
				if _, err := LoadManifest(writeTempFile(t, "manifest.json", doc)); err != nil {
					t.Fatalf("LoadManifest(%s labels) error = %v, want nil", c.name, err)
				}
			})
		}
	})

	t.Run("detector empty role is valid", func(t *testing.T) {
		doc := detectorDoc(replace(validDetector, `"role": "regions",`, ""))
		if _, err := LoadManifest(writeTempFile(t, "manifest.json", doc)); err != nil {
			t.Fatalf("LoadManifest(detector empty role) error = %v, want nil", err)
		}
	})

	t.Run("non-finite normalize parameters", func(t *testing.T) {
		cases := []struct {
			name    string
			mutate  func(*Input)
			wantErr string
		}{
			{"nan mean", func(in *Input) { in.Mean[0] = float32(math.NaN()) }, "mean[0]"},
			{"positive inf mean", func(in *Input) { in.Mean[2] = float32(math.Inf(1)) }, "mean[2]"},
			{"nan std", func(in *Input) { in.Std[0] = float32(math.NaN()) }, "std[0]"},
			{"negative inf std", func(in *Input) { in.Std[1] = float32(math.Inf(-1)) }, "std[1]"},
		}
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				m := loadSampleManifest(t)
				c.mutate(&m.Models[0].Input)
				err := m.validate()
				if err == nil {
					t.Fatalf("validate() error = nil, want error containing %q", c.wantErr)
				}
				if !strings.Contains(err.Error(), c.wantErr) {
					t.Errorf("validate() error = %v, want error containing %q", err, c.wantErr)
				}
			})
		}
	})

	t.Run("non-finite detector thresholds", func(t *testing.T) {
		cases := []struct {
			name    string
			mutate  func(*DetectorSpec)
			wantErr string
		}{
			{
				"nan score threshold",
				func(d *DetectorSpec) { d.ScoreThreshold = float32(math.NaN()) },
				"score_threshold",
			},
			{
				"positive inf score threshold",
				func(d *DetectorSpec) { d.ScoreThreshold = float32(math.Inf(1)) },
				"score_threshold",
			},
			{"nan iou threshold", func(d *DetectorSpec) { d.IoUThreshold = float32(math.NaN()) }, "iou_threshold"},
			{
				"negative inf iou threshold",
				func(d *DetectorSpec) { d.IoUThreshold = float32(math.Inf(-1)) },
				"iou_threshold",
			},
		}
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				m := loadSampleManifest(t)
				c.mutate(m.Models[1].Detector)
				err := m.validate()
				if err == nil {
					t.Fatalf("validate() error = nil, want error containing %q", c.wantErr)
				}
				if !strings.Contains(err.Error(), c.wantErr) {
					t.Errorf("validate() error = %v, want error containing %q", err, c.wantErr)
				}
			})
		}
	})
}

func TestManifestCropPct(t *testing.T) {
	model := func(cropPct string) string {
		return fmt.Sprintf(`{
  "id": "a", "kind": "classifier", "role": "anime", "file": "a.onnx", "sha256": %q,
  "input_names": ["input"], "output_names": ["output"],
  "source": {"type": "hf", "url": "https://example.com/a.onnx"},
  "input": {"width": 384, "height": 384, "mean": [0.485, 0.456, 0.406], "std": [0.229, 0.224, 0.225], "normalize": true%s},
  "labels": ["safe", "r15", "r18"],
  "output": {"activation": "none"},
  "required": true
}`, strings.Repeat("0", 64), cropPct)
	}
	doc := func(model string) string {
		return fmt.Sprintf(`{"version": 1, "profiles": {"balanced": {"models": ["a"]}}, "models": [%s]}`, model)
	}

	for _, c := range []struct {
		name    string
		cropPct string
		want    float32
	}{
		{"full crop", `, "crop_pct": 1.0`, 1.0},
		{"timms 0.875", `, "crop_pct": 0.875`, 0.875},
		{"omitted is zero", ``, 0},
		{"explicit zero", `, "crop_pct": 0`, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			m, err := LoadManifest(writeTempFile(t, "manifest.json", doc(model(c.cropPct))))
			if err != nil {
				t.Fatalf("LoadManifest(%s) error = %v, want nil", c.name, err)
			}
			a, err := m.Model("a")
			if err != nil {
				t.Fatalf("Model(a) error = %v, want nil", err)
			}
			if a.Input.CropPct != c.want {
				t.Errorf("Model(a).Input.CropPct = %v, want %v", a.Input.CropPct, c.want)
			}
		})
	}

	for _, cropPct := range []string{`1.5`, `-0.1`, `0.05`} {
		t.Run("crop_pct "+cropPct, func(t *testing.T) {
			_, err := LoadManifest(writeTempFile(t, "manifest.json", doc(model(`, "crop_pct": `+cropPct))))
			if err == nil {
				t.Fatalf("LoadManifest(crop_pct %s) error = nil, want error", cropPct)
			}
			if !strings.Contains(err.Error(), "crop_pct") {
				t.Errorf("LoadManifest(crop_pct %s) error = %v, want error mentioning crop_pct", cropPct, err)
			}
		})
	}

	t.Run("non-finite crop_pct", func(t *testing.T) {
		m := loadSampleManifest(t)
		m.Models[0].Input.CropPct = float32(math.NaN())
		if err := m.validate(); err == nil || !strings.Contains(err.Error(), "crop_pct") {
			t.Errorf("validate(NaN crop_pct) error = %v, want error mentioning crop_pct", err)
		}
		m.Models[0].Input.CropPct = float32(math.Inf(1))
		if err := m.validate(); err == nil || !strings.Contains(err.Error(), "crop_pct") {
			t.Errorf("validate(+Inf crop_pct) error = %v, want error mentioning crop_pct", err)
		}
	})
}

func TestManifestInterpolation(t *testing.T) {
	model := func(interpolation string) string {
		return fmt.Sprintf(`{
  "id": "a", "kind": "classifier", "role": "anime", "file": "a.onnx", "sha256": %q,
  "input_names": ["input"], "output_names": ["output"],
  "source": {"type": "hf", "url": "https://example.com/a.onnx"},
  "input": {"width": 224, "height": 224, "mean": [0.5, 0.5, 0.5], "std": [0.5, 0.5, 0.5], "normalize": true%s},
  "labels": ["safe", "r15", "r18"],
  "output": {"activation": "softmax"},
  "required": false
}`, strings.Repeat("0", 64), interpolation)
	}
	doc := func(model string) string {
		return fmt.Sprintf(`{"version": 1, "profiles": {"balanced": {"models": ["a"]}}, "models": [%s]}`, model)
	}

	for _, c := range []struct {
		name          string
		interpolation string
		want          string
	}{
		{"omitted stays empty", ``, ""},
		{"bicubic", `, "interpolation": "bicubic"`, InterpolationBicubic},
		{"bilinear", `, "interpolation": "bilinear"`, InterpolationBilinear},
	} {
		t.Run(c.name, func(t *testing.T) {
			m, err := LoadManifest(writeTempFile(t, "manifest.json", doc(model(c.interpolation))))
			if err != nil {
				t.Fatalf("LoadManifest(%s) error = %v, want nil", c.name, err)
			}
			a, err := m.Model("a")
			if err != nil {
				t.Fatalf("Model(a) error = %v, want nil", err)
			}
			if a.Input.Interpolation != c.want {
				t.Errorf("Model(a).Input.Interpolation = %q, want %q", a.Input.Interpolation, c.want)
			}
		})
	}

	for _, interpolation := range []string{"nearest", "BILINEAR", "bicubic "} {
		t.Run("invalid "+interpolation, func(t *testing.T) {
			_, err := LoadManifest(
				writeTempFile(t, "manifest.json", doc(model(`, "interpolation": `+fmt.Sprintf("%q", interpolation)))),
			)
			if err == nil {
				t.Fatalf("LoadManifest(interpolation %q) error = nil, want error", interpolation)
			}
			if !strings.Contains(err.Error(), "interpolation") {
				t.Errorf(
					"LoadManifest(interpolation %q) error = %v, want error mentioning interpolation",
					interpolation,
					err,
				)
			}
		})
	}
}

func TestManifestProvenance(t *testing.T) {
	model := func(extra string) string {
		return fmt.Sprintf(`{
  "id": "a", "kind": "classifier", "role": "anime", "file": "a.onnx", "sha256": %q,
  "input_names": ["input"], "output_names": ["output"],
  "source": {"type": "local"},
  "input": {"width": 224, "height": 224},
  "labels": ["safe", "r15", "r18"],
  "output": {"activation": "none"},
  "required": false%s
}`, strings.Repeat("0", 64), extra)
	}
	doc := func(model string) string {
		return fmt.Sprintf(`{"version": 1, "profiles": {"balanced": {"models": ["a"]}}, "models": [%s]}`, model)
	}
	load := func(t *testing.T, extra string) (Model, error) {
		t.Helper()
		m, err := LoadManifest(writeTempFile(t, "manifest.json", doc(model(extra))))
		if err != nil {
			return Model{}, err
		}
		a, err := m.Model("a")
		return a, err
	}

	t.Run("license and source_repo parse", func(t *testing.T) {
		a, err := load(t, `, "license": "AGPL-3.0", "source_repo": "https://example.com/repo"`)
		if err != nil {
			t.Fatalf("LoadManifest(provenance) error = %v, want nil", err)
		}
		if a.License != "AGPL-3.0" {
			t.Errorf("License = %q, want %q", a.License, "AGPL-3.0")
		}
		if a.SourceRepo != "https://example.com/repo" {
			t.Errorf("SourceRepo = %q, want %q", a.SourceRepo, "https://example.com/repo")
		}
	})

	t.Run("omitted provenance stays empty", func(t *testing.T) {
		a, err := load(t, "")
		if err != nil {
			t.Fatalf("LoadManifest(no provenance) error = %v, want nil", err)
		}
		if a.License != "" || a.SourceRepo != "" {
			t.Errorf("License/SourceRepo = %q/%q, want empty", a.License, a.SourceRepo)
		}
	})

	for _, c := range []struct {
		name    string
		extra   string
		wantErr string
	}{
		{"padded license", `, "license": " MIT "`, "license"},
		{"whitespace-only license", `, "license": "   "`, "license"},
		{"padded source_repo", `, "source_repo": " https://example.com "`, "source_repo"},
		{"whitespace-only source_repo", `, "source_repo": "\t"`, "source_repo"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := load(t, c.extra)
			if err == nil {
				t.Fatalf("LoadManifest(%s) error = nil, want error", c.name)
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("LoadManifest(%s) error = %v, want error mentioning %s", c.name, err, c.wantErr)
			}
		})
	}
}

func TestManifestLocalSourceAndUnnormalizedInput(t *testing.T) {
	model := fmt.Sprintf(`{
  "id": "m", "kind": "classifier", "role": "compat", "file": "m.ONNX", "sha256": %q,
  "input_names": ["input"], "output_names": ["output"],
  "source": {"type": "local", "url": ""},
  "input": {"width": 224, "height": 224, "mean": [0, 0, 0], "std": [0, 0, 0], "normalize": false},
  "labels": ["drawings", "hentai", "neutral", "porn", "sexy"],
  "output": {"activation": "none"},
  "required": false
}`, strings.Repeat("aB", 32))
	doc := fmt.Sprintf(`{"version": 1, "profiles": {"balanced": {"models": ["m"]}}, "models": [%s]}`, model)
	if _, err := LoadManifest(writeTempFile(t, "manifest.json", doc)); err != nil {
		t.Fatalf("LoadManifest(local source) error = %v, want nil", err)
	}
}

func TestManifestThresholdBoundaries(t *testing.T) {
	model := fmt.Sprintf(`{
  "id": "d", "kind": "detector", "file": "d.onnx", "sha256": %q,
  "input_names": ["images"], "output_names": ["output0"],
  "source": {"type": "local"},
  "input": {"width": 640, "height": 640},
  "detector": {"classes": ["X"], "score_threshold": 0, "iou_threshold": 1, "explicit_classes": [], "suggestive_classes": []},
  "required": true
}`, strings.Repeat("0", 64))
	doc := fmt.Sprintf(`{"version": 1, "profiles": {"balanced": {"models": ["d"]}}, "models": [%s]}`, model)
	if _, err := LoadManifest(writeTempFile(t, "manifest.json", doc)); err != nil {
		t.Fatalf("LoadManifest(boundary thresholds) error = %v, want nil", err)
	}
}

func TestSHA256Verify(t *testing.T) {
	content := []byte("layerforge nsfw_sherlock model bytes\n")
	path := writeTempFile(t, "model.onnx", string(content))
	sum := sha256.Sum256(content)
	want := hex.EncodeToString(sum[:])

	if err := VerifyFile(path, want); err != nil {
		t.Errorf("VerifyFile(correct) error = %v, want nil", err)
	}
	if err := VerifyFile(path, strings.ToUpper(want)); err != nil {
		t.Errorf("VerifyFile(uppercase) error = %v, want nil", err)
	}
	if err := VerifyFile(path, strings.Repeat("0", 64)); err == nil {
		t.Error("VerifyFile(wrong hash) error = nil, want error")
	}
	if err := VerifyFile(filepath.Join(t.TempDir(), "missing.onnx"), want); err == nil {
		t.Error("VerifyFile(missing file) error = nil, want error")
	}

	err := VerifyFile(path, "nothex")
	if err == nil {
		t.Error("VerifyFile(invalid hash) error = nil, want error")
	} else if !strings.Contains(err.Error(), "invalid sha256") {
		t.Errorf("VerifyFile(invalid hash) error = %v, want invalid sha256 error", err)
	}
	if err := VerifyFile(path, ""); err == nil {
		t.Error("VerifyFile(empty hash) error = nil, want error")
	}
}
