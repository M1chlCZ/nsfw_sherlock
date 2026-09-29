package engine

// ModelOutput is one model's scores tagged with its manifest role.
type ModelOutput struct {
	ID     string
	Kind   string
	Role   string
	Scores map[string]float32
}

// ClassifierOutput is one classifier's raw scores as included in an Analysis.
type ClassifierOutput struct {
	Model  string             `json:"model"`
	Scores map[string]float32 `json:"scores"`
}

type ModelInfo struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	SHA256 string `json:"sha256"`
}

// DetectionRules names the detector classes that force an explicit or suggestive verdict.
type DetectionRules struct {
	Explicit   []string
	Suggestive []string
}

// Verdicts returned in Analysis.Verdict.
const (
	VerdictSFW        = "sfw"
	VerdictSuggestive = "suggestive"
	VerdictExplicit   = "explicit"
)

const (
	// ThresholdPornExplicit is the Porn label score at or above which the image is explicit.
	ThresholdPornExplicit = 0.5
	// ThresholdHentaiExplicit is the Hentai label score at or above which the image is explicit.
	ThresholdHentaiExplicit = 0.5
	// ThresholdSexySuggestive is the Sexy label score at or above which the image is suggestive.
	ThresholdSexySuggestive = 0.5
	// ThresholdAnimeR15Suggestive is the anime R15 score at or above which the image is suggestive.
	ThresholdAnimeR15Suggestive = 0.5
	// ThresholdDetectionExplicit is the detection score at or above which a detection of an explicit class makes the image explicit.
	ThresholdDetectionExplicit = 0.3
	// ThresholdDetectionSuggestive is the detection score at or above which a detection of a suggestive class makes the image suggestive.
	ThresholdDetectionSuggestive = 0.35
	// SexyLowWeight scales a photo classifier's low-severity score when deriving Labels.Sexy.
	SexyLowWeight = 0.5
)

// Analysis is the combined result for one image.
type Analysis struct {
	Verdict    string            `json:"verdict"`
	NSFW       float32           `json:"nsfw"`
	Labels     Labels            `json:"labels"`
	Photo      *ClassifierOutput `json:"photo,omitempty"`
	Anime      *ClassifierOutput `json:"anime,omitempty"`
	Detections []Detection       `json:"detections"`
	Models     []ModelInfo       `json:"models,omitempty"`
	ElapsedMs  int64             `json:"elapsedMs"`

	explicitDetection bool
}

// Combine reduces per-model scores and detector boxes to the legacy five-label set, a verdict, and the scalar /pic/analyze fields.
func Combine(profile string, outputs []ModelOutput, dets []Detection, rules DetectionRules) Analysis {
	var labels Labels
	var photo, anime *ClassifierOutput
	var animeR15 float32

	if profile == "compat" {
		for i := range outputs {
			out := &outputs[i]
			if out.Role != RoleCompat {
				continue
			}
			scores := copyScores(out.Scores)
			labels = Labels{
				Drawings: scores["drawings"],
				Hentai:   scores["hentai"],
				Neutral:  scores["neutral"],
				Porn:     scores["porn"],
				Sexy:     scores["sexy"],
			}
		}
	} else {
		for i := range outputs {
			out := &outputs[i]
			switch out.Role {
			case RolePhoto:
				scores := copyScores(out.Scores)
				labels.Neutral = firstScore(scores, "neutral", "sfw", "normal")
				labels.Porn = firstScore(scores, "high", "nsfw", "porn")
				labels.Sexy = sexyFromPhoto(scores)
				photo = &ClassifierOutput{Model: out.ID, Scores: scores}
			case RoleAnime:
				scores := copyScores(out.Scores)
				labels.Drawings = scores["safe"]
				labels.Hentai = scores["r18"]
				animeR15 = scores["r15"]
				anime = &ClassifierOutput{Model: out.ID, Scores: scores}
			}
		}
	}

	explicit := stringSet(rules.Explicit)
	suggestive := stringSet(rules.Suggestive)
	detections := make([]Detection, len(dets))
	var detExplicit, detSuggestive float32
	for i := range dets {
		d := dets[i]
		d.Score = finiteOrZero(d.Score)
		d.Box = Box{
			X: finiteOrZero(d.Box.X),
			Y: finiteOrZero(d.Box.Y),
			W: finiteOrZero(d.Box.W),
			H: finiteOrZero(d.Box.H),
		}
		detections[i] = d
		if _, ok := explicit[d.Label]; ok && d.Score > detExplicit {
			detExplicit = d.Score
		}
		if _, ok := suggestive[d.Label]; ok && d.Score > detSuggestive {
			detSuggestive = d.Score
		}
	}

	verdict := VerdictSFW
	switch {
	case labels.Porn >= ThresholdPornExplicit ||
		labels.Hentai >= ThresholdHentaiExplicit ||
		detExplicit >= ThresholdDetectionExplicit:
		verdict = VerdictExplicit
	case labels.Sexy >= ThresholdSexySuggestive ||
		animeR15 >= ThresholdAnimeR15Suggestive ||
		detSuggestive >= ThresholdDetectionSuggestive:
		verdict = VerdictSuggestive
	}

	nsfw := max(labels.Porn, labels.Hentai, detExplicit)

	return Analysis{
		Verdict:           verdict,
		NSFW:              nsfw,
		Labels:            labels,
		Photo:             photo,
		Anime:             anime,
		Detections:        detections,
		explicitDetection: detExplicit >= ThresholdDetectionExplicit,
	}
}

// LegacyNSFW reproduces the legacy /pic/check boolean: the original Labels.NSFW thresholds, an explicit hentai label, or an explicit-class detection at the explicit threshold.
func (a Analysis) LegacyNSFW() bool {
	return a.Labels.NSFW(LegacyThresholdSafe) ||
		a.Labels.Hentai >= ThresholdHentaiExplicit ||
		a.explicitDetection
}

func firstScore(scores map[string]float32, keys ...string) float32 {
	for _, key := range keys {
		if score, ok := scores[key]; ok {
			return score
		}
	}
	return 0
}

func sexyFromPhoto(scores map[string]float32) float32 {
	medium, hasMedium := scores["medium"]
	low, hasLow := scores["low"]
	var sexy float32
	if hasMedium || hasLow {
		sexy = max(medium, SexyLowWeight*low)
	} else {
		sexy = scores["sexy"]
	}
	return min(sexy, 1)
}

func copyScores(scores map[string]float32) map[string]float32 {
	out := make(map[string]float32, len(scores))
	for label, score := range scores {
		out[label] = finiteOrZero(score)
	}
	return out
}

func finiteOrZero(f float32) float32 {
	if !isFinite(f) {
		return 0
	}
	return f
}

func stringSet(labels []string) map[string]struct{} {
	out := make(map[string]struct{}, len(labels))
	for _, label := range labels {
		out[label] = struct{}{}
	}
	return out
}
