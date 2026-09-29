package engine

const (
	LegacyThresholdSafe   = 0.75
	LegacyThresholdMedium = 0.85
	LegacyThresholdHigh   = 0.98
)

// Labels holds the legacy five-label classifier output.
type Labels struct {
	Drawings float32 `json:"drawings"`
	Hentai   float32 `json:"hentai"`
	Neutral  float32 `json:"neutral"`
	Porn     float32 `json:"porn"`
	Sexy     float32 `json:"sexy"`
}

// IsNSFW returns false if the image is probably safe for work.
func (l *Labels) IsNSFW() bool {
	return l.NSFW(LegacyThresholdSafe)
}

// GetLabels returns the label values produced by the model.
func (l *Labels) GetLabels() Labels {
	return *l
}

func (l *Labels) NSFW(threshold float32) bool {
	if l.Neutral > 0.75 || l.Drawings > 0.75 {
		if l.Porn < 0.1 && l.Sexy < 0.1 {
			return false
		} else {
			return true
		}
	}

	if l.Porn > 0.1 {
		return true
	}
	if l.Sexy > 0.1 {
		return true
	}
	if l.Hentai > 0.2 {
		return true
	}

	return false
}
