package config

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
)

const (
	envAppEnv          = "APP_ENV"
	envPort            = "PORT"
	envModelsDir       = "MODELS_DIR"
	envManifest        = "MANIFEST"
	envORTLib          = "ORT_LIB"
	envProfile         = "NSFW_PROFILE"
	envAllowDegraded   = "NSFW_ALLOW_DEGRADED"
	envOCR             = "OCR_ENABLED"
	envBadWordsFile    = "BAD_WORDS_FILE"
	envMaxImageBytes   = "MAX_IMAGE_BYTES"
	envMaxImagePixels  = "MAX_IMAGE_PIXELS"
	envSessionPoolSize = "SESSION_POOL_SIZE"
	envBatchMaxImages  = "BATCH_MAX_IMAGES"
	envCacheEntries    = "CACHE_ENTRIES"
	envFFmpegPath      = "FFMPEG_PATH"
	envVideoSampleFPS  = "VIDEO_SAMPLE_FPS"
	envVideoMaxFrames  = "VIDEO_MAX_FRAMES"
	envVideoMaxBytes   = "VIDEO_MAX_BYTES"
	envAPIKeys         = "API_KEYS"
	envRateLimitRPM    = "RATE_LIMIT_RPM"
	envLogLevel        = "LOG_LEVEL"
	envLogFormat       = "LOG_FORMAT"
)

const (
	defaultAppEnv          = "web"
	legacyAppEnvHTTP       = "http"
	defaultPort            = 4000
	defaultModelsDir       = "./assets/models"
	defaultManifest        = "models/manifest.json"
	defaultProfile         = "balanced"
	defaultOCR             = true
	defaultMaxImageBytes   = 20 << 20
	defaultMaxImagePixels  = 50_000_000
	defaultSessionPoolSize = 2
	defaultBatchMaxImages  = 4
	maxBatchImages         = 64
	defaultCacheEntries    = 0
	maxCacheEntries        = 100_000
	defaultFFmpegPath      = "ffmpeg"
	defaultVideoSampleFPS  = 1.0
	defaultVideoMaxFrames  = 24
	defaultVideoMaxBytes   = 64 << 20
	maxVideoSampleFPS      = 30.0
	maxVideoMaxFrames      = 240
	defaultRateLimitRPM    = 0
	maxRateLimitRPM        = 1_000_000
	defaultLogLevel        = "info"
	defaultLogFormat       = "text"
)

// Config is the process configuration assembled from environment variables.
type Config struct {
	AppEnv          string
	Port            int
	ModelsDir       string
	Manifest        string
	ORTProvider     string
	ORTDeviceID     int
	ORTLib          string
	Profile         string
	AllowDegraded   bool
	OCR             bool
	BadWordsFile    string
	MaxImageBytes   int64
	MaxImagePixels  int64
	SessionPoolSize int
	BatchMaxImages  int
	CacheEntries    int
	APIKeys         []string
	RateLimitRPM    int
	FFmpegPath      string
	VideoSampleFPS  float64
	VideoMaxFrames  int
	VideoMaxBytes   int64
	LogLevel        string
	LogFormat       string
}

// Load reads configuration from the environment with defaults applied and values validated.
func Load(getenv func(string) string) (Config, error) {
	cfg := Config{
		AppEnv:          defaultAppEnv,
		ORTProvider:     "cpu",
		Port:            defaultPort,
		ModelsDir:       defaultModelsDir,
		Manifest:        defaultManifest,
		Profile:         defaultProfile,
		AllowDegraded:   false,
		OCR:             defaultOCR,
		BadWordsFile:    "",
		MaxImageBytes:   defaultMaxImageBytes,
		MaxImagePixels:  defaultMaxImagePixels,
		SessionPoolSize: defaultSessionPoolSize,
		BatchMaxImages:  defaultBatchMaxImages,
		CacheEntries:    defaultCacheEntries,
		RateLimitRPM:    defaultRateLimitRPM,
		FFmpegPath:      defaultFFmpegPath,
		VideoSampleFPS:  defaultVideoSampleFPS,
		VideoMaxFrames:  defaultVideoMaxFrames,
		VideoMaxBytes:   defaultVideoMaxBytes,
		LogLevel:        defaultLogLevel,
		LogFormat:       defaultLogFormat,
	}

	var err error
	if cfg.AppEnv, err = appEnvFromEnv(getenv, cfg.AppEnv); err != nil {
		return Config{}, err
	}
	if cfg.Port, err = intFromEnv(getenv, envPort, cfg.Port); err != nil {
		return Config{}, err
	}
	if cfg.Port < 1 || cfg.Port > 65535 {
		return Config{}, fmt.Errorf("config: %s: value %d out of range 1..65535", envPort, cfg.Port)
	}

	cfg.ModelsDir = stringFromEnv(getenv, envModelsDir, cfg.ModelsDir)

	rawManifest := getenv(envManifest)
	if manifest := strings.TrimSpace(rawManifest); manifest != "" {
		cfg.Manifest = manifest
	} else if rawManifest != "" {
		return Config{}, fmt.Errorf("config: %s: value %q must not be blank", envManifest, rawManifest)
	}

	cfg.ORTLib = stringFromEnv(getenv, envORTLib, cfg.ORTLib)
	if err := cfg.loadGPU(getenv); err != nil {
		return Config{}, err
	}

	rawProfile := getenv(envProfile)
	if profile := strings.TrimSpace(rawProfile); profile != "" {
		cfg.Profile = profile
	} else if rawProfile != "" {
		return Config{}, fmt.Errorf("config: %s: value %q must not be blank", envProfile, rawProfile)
	}

	if cfg.AllowDegraded, err = boolFromEnv(getenv, envAllowDegraded, cfg.AllowDegraded); err != nil {
		return Config{}, err
	}
	if cfg.OCR, err = boolFromEnv(getenv, envOCR, cfg.OCR); err != nil {
		return Config{}, err
	}
	cfg.BadWordsFile = stringFromEnv(getenv, envBadWordsFile, cfg.BadWordsFile)

	if err := cfg.loadLimits(getenv); err != nil {
		return Config{}, err
	}
	if cfg.LogLevel, err = enumFromEnv(
		getenv,
		envLogLevel,
		cfg.LogLevel,
		"debug",
		"info",
		"warn",
		"error",
	); err != nil {
		return Config{}, err
	}
	if cfg.LogFormat, err = enumFromEnv(getenv, envLogFormat, cfg.LogFormat, "text", "json"); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (cfg *Config) loadLimits(getenv func(string) string) error {
	var err error
	if cfg.MaxImageBytes, err = int64FromEnv(getenv, envMaxImageBytes, cfg.MaxImageBytes); err != nil {
		return err
	}
	if cfg.MaxImageBytes <= 0 {
		return fmt.Errorf("config: %s: value %d must be greater than 0", envMaxImageBytes, cfg.MaxImageBytes)
	}
	if cfg.MaxImagePixels, err = int64FromEnv(getenv, envMaxImagePixels, cfg.MaxImagePixels); err != nil {
		return err
	}
	if cfg.MaxImagePixels <= 0 {
		return fmt.Errorf(
			"config: %s: value %d must be greater than 0",
			envMaxImagePixels,
			cfg.MaxImagePixels,
		)
	}
	if cfg.SessionPoolSize, err = intFromEnv(getenv, envSessionPoolSize, cfg.SessionPoolSize); err != nil {
		return err
	}
	if cfg.SessionPoolSize < 1 {
		return fmt.Errorf("config: %s: value %d must be at least 1", envSessionPoolSize, cfg.SessionPoolSize)
	}
	if cfg.BatchMaxImages, err = intFromEnv(getenv, envBatchMaxImages, cfg.BatchMaxImages); err != nil {
		return err
	}
	if cfg.BatchMaxImages < 1 || cfg.BatchMaxImages > maxBatchImages {
		return fmt.Errorf(
			"config: %s: value %d must be in 1..%d",
			envBatchMaxImages,
			cfg.BatchMaxImages,
			maxBatchImages,
		)
	}
	if cfg.CacheEntries, err = intFromEnv(getenv, envCacheEntries, cfg.CacheEntries); err != nil {
		return err
	}
	if cfg.CacheEntries < 0 || cfg.CacheEntries > maxCacheEntries {
		return fmt.Errorf(
			"config: %s: value %d must be in 0..%d",
			envCacheEntries,
			cfg.CacheEntries,
			maxCacheEntries,
		)
	}
	cfg.APIKeys = parseAPIKeys(getenv(envAPIKeys))
	if cfg.RateLimitRPM, err = intFromEnv(getenv, envRateLimitRPM, cfg.RateLimitRPM); err != nil {
		return err
	}
	if cfg.RateLimitRPM < 0 || cfg.RateLimitRPM > maxRateLimitRPM {
		return fmt.Errorf(
			"config: %s: value %d must be in 0..%d",
			envRateLimitRPM,
			cfg.RateLimitRPM,
			maxRateLimitRPM,
		)
	}
	cfg.FFmpegPath = stringFromEnv(getenv, envFFmpegPath, cfg.FFmpegPath)
	if cfg.VideoSampleFPS, err = floatFromEnv(getenv, envVideoSampleFPS, cfg.VideoSampleFPS); err != nil {
		return err
	}
	if cfg.VideoSampleFPS <= 0 || cfg.VideoSampleFPS > maxVideoSampleFPS {
		return fmt.Errorf(
			"config: %s: value %g must be in 0..%g",
			envVideoSampleFPS,
			cfg.VideoSampleFPS,
			maxVideoSampleFPS,
		)
	}
	if cfg.VideoMaxFrames, err = intFromEnv(getenv, envVideoMaxFrames, cfg.VideoMaxFrames); err != nil {
		return err
	}
	if cfg.VideoMaxFrames < 1 || cfg.VideoMaxFrames > maxVideoMaxFrames {
		return fmt.Errorf(
			"config: %s: value %d must be in 1..%d",
			envVideoMaxFrames,
			cfg.VideoMaxFrames,
			maxVideoMaxFrames,
		)
	}
	if cfg.VideoMaxBytes, err = int64FromEnv(getenv, envVideoMaxBytes, cfg.VideoMaxBytes); err != nil {
		return err
	}
	if cfg.VideoMaxBytes <= 0 {
		return fmt.Errorf(
			"config: %s: value %d must be greater than 0",
			envVideoMaxBytes,
			cfg.VideoMaxBytes,
		)
	}
	return nil
}

func parseAPIKeys(raw string) []string {
	var keys []string
	for part := range strings.SplitSeq(raw, ",") {
		if key := strings.TrimSpace(part); key != "" {
			keys = append(keys, key)
		}
	}
	return keys
}

func appEnvFromEnv(getenv func(string) string, def string) (string, error) {
	raw := getenv(envAppEnv)
	value := strings.TrimSpace(raw)
	if value == "" {
		return def, nil
	}
	if value == legacyAppEnvHTTP {
		value = defaultAppEnv
	}
	if value != "web" && value != "grpc" {
		return "", fmt.Errorf("config: %s: invalid value %q (want one of web, grpc)", envAppEnv, raw)
	}
	return value, nil
}

func intFromEnv(getenv func(string) string, key string, def int) (int, error) {
	raw := getenv(key)
	value := strings.TrimSpace(raw)
	if value == "" {
		return def, nil
	}
	n, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("config: %s: invalid integer %q", key, raw)
	}
	return n, nil
}

func floatFromEnv(getenv func(string) string, key string, def float64) (float64, error) {
	raw := getenv(key)
	value := strings.TrimSpace(raw)
	if value == "" {
		return def, nil
	}
	n, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, fmt.Errorf("config: %s: invalid number %q", key, raw)
	}
	return n, nil
}

func int64FromEnv(getenv func(string) string, key string, def int64) (int64, error) {
	raw := getenv(key)
	value := strings.TrimSpace(raw)
	if value == "" {
		return def, nil
	}
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("config: %s: invalid integer %q", key, raw)
	}
	return n, nil
}

func boolFromEnv(getenv func(string) string, key string, def bool) (bool, error) {
	raw := getenv(key)
	value := strings.TrimSpace(raw)
	if value == "" {
		return def, nil
	}
	b, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("config: %s: invalid boolean %q", key, raw)
	}
	return b, nil
}

func enumFromEnv(getenv func(string) string, key, def string, allowed ...string) (string, error) {
	raw := getenv(key)
	value := strings.TrimSpace(raw)
	if value == "" {
		return def, nil
	}
	if slices.Contains(allowed, value) {
		return value, nil
	}
	return "", fmt.Errorf("config: %s: invalid value %q (want one of %s)", key, raw, strings.Join(allowed, ", "))
}

func stringFromEnv(getenv func(string) string, key, def string) string {
	if value := strings.TrimSpace(getenv(key)); value != "" {
		return value
	}
	return def
}

func (cfg *Config) loadGPU(getenv func(string) string) error {
	var err error
	if cfg.ORTProvider, err = enumFromEnv(
		getenv,
		"ORT_PROVIDER",
		cfg.ORTProvider,
		"cpu",
		"cuda",
		"migraphx",
		"rocm",
		"tensorrt",
		"openvino",
	); err != nil {
		return err
	}
	if cfg.ORTProvider == "rocm" {
		cfg.ORTProvider = "migraphx"
	}
	if cfg.ORTDeviceID, err = intFromEnv(getenv, "ORT_DEVICE_ID", cfg.ORTDeviceID); err != nil {
		return err
	}
	if cfg.ORTDeviceID < 0 || cfg.ORTDeviceID > math.MaxInt32 {
		return fmt.Errorf("config: ORT_DEVICE_ID must be in 0..%d", math.MaxInt32)
	}

	return nil
}
