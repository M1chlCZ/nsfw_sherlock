package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestRealManifest(t *testing.T) {
	const path = "../models/manifest.json"
	if _, err := os.Stat(path); err != nil {
		t.Skipf("real manifest not present: %v", err)
	}

	m, err := LoadManifest(path)
	if err != nil {
		t.Fatalf("LoadManifest(%s) error = %v, want nil", path, err)
	}

	wantProfiles := map[string][]string{
		"balanced": {"photo-freepik", "anime-caformer", "regions-nudenet-320"},
		"max":      {"photo-freepik", "anime-caformer", "regions-nudenet-640"},
		"fast":     {"photo-freepik", "anime-mobilenet", "regions-nudenet-320"},
		"compat":   {"photo-compat-vit5"},
	}
	for name, want := range wantProfiles {
		got, err := m.Profile(name)
		if err != nil {
			t.Errorf("Profile(%q) error = %v, want nil", name, err)
			continue
		}
		if !slices.Equal(got, want) {
			t.Errorf("Profile(%q) = %v, want %v", name, got, want)
		}
	}

	wantModels := []struct {
		id         string
		kind       string
		role       string
		required   bool
		activation string
		license    string
		sourceRepo string
	}{
		{
			id: "photo-freepik", kind: KindClassifier, role: RolePhoto,
			required: true, activation: ActivationSoftmax,
			license:    "MIT",
			sourceRepo: "https://huggingface.co/Freepik/nsfw_image_detector",
		},
		{
			id: "photo-compat-vit5", kind: KindClassifier, role: RoleCompat,
			required: false, activation: ActivationSoftmax,
			license:    "CC-BY-NC-ND-4.0",
			sourceRepo: "https://huggingface.co/giacomoarienti/nsfw-classifier",
		},
		{
			id: "anime-caformer", kind: KindClassifier, role: RoleAnime,
			required: true, activation: ActivationNone,
			license:    "MIT",
			sourceRepo: "https://huggingface.co/deepghs/anime_rating",
		},
		{
			id: "anime-mobilenet", kind: KindClassifier, role: RoleAnime,
			required: false, activation: ActivationNone,
			license:    "MIT",
			sourceRepo: "https://huggingface.co/deepghs/anime_rating",
		},
		{
			id: "regions-nudenet-320", kind: KindDetector, role: RoleRegions,
			required: false, activation: "",
			license:    "AGPL-3.0",
			sourceRepo: "https://github.com/notAI-tech/NudeNet (mirror https://huggingface.co/deepghs/nudenet_onnx)",
		},
		{
			id: "regions-nudenet-640", kind: KindDetector, role: RoleRegions,
			required: false, activation: "",
			license:    "AGPL-3.0",
			sourceRepo: "https://github.com/notAI-tech/NudeNet (mirror https://huggingface.co/Kalashnikov/NudeNet)",
		},
	}
	for _, want := range wantModels {
		model, err := m.Model(want.id)
		if err != nil {
			t.Errorf("Model(%q) error = %v, want nil", want.id, err)
			continue
		}
		if model.Kind != want.kind || model.Role != want.role || model.Required != want.required {
			t.Errorf(
				"model %q = kind %q role %q required %v, want %q %q %v",
				want.id, model.Kind, model.Role, model.Required, want.kind, want.role, want.required,
			)
		}
		if model.Output.Activation != want.activation {
			t.Errorf("model %q activation = %q, want %q", want.id, model.Output.Activation, want.activation)
		}
		if model.License != want.license {
			t.Errorf("model %q license = %q, want %q", want.id, model.License, want.license)
		}
		if model.SourceRepo != want.sourceRepo {
			t.Errorf("model %q source_repo = %q, want %q", want.id, model.SourceRepo, want.sourceRepo)
		}
	}

	t.Run("parity fixtures", func(t *testing.T) {
		const fixturesDir = "../testdata/parity"
		if _, err := os.Stat(fixturesDir); err != nil {
			t.Skipf("parity fixtures not present: %v", err)
		}
		for _, model := range m.Models {
			fixturePath := filepath.Join(fixturesDir, model.File+".json")
			data, err := os.ReadFile(fixturePath)
			if err != nil {
				t.Errorf("model %q: read fixture %s: %v", model.ID, fixturePath, err)
				continue
			}
			var fixture struct {
				Model  string `json:"model"`
				SHA256 string `json:"sha256"`
			}
			if err := json.Unmarshal(data, &fixture); err != nil {
				t.Errorf("model %q: parse fixture %s: %v", model.ID, fixturePath, err)
				continue
			}
			if fixture.Model != model.File {
				t.Errorf("model %q: fixture model = %q, want %q", model.ID, fixture.Model, model.File)
			}
			if fixture.SHA256 != model.SHA256 {
				t.Errorf("model %q: fixture sha256 = %q, want %q", model.ID, fixture.SHA256, model.SHA256)
			}
		}
	})
}
