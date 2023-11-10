package nsfw

const (
	ThresholdSafe   = 0.75
	ThresholdMedium = 0.85
	ThresholdHigh   = 0.98
)

type Labels struct {
	Drawings float32
	Hentai   float32
	Neutral  float32
	Porn     float32
	Sexy     float32
}

// IsNSFW returns false if the image is probably safe for work.
func (l *Labels) IsNSFW() bool {
	return l.NSFW(ThresholdSafe)
}

// NSFW returns true if the image is may not be safe for work.
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
