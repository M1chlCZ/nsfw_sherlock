package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"nsfw_sherlock/config"
	"nsfw_sherlock/engine"
	"nsfw_sherlock/grpcServer"
	"nsfw_sherlock/logging"
	"nsfw_sherlock/textcheck"
	"nsfw_sherlock/web"
)

func main() {
	os.Exit(run())
}

func run() int {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	log, err := logging.New(cfg.LogLevel, cfg.LogFormat)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := serve(cfg, log); err != nil {
		log.Error("fatal", "error", err)
		return 1
	}
	return 0
}

func serve(cfg config.Config, log *slog.Logger) error {
	if err := engine.InitRuntime(cfg.ORTLib); err != nil {
		return fmt.Errorf("onnxruntime unavailable: set ORT_LIB or run scripts/fetch-models.sh --ort-only: %w", err)
	}
	m, err := engine.LoadManifest(cfg.Manifest)
	if err != nil {
		return err
	}
	eng, err := engine.NewEngine(m, engine.Options{
		Profile:       cfg.Profile,
		ModelsDir:     cfg.ModelsDir,
		PoolSize:      cfg.SessionPoolSize,
		AllowDegraded: cfg.AllowDegraded,
		Limits:        engine.Limits{MaxBytes: cfg.MaxImageBytes, MaxPixels: cfg.MaxImagePixels},
		Log:           log,
	})
	if err != nil {
		return err
	}
	defer func() {
		if err := eng.Close(); err != nil {
			log.Error("engine close", "error", err)
		}
	}()
	for _, warning := range eng.Warnings() {
		log.Warn(warning)
	}

	if err := loadBadWords(cfg, log); err != nil {
		return err
	}

	var checker func(context.Context, []byte) (bool, error)
	if cfg.OCR && textcheck.Enabled() {
		if err := textcheck.Warmup(); err != nil {
			log.Warn("OCR warmup failed; nsfwText will always be false", "error", err)
		} else {
			checker = func(_ context.Context, img []byte) (bool, error) {
				return textcheck.DetectText(img)
			}
		}
	} else {
		log.Warn("OCR disabled; nsfwText will always be false", "ocr_enabled", cfg.OCR, "ocr_available", textcheck.Enabled())
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Info("starting server", "app_env", cfg.AppEnv, "port", cfg.Port)
	if cfg.AppEnv == "grpc" {
		return grpcServer.StartGrpcServer(ctx, cfg, eng, checker, log)
	}
	return web.StartWebServer(ctx, cfg, eng, checker, log)
}

func loadBadWords(cfg config.Config, log *slog.Logger) error {
	path := cfg.BadWordsFile
	if path == "" {
		for _, candidate := range []string{"bad_words.txt", "/bad_words.txt"} {
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				path = candidate
				break
			}
		}
	}
	if err := textcheck.LoadBadWords(path); err != nil {
		log.Warn("bad words load failed; using embedded fallback", "path", path, "error", err)
		if err := textcheck.LoadBadWords(""); err != nil {
			return fmt.Errorf("load embedded bad words: %w", err)
		}
	}
	return nil
}
