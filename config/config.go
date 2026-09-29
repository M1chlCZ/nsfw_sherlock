package config

import (
	"fmt"
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
	defaultLogLevel        = "info"
	defaultLogFormat       = "text"
)

// Config is the process configuration assembled from environment variables.
type Config struct {
	AppEnv          string
	Port            int
	ModelsDir       string
	Manifest        string
	ORTLib          string
	Profile         string
	AllowDegraded   bool
	OCR             bool
	BadWordsFile    string
	MaxImageBytes   int64
	MaxImagePixels  int64
	SessionPoolSize int
	LogLevel        string
	LogFormat       string
}

// Load reads configuration from the environment with defaults applied and values validated.
func Load(getenv func(string) string) (Config, error) {
	cfg := Config{
		AppEnv:          defaultAppEnv,
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

	if cfg.MaxImageBytes, err = int64FromEnv(getenv, envMaxImageBytes, cfg.MaxImageBytes); err != nil {
		return Config{}, err
	}
	if cfg.MaxImageBytes <= 0 {
		return Config{}, fmt.Errorf("config: %s: value %d must be greater than 0", envMaxImageBytes, cfg.MaxImageBytes)
	}
	if cfg.MaxImagePixels, err = int64FromEnv(getenv, envMaxImagePixels, cfg.MaxImagePixels); err != nil {
		return Config{}, err
	}
	if cfg.MaxImagePixels <= 0 {
		return Config{}, fmt.Errorf("config: %s: value %d must be greater than 0", envMaxImagePixels, cfg.MaxImagePixels)
	}
	if cfg.SessionPoolSize, err = intFromEnv(getenv, envSessionPoolSize, cfg.SessionPoolSize); err != nil {
		return Config{}, err
	}
	if cfg.SessionPoolSize < 1 {
		return Config{}, fmt.Errorf("config: %s: value %d must be at least 1", envSessionPoolSize, cfg.SessionPoolSize)
	}

	if cfg.LogLevel, err = enumFromEnv(getenv, envLogLevel, cfg.LogLevel, "debug", "info", "warn", "error"); err != nil {
		return Config{}, err
	}
	if cfg.LogFormat, err = enumFromEnv(getenv, envLogFormat, cfg.LogFormat, "text", "json"); err != nil {
		return Config{}, err
	}
	return cfg, nil
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
	for _, a := range allowed {
		if value == a {
			return value, nil
		}
	}
	return "", fmt.Errorf("config: %s: invalid value %q (want one of %s)", key, raw, strings.Join(allowed, ", "))
}

func stringFromEnv(getenv func(string) string, key, def string) string {
	if value := strings.TrimSpace(getenv(key)); value != "" {
		return value
	}
	return def
}
