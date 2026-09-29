package config

import (
	"strings"
	"testing"
)

func getenvFrom(env map[string]string) func(string) string {
	return func(key string) string { return env[key] }
}

func wantDefaults() Config {
	return Config{
		AppEnv:          "web",
		ORTProvider:     "cpu",
		Port:            4000,
		ModelsDir:       "./assets/models",
		Manifest:        "models/manifest.json",
		ORTLib:          "",
		Profile:         "balanced",
		AllowDegraded:   false,
		OCR:             true,
		BadWordsFile:    "",
		MaxImageBytes:   20 << 20,
		MaxImagePixels:  50_000_000,
		SessionPoolSize: 2,
		LogLevel:        "info",
		LogFormat:       "text",
	}
}

func TestLoadDefaults(t *testing.T) {
	got, err := Load(func(string) string { return "" })
	if err != nil {
		t.Fatalf("Load() returned error %v, want nil", err)
	}
	if want := wantDefaults(); got != want {
		t.Errorf("Load() = %+v, want defaults %+v", got, want)
	}
}

func TestLoadOverrides(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want func(Config) Config
	}{
		{
			name: "server and paths",
			env: map[string]string{
				"APP_ENV":        "grpc",
				"PORT":           "8443",
				"MODELS_DIR":     "/srv/models",
				"MANIFEST":       "/srv/manifest.json",
				"ORT_LIB":        "/opt/ort/libonnxruntime.so",
				"BAD_WORDS_FILE": "/etc/bad.txt",
			},
			want: func(c Config) Config {
				c.AppEnv = "grpc"
				c.Port = 8443
				c.ModelsDir = "/srv/models"
				c.Manifest = "/srv/manifest.json"
				c.ORTLib = "/opt/ort/libonnxruntime.so"
				c.BadWordsFile = "/etc/bad.txt"
				return c
			},
		},
		{
			name: "legacy http app env alias",
			env:  map[string]string{"APP_ENV": "http"},
			want: func(c Config) Config {
				c.AppEnv = "web"
				return c
			},
		},
		{
			name: "engine knobs",
			env: map[string]string{
				"NSFW_PROFILE":        "strict",
				"NSFW_ALLOW_DEGRADED": "true",
				"OCR_ENABLED":         "0",
				"MAX_IMAGE_BYTES":     "1048576",
				"MAX_IMAGE_PIXELS":    "1000000",
				"SESSION_POOL_SIZE":   "8",
			},
			want: func(c Config) Config {
				c.Profile = "strict"
				c.AllowDegraded = true
				c.OCR = false
				c.MaxImageBytes = 1048576
				c.MaxImagePixels = 1000000
				c.SessionPoolSize = 8
				return c
			},
		},
		{
			name: "logging",
			env: map[string]string{
				"LOG_LEVEL":  "debug",
				"LOG_FORMAT": "json",
			},
			want: func(c Config) Config {
				c.LogLevel = "debug"
				c.LogFormat = "json"
				return c
			},
		},
		{
			name: "bool spellings",
			env: map[string]string{
				"NSFW_ALLOW_DEGRADED": "1",
				"OCR_ENABLED":         "f",
			},
			want: func(c Config) Config {
				c.AllowDegraded = true
				c.OCR = false
				return c
			},
		},
		{
			name: "ocr explicitly enabled",
			env:  map[string]string{"OCR_ENABLED": "true"},
			want: func(c Config) Config { return c },
		},
		{
			name: "unset keys keep defaults",
			env:  map[string]string{"PORT": "4001"},
			want: func(c Config) Config {
				c.Port = 4001
				return c
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Load(getenvFrom(tt.env))
			if err != nil {
				t.Fatalf("Load(%v) returned error %v, want nil", tt.env, err)
			}
			want := tt.want(wantDefaults())
			if got != want {
				t.Errorf("Load(%v) = %+v, want %+v", tt.env, got, want)
			}
		})
	}
}

func TestLoadInvalid(t *testing.T) {
	tests := []struct {
		name      string
		env       map[string]string
		wantKey   string
		wantValue string
	}{
		{"port not a number", map[string]string{"PORT": "abc"}, "PORT", "abc"},
		{"port zero", map[string]string{"PORT": "0"}, "PORT", "0"},
		{"port too large", map[string]string{"PORT": "70000"}, "PORT", "70000"},
		{"port negative", map[string]string{"PORT": "-1"}, "PORT", "-1"},
		{"app env unknown", map[string]string{"APP_ENV": "cli"}, "APP_ENV", "cli"},
		{"allow degraded not bool", map[string]string{"NSFW_ALLOW_DEGRADED": "maybe"}, "NSFW_ALLOW_DEGRADED", "maybe"},
		{"ocr not bool", map[string]string{"OCR_ENABLED": "yes"}, "OCR_ENABLED", "yes"},
		{"max bytes zero", map[string]string{"MAX_IMAGE_BYTES": "0"}, "MAX_IMAGE_BYTES", "0"},
		{"max bytes negative", map[string]string{"MAX_IMAGE_BYTES": "-5"}, "MAX_IMAGE_BYTES", "-5"},
		{"max pixels zero", map[string]string{"MAX_IMAGE_PIXELS": "0"}, "MAX_IMAGE_PIXELS", "0"},
		{"max pixels negative", map[string]string{"MAX_IMAGE_PIXELS": "-1"}, "MAX_IMAGE_PIXELS", "-1"},
		{"pool zero", map[string]string{"SESSION_POOL_SIZE": "0"}, "SESSION_POOL_SIZE", "0"},
		{"pool negative", map[string]string{"SESSION_POOL_SIZE": "-2"}, "SESSION_POOL_SIZE", "-2"},
		{"log level unknown", map[string]string{"LOG_LEVEL": "verbose"}, "LOG_LEVEL", "verbose"},
		{"log format unknown", map[string]string{"LOG_FORMAT": "xml"}, "LOG_FORMAT", "xml"},
		{"profile blank", map[string]string{"NSFW_PROFILE": "   "}, "NSFW_PROFILE", ""},
		{"manifest blank", map[string]string{"MANIFEST": "   "}, "MANIFEST", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(getenvFrom(tt.env))
			if err == nil {
				t.Fatalf("Load(%v) returned nil error, want error naming %s", tt.env, tt.wantKey)
			}
			if !strings.Contains(err.Error(), tt.wantKey) {
				t.Errorf("Load(%v) error = %q, want it to mention %s", tt.env, err, tt.wantKey)
			}
			if tt.wantValue != "" && !strings.Contains(err.Error(), tt.wantValue) {
				t.Errorf("Load(%v) error = %q, want it to mention offending value %q", tt.env, err, tt.wantValue)
			}
		})
	}
}

func TestLoadWhitespace(t *testing.T) {
	t.Run("values are trimmed", func(t *testing.T) {
		env := map[string]string{
			"APP_ENV":             "  grpc ",
			"PORT":                " 8443 ",
			"MODELS_DIR":          " /srv/models\t",
			"MANIFEST":            " /srv/manifest.json ",
			"ORT_LIB":             " /opt/ort/libonnxruntime.so ",
			"NSFW_PROFILE":        "  strict ",
			"NSFW_ALLOW_DEGRADED": " true ",
			"OCR_ENABLED":         " false ",
			"BAD_WORDS_FILE":      " /etc/bad.txt ",
			"MAX_IMAGE_BYTES":     " 1048576 ",
			"MAX_IMAGE_PIXELS":    " 1000000 ",
			"SESSION_POOL_SIZE":   " 4 ",
			"LOG_LEVEL":           " warn ",
			"LOG_FORMAT":          " json ",
		}
		got, err := Load(getenvFrom(env))
		if err != nil {
			t.Fatalf("Load(%v) returned error %v, want nil", env, err)
		}
		want := wantDefaults()
		want.AppEnv = "grpc"
		want.Port = 8443
		want.ModelsDir = "/srv/models"
		want.Manifest = "/srv/manifest.json"
		want.ORTLib = "/opt/ort/libonnxruntime.so"
		want.Profile = "strict"
		want.AllowDegraded = true
		want.OCR = false
		want.BadWordsFile = "/etc/bad.txt"
		want.MaxImageBytes = 1048576
		want.MaxImagePixels = 1000000
		want.SessionPoolSize = 4
		want.LogLevel = "warn"
		want.LogFormat = "json"
		if got != want {
			t.Errorf("Load(%v) = %+v, want %+v", env, got, want)
		}
	})

	t.Run("whitespace-only strings use defaults", func(t *testing.T) {
		env := map[string]string{
			"MODELS_DIR":     "   ",
			"ORT_LIB":        "\t",
			"BAD_WORDS_FILE": " ",
		}
		got, err := Load(getenvFrom(env))
		if err != nil {
			t.Fatalf("Load(%v) returned error %v, want nil", env, err)
		}
		if want := wantDefaults(); got != want {
			t.Errorf("Load(%v) = %+v, want defaults %+v", env, got, want)
		}
	})

	t.Run("numeric parse error reports raw value", func(t *testing.T) {
		_, err := Load(getenvFrom(map[string]string{"PORT": " 12x "}))
		if err == nil {
			t.Fatal("Load() returned nil error, want parse error")
		}
		for _, want := range []string{"PORT", `" 12x "`} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("Load() error = %q, want it to contain %q", err, want)
			}
		}
	})
}

func TestLoadInvalidGPUConfig(t *testing.T) {
	for _, env := range []map[string]string{
		{"ORT_PROVIDER": "unknown"},
		{"ORT_DEVICE_ID": "-1"},
		{"ORT_DEVICE_ID": "2147483648"},
		{"ORT_DEVICE_ID": "invalid"},
	} {
		if _, err := Load(getenvFrom(env)); err == nil {
			t.Errorf("Load(%v) accepted invalid GPU configuration", env)
		}
	}
}

func TestLoadGPUConfig(t *testing.T) {
	for _, provider := range []string{"cpu", "cuda", "migraphx", "rocm"} {
		cfg, err := Load(getenvFrom(map[string]string{"ORT_PROVIDER": provider, "ORT_DEVICE_ID": "2"}))
		if err != nil {
			t.Fatal(err)
		}
		want := provider
		if want == "rocm" {
			want = "migraphx"
		}
		if cfg.ORTProvider != want || cfg.ORTDeviceID != 2 {
			t.Fatalf("Load(%s) = %+v", provider, cfg)
		}
	}
}
