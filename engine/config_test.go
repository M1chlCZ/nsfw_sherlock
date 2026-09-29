package engine

import (
	"log/slog"
	"testing"
)

func TestOptionsDefaults(t *testing.T) {
	opts := Options{}.withDefaults()

	if opts.Profile != "balanced" {
		t.Errorf("Profile = %q, want %q", opts.Profile, "balanced")
	}
	if opts.ModelsDir != "./assets/models" {
		t.Errorf("ModelsDir = %q, want %q", opts.ModelsDir, "./assets/models")
	}
	if opts.PoolSize != 2 {
		t.Errorf("PoolSize = %d, want 2", opts.PoolSize)
	}
	if opts.AllowDegraded {
		t.Error("AllowDegraded = true, want false")
	}
	if opts.VerifySHA == nil {
		t.Fatal("VerifySHA = nil, want pointer to true")
	}
	if !*opts.VerifySHA {
		t.Error("*VerifySHA = false, want true")
	}
	if opts.Limits != defaultLimits {
		t.Errorf("Limits = %+v, want %+v", opts.Limits, defaultLimits)
	}
	if opts.Log == nil {
		t.Error("Log = nil, want slog.Default()")
	}
}

func TestOptionsInvalidValuesFallBackToDefaults(t *testing.T) {
	cases := []struct {
		name string
		opts Options
	}{
		{"empty", Options{}},
		{"empty profile", Options{Profile: ""}},
		{"empty models dir", Options{ModelsDir: ""}},
		{"zero pool size", Options{PoolSize: 0}},
		{"negative pool size", Options{PoolSize: -3}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := c.opts.withDefaults()
			if got.Profile != "balanced" {
				t.Errorf("Profile = %q, want %q", got.Profile, "balanced")
			}
			if got.ModelsDir != "./assets/models" {
				t.Errorf("ModelsDir = %q, want %q", got.ModelsDir, "./assets/models")
			}
			if got.PoolSize != 2 {
				t.Errorf("PoolSize = %d, want 2", got.PoolSize)
			}
		})
	}
}

func TestOptionsOverrides(t *testing.T) {
	no := false
	logger := slog.New(slog.DiscardHandler)
	limits := Limits{MaxBytes: 1 << 10, MaxPixels: 4096}

	opts := Options{
		Profile:       "compat",
		ModelsDir:     "/srv/models",
		PoolSize:      4,
		AllowDegraded: true,
		VerifySHA:     &no,
		Limits:        limits,
		Log:           logger,
	}.withDefaults()

	if opts.Profile != "compat" {
		t.Errorf("Profile = %q, want %q", opts.Profile, "compat")
	}
	if opts.ModelsDir != "/srv/models" {
		t.Errorf("ModelsDir = %q, want %q", opts.ModelsDir, "/srv/models")
	}
	if opts.PoolSize != 4 {
		t.Errorf("PoolSize = %d, want 4", opts.PoolSize)
	}
	if !opts.AllowDegraded {
		t.Error("AllowDegraded = false, want true")
	}
	if opts.VerifySHA == nil || *opts.VerifySHA {
		t.Errorf("VerifySHA = %v, want pointer to false", opts.VerifySHA)
	}
	if opts.Limits != limits {
		t.Errorf("Limits = %+v, want %+v", opts.Limits, limits)
	}
	if opts.Log != logger {
		t.Error("Log was replaced, want the provided logger")
	}
}
