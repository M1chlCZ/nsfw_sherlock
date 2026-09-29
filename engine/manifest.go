package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const (
	KindClassifier = "classifier"
	KindDetector   = "detector"
)

// Model roles describe how a model's scores take part in Combine.
const (
	RolePhoto   = "photo"
	RoleAnime   = "anime"
	RoleCompat  = "compat"
	RoleRegions = "regions"
)

const (
	ActivationSoftmax = "softmax"
	ActivationNone    = "none"
)

const (
	SourceHF      = "hf"
	SourceRelease = "release"
	SourceLocal   = "local"
)

// Manifest describes the available ONNX models and the profiles that group them.
type Manifest struct {
	Version  int                `json:"version"`
	Profiles map[string]Profile `json:"profiles"`
	Models   []Model            `json:"models"`
}

// Profile is a named, ordered list of model IDs.
type Profile struct {
	Models []string `json:"models"`
}

// Model describes a single ONNX model and how to fetch and preprocess it.
type Model struct {
	ID          string        `json:"id"`
	Kind        string        `json:"kind"`
	Role        string        `json:"role"`
	File        string        `json:"file"`
	SHA256      string        `json:"sha256"`
	InputNames  []string      `json:"input_names"`
	OutputNames []string      `json:"output_names"`
	Source      Source        `json:"source"`
	Input       Input         `json:"input"`
	Labels      []string      `json:"labels,omitempty"`
	Output      Output        `json:"output"`
	Detector    *DetectorSpec `json:"detector,omitempty"`
	Required    bool          `json:"required"`
	License     string        `json:"license,omitempty"`
	SourceRepo  string        `json:"source_repo,omitempty"`
}

// Source identifies where a model file is downloaded from.
type Source struct {
	Type string `json:"type"`
	URL  string `json:"url"`
}

// Input describes the tensor Preprocess builds for a model.
type Input struct {
	Width         int        `json:"width"`
	Height        int        `json:"height"`
	Mean          [3]float32 `json:"mean"`
	Std           [3]float32 `json:"std"`
	Normalize     bool       `json:"normalize"`
	Resize        ResizeMode `json:"resize"`
	CropPct       float32    `json:"crop_pct,omitempty"`
	Interpolation string     `json:"interpolation,omitempty"`
}

// Output describes how a classifier's raw scores are turned into labels.
type Output struct {
	Activation string `json:"activation"`
}

// DetectorSpec configures a detector model's post-processing and class grouping.
type DetectorSpec struct {
	Classes           []string `json:"classes"`
	ScoreThreshold    float32  `json:"score_threshold"`
	IoUThreshold      float32  `json:"iou_threshold"`
	ExplicitClasses   []string `json:"explicit_classes"`
	SuggestiveClasses []string `json:"suggestive_classes"`
}

// LoadManifest reads and validates a manifest JSON file.
func LoadManifest(path string) (*Manifest, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("engine: open manifest: %w", err)
	}
	defer func() { _ = f.Close() }()

	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("engine: parse manifest %s: %w", path, err)
	}
	var trailing struct{}
	if err := dec.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("engine: parse manifest %s: unexpected trailing data", path)
	}
	if err := m.validate(); err != nil {
		return nil, fmt.Errorf("engine: manifest %s: %w", path, err)
	}
	return &m, nil
}

// Profile returns a copy of the ordered model IDs for the named profile.
func (m *Manifest) Profile(name string) ([]string, error) {
	p, ok := m.Profiles[name]
	if !ok {
		return nil, fmt.Errorf("engine: unknown profile %q", name)
	}
	return append([]string(nil), p.Models...), nil
}

// Model returns the model with the given ID.
func (m *Manifest) Model(id string) (Model, error) {
	for _, model := range m.Models {
		if model.ID == id {
			return model, nil
		}
	}
	return Model{}, fmt.Errorf("engine: unknown model %q", id)
}

// VerifyFile checks that the file at path has the expected SHA-256 digest.
func VerifyFile(path, wantSHA256 string) error {
	if err := validateSHA256(wantSHA256); err != nil {
		return fmt.Errorf("engine: verify %s: %w", path, err)
	}

	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("engine: verify %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("engine: hash %s: %w", path, err)
	}
	got := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(got, wantSHA256) {
		return fmt.Errorf("engine: sha256 mismatch for %s: got %s, want %s", path, got, wantSHA256)
	}
	return nil
}

func (m *Manifest) validate() error {
	if m.Version != 1 {
		return fmt.Errorf("unsupported version %d", m.Version)
	}
	if len(m.Models) == 0 {
		return fmt.Errorf("no models")
	}
	ids := make(map[string]struct{}, len(m.Models))
	for _, model := range m.Models {
		if model.ID == "" {
			return fmt.Errorf("model with empty id")
		}
		if _, ok := ids[model.ID]; ok {
			return fmt.Errorf("duplicate model id %q", model.ID)
		}
		ids[model.ID] = struct{}{}
		if err := model.validate(); err != nil {
			return fmt.Errorf("model %q: %w", model.ID, err)
		}
	}
	if len(m.Profiles) == 0 {
		return fmt.Errorf("no profiles")
	}
	for name, profile := range m.Profiles {
		if len(profile.Models) == 0 {
			return fmt.Errorf("profile %q: no models", name)
		}
		seen := make(map[string]struct{}, len(profile.Models))
		for _, id := range profile.Models {
			if _, ok := ids[id]; !ok {
				return fmt.Errorf("profile %q: unknown model %q", name, id)
			}
			if _, ok := seen[id]; ok {
				return fmt.Errorf("profile %q: duplicate model %q", name, id)
			}
			seen[id] = struct{}{}
		}
	}
	return nil
}

func (m *Model) validate() error {
	switch m.Kind {
	case KindClassifier:
		switch m.Role {
		case RolePhoto, RoleAnime, RoleCompat:
		case "":
			return fmt.Errorf("classifier: missing role")
		default:
			return fmt.Errorf("classifier: invalid role %q", m.Role)
		}
		if err := validateNames(m.Labels, "labels"); err != nil {
			return err
		}
		if err := validateRoleLabels(m.Role, m.Labels); err != nil {
			return err
		}
		if m.Output.Activation != ActivationSoftmax && m.Output.Activation != ActivationNone {
			return fmt.Errorf("classifier: invalid activation %q", m.Output.Activation)
		}
	case KindDetector:
		if m.Role != "" && m.Role != RoleRegions {
			return fmt.Errorf("detector: invalid role %q", m.Role)
		}
		if m.Detector == nil {
			return fmt.Errorf("detector: missing detector block")
		}
		if err := m.Detector.validate(); err != nil {
			return err
		}
	default:
		return fmt.Errorf("invalid kind %q", m.Kind)
	}
	if err := validateFile(m.File); err != nil {
		return err
	}
	if err := validateTensorNames(m.InputNames, "input_names"); err != nil {
		return err
	}
	if err := validateTensorNames(m.OutputNames, "output_names"); err != nil {
		return err
	}
	if m.Input.Width <= 0 || m.Input.Height <= 0 || m.Input.Width > MaxInputDimension || m.Input.Height > MaxInputDimension {
		return fmt.Errorf("invalid input size %dx%d: must be between 1 and %d", m.Input.Width, m.Input.Height, MaxInputDimension)
	}
	if m.Input.Resize != "" && m.Input.Resize != ResizeStretch {
		return fmt.Errorf("unsupported resize mode %q", m.Input.Resize)
	}
	if math.IsNaN(float64(m.Input.CropPct)) || m.Input.CropPct < 0 || m.Input.CropPct > 1 || (m.Input.CropPct > 0 && m.Input.CropPct < MinCropPct) {
		return fmt.Errorf("input: crop_pct %v must be 0 or in [%v, 1]", m.Input.CropPct, MinCropPct)
	}
	if err := validateInterpolation(m.Input.Interpolation); err != nil {
		return fmt.Errorf("input: %w", err)
	}
	if m.Input.Normalize {
		for c := 0; c < 3; c++ {
			if !isFinite(m.Input.Mean[c]) {
				return fmt.Errorf("input: mean[%d] must be finite", c)
			}
			if !isFinite(m.Input.Std[c]) || m.Input.Std[c] == 0 {
				return fmt.Errorf("input: std[%d] must be finite and non-zero", c)
			}
		}
	}
	if err := validateOptionalText(m.License, "license"); err != nil {
		return err
	}
	if err := validateOptionalText(m.SourceRepo, "source_repo"); err != nil {
		return err
	}
	if err := validateSHA256(m.SHA256); err != nil {
		return err
	}
	return m.Source.validate()
}

func validateOptionalText(value, field string) error {
	if value == "" {
		return nil
	}
	if strings.TrimSpace(value) != value {
		return fmt.Errorf("%s %q must not be whitespace-only or padded with whitespace", field, value)
	}
	return nil
}

func (s *Source) validate() error {
	switch s.Type {
	case SourceHF, SourceRelease:
		u, err := url.Parse(s.URL)
		if err != nil {
			return fmt.Errorf("invalid url %q: %w", s.URL, err)
		}
		if u.Scheme != "https" || u.Host == "" {
			return fmt.Errorf("url %q must be an absolute https url", s.URL)
		}
	case SourceLocal:
	default:
		return fmt.Errorf("invalid source type %q", s.Type)
	}
	return nil
}

func (d *DetectorSpec) validate() error {
	if err := validateNames(d.Classes, "classes"); err != nil {
		return err
	}
	if err := validateClassRefs(d.ExplicitClasses, d.Classes, "explicit_classes"); err != nil {
		return err
	}
	if err := validateClassRefs(d.SuggestiveClasses, d.Classes, "suggestive_classes"); err != nil {
		return err
	}
	score := float64(d.ScoreThreshold)
	if math.IsNaN(score) || math.IsInf(score, 0) || score < 0 || score > 1 {
		return fmt.Errorf("score_threshold %v must be finite in [0, 1]", d.ScoreThreshold)
	}
	iou := float64(d.IoUThreshold)
	if math.IsNaN(iou) || math.IsInf(iou, 0) || iou <= 0 || iou > 1 {
		return fmt.Errorf("iou_threshold %v must be finite in (0, 1]", d.IoUThreshold)
	}
	return nil
}

func validateRoleLabels(role string, labels []string) error {
	hasAny := func(candidates ...string) bool {
		for _, candidate := range candidates {
			if slices.Contains(labels, candidate) {
				return true
			}
		}
		return false
	}
	switch role {
	case RolePhoto:
		if !hasAny("neutral", "sfw", "normal") {
			return fmt.Errorf("classifier: photo labels missing a neutral label (one of %q, %q, %q)", "neutral", "sfw", "normal")
		}
		if !hasAny("high", "nsfw", "porn") {
			return fmt.Errorf("classifier: photo labels missing a severity label (one of %q, %q, %q)", "high", "nsfw", "porn")
		}
	case RoleAnime:
		for _, required := range []string{"safe", "r15", "r18"} {
			if !slices.Contains(labels, required) {
				return fmt.Errorf("classifier: anime labels missing %q", required)
			}
		}
	case RoleCompat:
		for _, required := range []string{"drawings", "hentai", "neutral", "porn", "sexy"} {
			if !slices.Contains(labels, required) {
				return fmt.Errorf("classifier: compat labels missing %q", required)
			}
		}
	}
	return nil
}

func validateNames(names []string, field string) error {
	if len(names) == 0 {
		return fmt.Errorf("%s: no entries", field)
	}
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		if name == "" {
			return fmt.Errorf("%s: empty entry", field)
		}
		if _, ok := seen[name]; ok {
			return fmt.Errorf("%s: duplicate entry %q", field, name)
		}
		seen[name] = struct{}{}
	}
	return nil
}

func validateTensorNames(names []string, field string) error {
	if err := validateNames(names, field); err != nil {
		return err
	}
	if len(names) != 1 {
		return fmt.Errorf("%s: must have exactly one entry", field)
	}
	return nil
}

func validateClassRefs(refs, classes []string, field string) error {
	valid := make(map[string]struct{}, len(classes))
	for _, class := range classes {
		valid[class] = struct{}{}
	}
	seen := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		if ref == "" {
			return fmt.Errorf("%s: empty entry", field)
		}
		if _, ok := seen[ref]; ok {
			return fmt.Errorf("%s: duplicate entry %q", field, ref)
		}
		seen[ref] = struct{}{}
		if _, ok := valid[ref]; !ok {
			return fmt.Errorf("%s: unknown class %q", field, ref)
		}
	}
	return nil
}

func validateFile(file string) error {
	if file == "" {
		return fmt.Errorf("empty file")
	}
	if !filepath.IsLocal(file) || filepath.Base(file) != file {
		return fmt.Errorf("invalid file path %q", file)
	}
	if !strings.EqualFold(filepath.Ext(file), ".onnx") {
		return fmt.Errorf("file %q must have an .onnx extension", file)
	}
	return nil
}

func validateSHA256(sha string) error {
	if len(sha) != 64 {
		return fmt.Errorf("invalid sha256 %q: must be 64 hex characters", sha)
	}
	if _, err := hex.DecodeString(sha); err != nil {
		return fmt.Errorf("invalid sha256 %q: must be 64 hex characters", sha)
	}
	return nil
}

func isFinite(f float32) bool {
	return !math.IsNaN(float64(f)) && !math.IsInf(float64(f), 0)
}
