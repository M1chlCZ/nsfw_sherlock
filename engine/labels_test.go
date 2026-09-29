package engine

import "testing"

func TestLegacyNSFWThresholds(t *testing.T) {
	cases := []struct {
		name string
		l    Labels
		want bool
	}{
		{"neutral and drawings high, porn and sexy low", Labels{Neutral: 0.9, Drawings: 0.8}, false},
		{"neutral branch with porn over 0.1", Labels{Neutral: 0.8, Drawings: 0.1, Porn: 0.15}, true},
		{"standalone sexy over 0.1", Labels{Neutral: 0.2, Sexy: 0.11}, true},
		{"standalone hentai over 0.2", Labels{Neutral: 0.2, Hentai: 0.25}, true},
		{"neutral high, porn and sexy below 0.1", Labels{Neutral: 0.95, Porn: 0.05, Sexy: 0.09}, false},
		{"zero value defaults to safe", Labels{}, false},
		{"standalone porn over 0.1", Labels{Neutral: 0.2, Porn: 0.11}, true},
		{"porn boundary 0.1 is not exceeded", Labels{Neutral: 0.2, Porn: 0.1}, false},
		{"neutral boundary 0.75 is not exceeded", Labels{Neutral: 0.75}, false},
		{"neutral branch with porn and sexy low", Labels{Neutral: 0.76}, false},
		{"drawings branch with porn and sexy low", Labels{Drawings: 0.9}, false},
		{"hentai boundary 0.2 is not exceeded", Labels{Neutral: 0.2, Hentai: 0.2}, false},
		{"standalone hentai just over 0.2", Labels{Neutral: 0.2, Hentai: 0.21}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.l.NSFW(LegacyThresholdSafe); got != c.want {
				t.Errorf("NSFW(%+v) = %v, want %v", c.l, got, c.want)
			}
		})
	}
}

func TestLegacyThresholdParamIgnored(t *testing.T) {
	vectors := []Labels{
		{},
		{Neutral: 0.9, Drawings: 0.8},
		{Neutral: 0.8, Drawings: 0.1, Porn: 0.15},
		{Neutral: 0.2, Sexy: 0.11},
		{Neutral: 0.95, Porn: 0.05, Sexy: 0.09},
	}
	thresholds := []float32{0, 0.5, 1.0}
	for i, l := range vectors {
		want := l.NSFW(0)
		for _, threshold := range thresholds {
			if got := l.NSFW(threshold); got != want {
				t.Errorf("vector %d: NSFW(%v) = %v, want %v", i, threshold, got, want)
			}
		}
	}
}

func TestIsNSFWAndGetLabels(t *testing.T) {
	nsfw := Labels{Porn: 0.5}
	if !nsfw.IsNSFW() {
		t.Errorf("Labels{Porn: 0.5}.IsNSFW() = false, want true")
	}
	safe := Labels{}
	if safe.IsNSFW() {
		t.Errorf("Labels{}.IsNSFW() = true, want false")
	}

	l := Labels{Drawings: 0.1, Hentai: 0.2, Neutral: 0.3, Porn: 0.4, Sexy: 0.5}
	if got := l.GetLabels(); got != l {
		t.Errorf("GetLabels() = %+v, want %+v", got, l)
	}
}
