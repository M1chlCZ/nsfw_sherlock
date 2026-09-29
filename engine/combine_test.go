package engine

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

var combineRules = DetectionRules{
	Explicit:   []string{"EXPOSED"},
	Suggestive: []string{"COVERED"},
}

func classifierOutput(id, role string, scores map[string]float32) ModelOutput {
	return ModelOutput{ID: id, Kind: KindClassifier, Role: role, Scores: scores}
}

func detection(label string, score float32) Detection {
	return Detection{Label: label, Score: score}
}

func closeEnough(got, want float32) bool {
	return math.Abs(float64(got-want)) < 1e-6
}

func labelsCloseTo(got, want Labels) bool {
	return closeEnough(got.Drawings, want.Drawings) &&
		closeEnough(got.Hentai, want.Hentai) &&
		closeEnough(got.Neutral, want.Neutral) &&
		closeEnough(got.Porn, want.Porn) &&
		closeEnough(got.Sexy, want.Sexy)
}

func TestCombine(t *testing.T) {
	safePhoto := classifierOutput("photo", RolePhoto, map[string]float32{"neutral": 0.99})
	safeAnime := classifierOutput("anime", RoleAnime, map[string]float32{"safe": 0.99})

	cases := []struct {
		name        string
		profile     string
		outputs     []ModelOutput
		dets        []Detection
		wantVerdict string
		wantNSFW    float32
		wantLabels  Labels
		wantPhoto   bool
		wantAnime   bool
		wantDets    int
		wantLegacy  bool
	}{
		{
			name:        "photo high is explicit",
			outputs:     []ModelOutput{classifierOutput("photo", RolePhoto, map[string]float32{"high": 0.9, "neutral": 0.05})},
			wantVerdict: VerdictExplicit,
			wantNSFW:    0.9,
			wantLabels:  Labels{Neutral: 0.05, Porn: 0.9},
			wantPhoto:   true,
			wantLegacy:  true,
		},
		{
			name:        "photo medium is suggestive",
			outputs:     []ModelOutput{classifierOutput("photo", RolePhoto, map[string]float32{"medium": 0.6, "neutral": 0.1})},
			wantVerdict: VerdictSuggestive,
			wantNSFW:    0,
			wantLabels:  Labels{Neutral: 0.1, Sexy: 0.6},
			wantPhoto:   true,
			wantLegacy:  true,
		},
		{
			name:        "photo low is weighted",
			outputs:     []ModelOutput{classifierOutput("photo", RolePhoto, map[string]float32{"low": 0.6, "medium": 0})},
			wantVerdict: VerdictSFW,
			wantNSFW:    0,
			wantLabels:  Labels{Sexy: 0.3},
			wantPhoto:   true,
			wantLegacy:  true,
		},
		{
			name:        "photo sexy fallback key is suggestive",
			outputs:     []ModelOutput{classifierOutput("photo", RolePhoto, map[string]float32{"sexy": 0.6, "neutral": 0.1})},
			wantVerdict: VerdictSuggestive,
			wantNSFW:    0,
			wantLabels:  Labels{Neutral: 0.1, Sexy: 0.6},
			wantPhoto:   true,
			wantLegacy:  true,
		},
		{
			name:        "photo medium is capped at one",
			outputs:     []ModelOutput{classifierOutput("photo", RolePhoto, map[string]float32{"medium": 1.5, "neutral": 0.1})},
			wantVerdict: VerdictSuggestive,
			wantNSFW:    0,
			wantLabels:  Labels{Neutral: 0.1, Sexy: 1},
			wantPhoto:   true,
			wantLegacy:  true,
		},
		{
			name:        "photo low dominates at the weighted boundary",
			outputs:     []ModelOutput{classifierOutput("photo", RolePhoto, map[string]float32{"low": 1, "medium": 0})},
			wantVerdict: VerdictSuggestive,
			wantNSFW:    0,
			wantLabels:  Labels{Sexy: 0.5},
			wantPhoto:   true,
			wantLegacy:  true,
		},
		{
			name:        "sexy is excluded from the nsfw scalar",
			outputs:     []ModelOutput{classifierOutput("photo", RolePhoto, map[string]float32{"medium": 0.9})},
			wantVerdict: VerdictSuggestive,
			wantNSFW:    0,
			wantLabels:  Labels{Sexy: 0.9},
			wantPhoto:   true,
			wantLegacy:  true,
		},
		{
			name:        "anime r18 is explicit",
			outputs:     []ModelOutput{classifierOutput("anime", RoleAnime, map[string]float32{"safe": 0.1, "r18": 0.8})},
			wantVerdict: VerdictExplicit,
			wantNSFW:    0.8,
			wantLabels:  Labels{Drawings: 0.1, Hentai: 0.8},
			wantAnime:   true,
			wantLegacy:  true,
		},
		{
			name:        "explicit detection beats safe classifiers",
			outputs:     []ModelOutput{safePhoto, safeAnime},
			dets:        []Detection{detection("EXPOSED", 0.4)},
			wantVerdict: VerdictExplicit,
			wantNSFW:    0.4,
			wantLabels:  Labels{Drawings: 0.99, Neutral: 0.99},
			wantPhoto:   true,
			wantAnime:   true,
			wantDets:    1,
			wantLegacy:  true,
		},
		{
			name:        "suggestive detection",
			outputs:     []ModelOutput{safePhoto},
			dets:        []Detection{detection("COVERED", 0.4)},
			wantVerdict: VerdictSuggestive,
			wantNSFW:    0,
			wantLabels:  Labels{Neutral: 0.99},
			wantPhoto:   true,
			wantDets:    1,
			wantLegacy:  false,
		},
		{
			name:        "unknown detection label is ignored",
			outputs:     []ModelOutput{safePhoto},
			dets:        []Detection{detection("OTHER", 0.99)},
			wantVerdict: VerdictSFW,
			wantNSFW:    0,
			wantLabels:  Labels{Neutral: 0.99},
			wantPhoto:   true,
			wantDets:    1,
			wantLegacy:  false,
		},
		{
			name:        "compat copies labels verbatim",
			profile:     "compat",
			outputs:     []ModelOutput{classifierOutput("compat", RoleCompat, map[string]float32{"drawings": 0.05, "hentai": 0.07, "neutral": 0.11, "porn": 0.55, "sexy": 0.22})},
			wantVerdict: VerdictExplicit,
			wantNSFW:    0.55,
			wantLabels:  Labels{Drawings: 0.05, Hentai: 0.07, Neutral: 0.11, Porn: 0.55, Sexy: 0.22},
			wantLegacy:  true,
		},
		{
			name:        "compat with explicit detection feeds the nsfw scalar",
			profile:     "compat",
			outputs:     []ModelOutput{classifierOutput("compat", RoleCompat, map[string]float32{"drawings": 0.05, "hentai": 0.07, "neutral": 0.11, "porn": 0.01, "sexy": 0.02})},
			dets:        []Detection{detection("EXPOSED", 0.4)},
			wantVerdict: VerdictExplicit,
			wantNSFW:    0.4,
			wantLabels:  Labels{Drawings: 0.05, Hentai: 0.07, Neutral: 0.11, Porn: 0.01, Sexy: 0.02},
			wantDets:    1,
			wantLegacy:  true,
		},
		{
			name:        "binary photo model maps normal and nsfw",
			outputs:     []ModelOutput{classifierOutput("photo", RolePhoto, map[string]float32{"normal": 0.6, "nsfw": 0.4})},
			wantVerdict: VerdictSFW,
			wantNSFW:    0.4,
			wantLabels:  Labels{Neutral: 0.6, Porn: 0.4},
			wantPhoto:   true,
			wantLegacy:  true,
		},
		{
			name:        "photo sfw and porn fallback keys",
			outputs:     []ModelOutput{classifierOutput("photo", RolePhoto, map[string]float32{"sfw": 0.97, "nsfw": 0.02, "porn": 0.01})},
			wantVerdict: VerdictSFW,
			wantNSFW:    0.02,
			wantLabels:  Labels{Neutral: 0.97, Porn: 0.02},
			wantPhoto:   true,
			wantLegacy:  false,
		},
		{
			name:        "detector-only profile",
			profile:     "detector-only",
			outputs:     nil,
			dets:        []Detection{detection("COVERED", 0.4)},
			wantVerdict: VerdictSuggestive,
			wantNSFW:    0,
			wantLabels:  Labels{},
			wantDets:    1,
			wantLegacy:  false,
		},
		{
			name:        "no outputs and no detections",
			outputs:     nil,
			dets:        nil,
			wantVerdict: VerdictSFW,
			wantNSFW:    0,
			wantLabels:  Labels{},
			wantDets:    0,
			wantLegacy:  false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Combine(c.profile, c.outputs, c.dets, combineRules)
			if got.Verdict != c.wantVerdict {
				t.Errorf("Verdict = %q, want %q", got.Verdict, c.wantVerdict)
			}
			if !closeEnough(got.NSFW, c.wantNSFW) {
				t.Errorf("NSFW = %v, want %v", got.NSFW, c.wantNSFW)
			}
			if !labelsCloseTo(got.Labels, c.wantLabels) {
				t.Errorf("Labels = %+v, want %+v", got.Labels, c.wantLabels)
			}
			if (got.Photo != nil) != c.wantPhoto {
				t.Errorf("Photo = %+v, want present = %v", got.Photo, c.wantPhoto)
			}
			if (got.Anime != nil) != c.wantAnime {
				t.Errorf("Anime = %+v, want present = %v", got.Anime, c.wantAnime)
			}
			if got.Detections == nil {
				t.Error("Detections = nil, want non-nil")
			} else if len(got.Detections) != c.wantDets {
				t.Errorf("len(Detections) = %d, want %d", len(got.Detections), c.wantDets)
			}
			if got.LegacyNSFW() != c.wantLegacy {
				t.Errorf("LegacyNSFW() = %v, want %v", got.LegacyNSFW(), c.wantLegacy)
			}
		})
	}
}

func TestCombineThresholdBoundaries(t *testing.T) {
	safePhoto := classifierOutput("photo", RolePhoto, map[string]float32{"neutral": 0.99})

	cases := []struct {
		name    string
		outputs []ModelOutput
		dets    []Detection
		want    string
	}{
		{
			name:    "porn exactly at explicit threshold",
			outputs: []ModelOutput{classifierOutput("photo", RolePhoto, map[string]float32{"high": 0.5})},
			want:    VerdictExplicit,
		},
		{
			name:    "porn just below explicit threshold",
			outputs: []ModelOutput{classifierOutput("photo", RolePhoto, map[string]float32{"high": 0.499})},
			want:    VerdictSFW,
		},
		{
			name:    "sexy exactly at suggestive threshold",
			outputs: []ModelOutput{classifierOutput("photo", RolePhoto, map[string]float32{"medium": 0.5})},
			want:    VerdictSuggestive,
		},
		{
			name:    "hentai exactly at explicit threshold",
			outputs: []ModelOutput{classifierOutput("anime", RoleAnime, map[string]float32{"safe": 0.99, "r18": 0.5})},
			want:    VerdictExplicit,
		},
		{
			name:    "r15 exactly at suggestive threshold",
			outputs: []ModelOutput{classifierOutput("anime", RoleAnime, map[string]float32{"safe": 0.99, "r15": 0.5})},
			want:    VerdictSuggestive,
		},
		{
			name:    "sexy just below suggestive threshold",
			outputs: []ModelOutput{classifierOutput("photo", RolePhoto, map[string]float32{"medium": 0.499})},
			want:    VerdictSFW,
		},
		{
			name:    "detection explicit exactly at threshold",
			outputs: []ModelOutput{safePhoto},
			dets:    []Detection{detection("EXPOSED", 0.3)},
			want:    VerdictExplicit,
		},
		{
			name:    "detection explicit just below threshold",
			outputs: []ModelOutput{safePhoto},
			dets:    []Detection{detection("EXPOSED", 0.299)},
			want:    VerdictSFW,
		},
		{
			name:    "detection suggestive exactly at threshold",
			outputs: []ModelOutput{safePhoto},
			dets:    []Detection{detection("COVERED", 0.35)},
			want:    VerdictSuggestive,
		},
		{
			name:    "detection suggestive just below threshold",
			outputs: []ModelOutput{safePhoto},
			dets:    []Detection{detection("COVERED", 0.349)},
			want:    VerdictSFW,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Combine("balanced", c.outputs, c.dets, combineRules); got.Verdict != c.want {
				t.Errorf("Verdict = %q, want %q", got.Verdict, c.want)
			}
		})
	}
}

func TestCombineOutputOrderIndependent(t *testing.T) {
	photo := classifierOutput("photo", RolePhoto, map[string]float32{"high": 0.8, "neutral": 0.1})
	anime := classifierOutput("anime", RoleAnime, map[string]float32{"safe": 0.2, "r18": 0.7})
	want := Labels{Drawings: 0.2, Hentai: 0.7, Neutral: 0.1, Porn: 0.8}

	results := []Analysis{
		Combine("balanced", []ModelOutput{photo, anime}, nil, combineRules),
		Combine("balanced", []ModelOutput{anime, photo}, nil, combineRules),
	}
	for i, got := range results {
		if got.Verdict != VerdictExplicit {
			t.Errorf("order %d: Verdict = %q, want %q", i, got.Verdict, VerdictExplicit)
		}
		if !labelsCloseTo(got.Labels, want) {
			t.Errorf("order %d: Labels = %+v, want %+v", i, got.Labels, want)
		}
		if got.Photo == nil || got.Anime == nil {
			t.Errorf("order %d: Photo/Anime = %v/%v, want both present", i, got.Photo, got.Anime)
		}
	}
}

func TestCombineDuplicateRoleLastWins(t *testing.T) {
	low := classifierOutput("photo-low", RolePhoto, map[string]float32{"high": 0.1})
	high := classifierOutput("photo-high", RolePhoto, map[string]float32{"high": 0.9})

	got := Combine("balanced", []ModelOutput{low, high}, nil, combineRules)
	if got.Verdict != VerdictExplicit || !closeEnough(got.Labels.Porn, 0.9) {
		t.Errorf("Combine() = %+v, want last photo output to win", got)
	}
	if got.Photo == nil || got.Photo.Model != "photo-high" {
		t.Errorf("Photo = %+v, want model photo-high", got.Photo)
	}
}

func TestCombineCopiesInputs(t *testing.T) {
	scores := map[string]float32{"high": 0.9}
	dets := []Detection{detection("EXPOSED", 0.4)}
	outputs := []ModelOutput{classifierOutput("photo", RolePhoto, scores)}

	got := Combine("balanced", outputs, dets, combineRules)
	scores["high"] = 0
	dets[0].Score = 0

	if !closeEnough(got.Labels.Porn, 0.9) {
		t.Errorf("Labels.Porn = %v, want 0.9 after input mutation", got.Labels.Porn)
	}
	if got.Photo == nil || !closeEnough(got.Photo.Scores["high"], 0.9) {
		t.Errorf("Photo = %+v, want copied scores with high 0.9", got.Photo)
	}
	if len(got.Detections) != 1 || !closeEnough(got.Detections[0].Score, 0.4) {
		t.Errorf("Detections = %+v, want copied score 0.4", got.Detections)
	}
}

func TestCombineNonFiniteScores(t *testing.T) {
	nan := float32(math.NaN())
	inf := float32(math.Inf(1))
	outputs := []ModelOutput{
		classifierOutput("photo", RolePhoto, map[string]float32{"high": nan, "medium": inf, "low": nan, "neutral": inf}),
		classifierOutput("anime", RoleAnime, map[string]float32{"safe": nan, "r15": inf, "r18": nan}),
	}
	dets := []Detection{{Label: "EXPOSED", Score: nan, Box: Box{X: nan, Y: inf, W: nan, H: inf}}}

	got := Combine("balanced", outputs, dets, combineRules)
	if got.Verdict != VerdictSFW {
		t.Errorf("Verdict = %q, want %q", got.Verdict, VerdictSFW)
	}
	if got.NSFW != 0 {
		t.Errorf("NSFW = %v, want 0", got.NSFW)
	}
	if !labelsCloseTo(got.Labels, Labels{}) {
		t.Errorf("Labels = %+v, want zero labels", got.Labels)
	}
	for _, score := range []float32{
		got.Labels.Drawings, got.Labels.Hentai, got.Labels.Neutral, got.Labels.Porn, got.Labels.Sexy,
		got.Photo.Scores["high"], got.Photo.Scores["medium"], got.Photo.Scores["low"], got.Photo.Scores["neutral"],
		got.Anime.Scores["safe"], got.Anime.Scores["r15"], got.Anime.Scores["r18"],
		got.Detections[0].Score,
		got.Detections[0].Box.X, got.Detections[0].Box.Y, got.Detections[0].Box.W, got.Detections[0].Box.H,
	} {
		if !isFinite(score) {
			t.Errorf("non-finite value %v survived Combine", score)
		}
	}
	if _, err := json.Marshal(got); err != nil {
		t.Fatalf("json.Marshal(Analysis with non-finite input) error = %v, want nil", err)
	}
}

func TestLegacyNSFW(t *testing.T) {
	cases := []struct {
		name     string
		analysis Analysis
		want     bool
	}{
		{
			name:     "neutral high with porn over the legacy flag",
			analysis: Analysis{Labels: Labels{Neutral: 0.8, Drawings: 0.1, Porn: 0.15}},
			want:     true,
		},
		{
			name:     "neutral and drawings high with low porn and sexy",
			analysis: Analysis{Labels: Labels{Neutral: 0.9, Drawings: 0.8}},
			want:     false,
		},
		{
			name: "explicit detection with all-safe labels",
			analysis: Combine("balanced",
				[]ModelOutput{classifierOutput("photo", RolePhoto, map[string]float32{"neutral": 0.99})},
				[]Detection{detection("EXPOSED", 0.4)}, combineRules),
			want: true,
		},
		{
			name: "suggestive detection with all-safe labels",
			analysis: Combine("balanced",
				[]ModelOutput{classifierOutput("photo", RolePhoto, map[string]float32{"neutral": 0.99})},
				[]Detection{detection("COVERED", 0.4)}, combineRules),
			want: false,
		},
		{
			name: "mixed model hentai behind the neutral guard",
			analysis: Combine("balanced",
				[]ModelOutput{
					classifierOutput("photo", RolePhoto, map[string]float32{"neutral": 0.9}),
					classifierOutput("anime", RoleAnime, map[string]float32{"safe": 0.05, "r18": 0.95}),
				}, nil, combineRules),
			want: true,
		},
		{
			name: "explicit detection below the verdict threshold",
			analysis: Combine("balanced",
				[]ModelOutput{classifierOutput("photo", RolePhoto, map[string]float32{"neutral": 0.99})},
				[]Detection{detection("EXPOSED", 0.26)}, combineRules),
			want: false,
		},
		{
			name: "explicit detection at the verdict threshold",
			analysis: Combine("balanced",
				[]ModelOutput{classifierOutput("photo", RolePhoto, map[string]float32{"neutral": 0.99})},
				[]Detection{detection("EXPOSED", ThresholdDetectionExplicit)}, combineRules),
			want: true,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.analysis.LegacyNSFW(); got != c.want {
				t.Errorf("LegacyNSFW() = %v, want %v", got, c.want)
			}
		})
	}

	t.Run("value receiver works on a Combine result", func(t *testing.T) {
		if !Combine("balanced", nil, []Detection{detection("EXPOSED", 0.4)}, combineRules).LegacyNSFW() {
			t.Error("Combine(...).LegacyNSFW() = false, want true")
		}
	})
}

func TestAnalysisJSONShape(t *testing.T) {
	got := Combine("balanced",
		[]ModelOutput{classifierOutput("photo", RolePhoto, map[string]float32{"high": 0.9, "neutral": 0.05})},
		nil, combineRules)
	data, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("json.Marshal(Analysis) error = %v, want nil", err)
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatalf("json.Unmarshal(Analysis) error = %v, want nil", err)
	}
	for _, key := range []string{"verdict", "nsfw", "labels", "photo", "detections", "elapsedMs"} {
		if _, ok := fields[key]; !ok {
			t.Errorf("marshaled Analysis missing key %q", key)
		}
	}
	for _, key := range []string{"anime", "models"} {
		if _, ok := fields[key]; ok {
			t.Errorf("marshaled Analysis has key %q, want omitted", key)
		}
	}

	var labels map[string]json.RawMessage
	if err := json.Unmarshal(fields["labels"], &labels); err != nil {
		t.Fatalf("json.Unmarshal(labels) error = %v, want nil", err)
	}
	wantLabelKeys := []string{"drawings", "hentai", "neutral", "porn", "sexy"}
	if len(labels) != len(wantLabelKeys) {
		t.Errorf("marshaled labels has %d keys, want exactly %d: %v", len(labels), len(wantLabelKeys), labels)
	}
	for _, key := range wantLabelKeys {
		if _, ok := labels[key]; !ok {
			t.Errorf("marshaled labels missing key %q", key)
		}
	}

	var photo map[string]json.RawMessage
	if err := json.Unmarshal(fields["photo"], &photo); err != nil {
		t.Fatalf("json.Unmarshal(photo) error = %v, want nil", err)
	}
	if string(photo["model"]) != `"photo"` {
		t.Errorf("photo.model = %s, want %q", photo["model"], "photo")
	}
	if _, ok := photo["scores"]; !ok {
		t.Error("marshaled photo missing key \"scores\"")
	}

	empty, err := json.Marshal(Combine("balanced", nil, nil, DetectionRules{}))
	if err != nil {
		t.Fatalf("json.Marshal(empty Analysis) error = %v, want nil", err)
	}
	if !strings.Contains(string(empty), `"detections":[]`) {
		t.Errorf("marshaled empty Analysis = %s, want detections []", empty)
	}
}
