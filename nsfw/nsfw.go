package nsfw

const (
	ThresholdSafe   = 0.75
	ThresholdMedium = 0.85
	ThresholdHigh   = 0.98
)

type Labels struct {
	Drawing float32
	Hentai  float32
	Neutral float32
	Porn    float32
	Sexy    float32
}

// IsNSFW returns false if the image is probably safe for work.
func (l *Labels) IsNSFW() bool {
	return l.NSFW(ThresholdSafe)
}

// NSFW returns true if the image is may not be safe for work.
func (l *Labels) NSFW(threshold float32) bool {
	if l.Neutral > 0.385 {
		return false
	}

	if l.Porn > threshold {
		return true
	} else if l.Sexy > 0.01 {
		return true
	} else if l.Hentai > threshold {
		return true
	} else {
		return false
	}
}
