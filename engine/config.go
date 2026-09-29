package engine

import "log/slog"

const (
	defaultProfile   = "balanced"
	defaultModelsDir = "./assets/models"
	defaultPoolSize  = 2
)

// Options configures an Engine.
type Options struct {
	// Profile names the manifest profile to load.
	Profile string
	// Provider selects cpu, cuda, or migraphx (ROCm).
	Provider string
	// DeviceID selects the GPU device index.
	DeviceID int
	// ModelsDir is the directory holding the model files.
	ModelsDir string
	// PoolSize is the number of ONNX sessions created per model.
	PoolSize int
	// AllowDegraded lets a required model that fails to load be skipped with a warning instead of failing NewEngine.
	AllowDegraded bool
	// VerifySHA controls SHA-256 verification of model files.
	VerifySHA *bool
	// Limits bounds accepted image sizes.
	Limits Limits
	// Log receives engine diagnostics.
	Log *slog.Logger
}

func (o Options) withDefaults() Options {
	if o.Provider == "" {
		o.Provider = "cpu"
	}
	if o.Profile == "" {
		o.Profile = defaultProfile
	}
	if o.ModelsDir == "" {
		o.ModelsDir = defaultModelsDir
	}
	if o.PoolSize < 1 {
		o.PoolSize = defaultPoolSize
	}
	if o.VerifySHA == nil {
		verify := true
		o.VerifySHA = &verify
	}
	if o.Limits == (Limits{}) {
		o.Limits = defaultLimits
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	return o
}

func (o Options) verifySHA() bool {
	return o.VerifySHA == nil || *o.VerifySHA
}
