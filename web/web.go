package web

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net"
	"net/http"
	"time"

	"nsfw_sherlock/config"
	"nsfw_sherlock/engine"
	"nsfw_sherlock/internal/ocrguard"
	"nsfw_sherlock/utils"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/cors"
	fiberrecover "github.com/gofiber/fiber/v3/middleware/recover"
)

const (
	serverTimeout     = 240 * time.Second
	bodyLimitOverhead = 64 << 10
	bodyLimitFloor    = 1 << 20
)

var (
	analyzeTimeout     = 15 * time.Second
	shutdownTimeout    = 15 * time.Second
	ocrTimeout         = 10 * time.Second
	ocrConcurrency     = 2
	requestConcurrency = 16
	connectionLimit    = 256
)

// Analyzer is the engine surface the HTTP API needs.
type Analyzer interface {
	Analyze(ctx context.Context, data []byte) (engine.Analysis, error)
}

// TextChecker reports whether an encoded image contains bad words.
type TextChecker func(ctx context.Context, img []byte) (bool, error)

type handlers struct {
	analyzer Analyzer
	checker  TextChecker
	timeout  time.Duration
}

// NewApp builds the HTTP application with the legacy routes and middleware.
func NewApp(cfg config.Config, analyzer Analyzer, checker TextChecker, log *slog.Logger) *fiber.App {
	return newApp(context.Background(), cfg, analyzer, checker, log)
}

func newApp(baseCtx context.Context, cfg config.Config, analyzer Analyzer, checker TextChecker, log *slog.Logger) *fiber.App {
	if analyzer == nil {
		panic("web: NewApp: nil analyzer")
	}
	if log == nil {
		log = slog.Default()
	}

	h := &handlers{
		analyzer: analyzer,
		checker:  ocrguard.Guard(checker, ocrConcurrency, ocrTimeout),
		timeout:  analyzeTimeout,
	}
	limit := concurrencyLimiter(make(chan struct{}, requestConcurrency))

	app := fiber.New(fiber.Config{
		AppName:       "NSFW Detector API",
		StrictRouting: false,
		BodyLimit:     bodyLimitFor(cfg.MaxImageBytes),
		ReadTimeout:   serverTimeout,
		WriteTimeout:  serverTimeout,
		IdleTimeout:   serverTimeout,
		Concurrency:   connectionLimit,
		ErrorHandler:  errorHandler(log),
	})
	app.Use(requestContext(baseCtx))
	app.Use(requestLogger(log))
	app.Use(fiberrecover.New(fiberrecover.Config{EnableStackTrace: true}))
	app.Use(cors.New())

	app.Get("/ping", h.ping)
	app.Post("/pic/check", limit, h.picCheck)
	app.Post("/pic/labels", limit, h.picLabels)
	app.Post("/pic/analyze", limit, h.picAnalyze)
	return app
}

// StartWebServer serves the API on :cfg.Port until ctx is cancelled.
func StartWebServer(ctx context.Context, cfg config.Config, analyzer Analyzer, checker TextChecker, log *slog.Logger) error {
	if log == nil {
		log = slog.Default()
	}
	serverCtx, cancelRequests := context.WithCancel(context.Background())
	defer cancelRequests()
	timeout := shutdownTimeout

	app := newApp(serverCtx, cfg, analyzer, checker, log)

	addr := fmt.Sprintf(":%d", cfg.Port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("web: cannot listen on %s: %w", addr, err)
	}
	defer func() { _ = ln.Close() }()

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- app.Listener(ln, fiber.ListenConfig{DisableStartupMessage: true})
	}()

	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
	}

	log.Info("web: shutting down HTTP server", "timeout", timeout)
	cancelRequests()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	shutdownErr := app.ShutdownWithContext(shutdownCtx)
	_ = ln.Close()
	return errors.Join(shutdownErr, <-serveErr)
}

func (h *handlers) ping(c fiber.Ctx) error {
	return c.JSON(fiber.Map{"status": "OK"})
}

type picRequest struct {
	Base64   string `json:"base64"`
	Filename string `json:"filename"`
}

type checkResponse struct {
	Status   string `json:"status"`
	Message  string `json:"message"`
	NSFWText bool   `json:"nsfwText"`
	NSFWPic  bool   `json:"nsfwPic"`
}

type labelsResponse struct {
	Status   string  `json:"status"`
	Message  string  `json:"message"`
	Drawings float32 `json:"drawings"`
	Hentai   float32 `json:"hentai"`
	Neutral  float32 `json:"neutral"`
	Porn     float32 `json:"porn"`
	Sexy     float32 `json:"sexy"`
	NSFWText bool    `json:"nsfwText"`
}

type analyzeResponse struct {
	Status   string `json:"status"`
	Message  string `json:"message"`
	NSFWText bool   `json:"nsfwText"`
	engine.Analysis
}

func (h *handlers) picCheck(c fiber.Ctx) error {
	analysis, data, ok := h.analyze(c)
	if !ok {
		return nil
	}
	nsfwText, err := h.textNSFW(c.Context(), data)
	if err != nil {
		return utils.ReportError(c, err.Error(), fiber.StatusInternalServerError)
	}
	return c.JSON(checkResponse{
		Status:   "ok",
		Message:  "success",
		NSFWText: nsfwText,
		NSFWPic:  analysis.LegacyNSFW(),
	})
}

func (h *handlers) picLabels(c fiber.Ctx) error {
	analysis, data, ok := h.analyze(c)
	if !ok {
		return nil
	}
	nsfwText, err := h.textNSFW(c.Context(), data)
	if err != nil {
		return utils.ReportError(c, err.Error(), fiber.StatusInternalServerError)
	}
	return c.JSON(labelsResponse{
		Status:   "ok",
		Message:  "success",
		Drawings: analysis.Labels.Drawings,
		Hentai:   analysis.Labels.Hentai,
		Neutral:  analysis.Labels.Neutral,
		Porn:     analysis.Labels.Porn,
		Sexy:     analysis.Labels.Sexy,
		NSFWText: nsfwText,
	})
}

func (h *handlers) picAnalyze(c fiber.Ctx) error {
	analysis, data, ok := h.analyze(c)
	if !ok {
		return nil
	}
	nsfwText, err := h.textNSFW(c.Context(), data)
	if err != nil {
		return utils.ReportError(c, err.Error(), fiber.StatusInternalServerError)
	}
	return c.JSON(analyzeResponse{
		Status:   "ok",
		Message:  "success",
		NSFWText: nsfwText,
		Analysis: analysis,
	})
}

func (h *handlers) analyze(c fiber.Ctx) (engine.Analysis, []byte, bool) {
	var req picRequest
	if err := c.Bind().JSON(&req); err != nil {
		_ = utils.ReportError(c, err.Error(), fiber.StatusBadRequest)
		return engine.Analysis{}, nil, false
	}
	if req.Base64 == "" {
		_ = utils.ReportError(c, "Bad Request", fiber.StatusBadRequest)
		return engine.Analysis{}, nil, false
	}
	data, err := utils.DecodePayload(req.Base64)
	if err != nil {
		_ = utils.ReportError(c, err.Error(), fiber.StatusBadRequest)
		return engine.Analysis{}, nil, false
	}

	ctx, cancel := context.WithTimeout(c.Context(), h.timeout)
	defer cancel()
	analysis, err := h.analyzer.Analyze(ctx, data)
	if err != nil {
		_ = utils.ReportError(c, err.Error(), analyzeErrorStatus(err))
		return engine.Analysis{}, nil, false
	}
	if analysis.Detections == nil {
		analysis.Detections = []engine.Detection{}
	}
	return analysis, data, true
}

func (h *handlers) textNSFW(ctx context.Context, data []byte) (bool, error) {
	if h.checker == nil {
		return false, nil
	}
	return h.checker(ctx, data)
}

func analyzeErrorStatus(err error) int {
	switch {
	case errors.Is(err, engine.ErrImageTooLarge):
		return fiber.StatusRequestEntityTooLarge
	case errors.Is(err, engine.ErrUnsupportedImage):
		return fiber.StatusBadRequest
	case errors.Is(err, context.DeadlineExceeded):
		return fiber.StatusGatewayTimeout
	default:
		return fiber.StatusInternalServerError
	}
}

func bodyLimitFor(maxImageBytes int64) int {
	if maxImageBytes <= 0 {
		return bodyLimitFloor
	}
	const maxInt32 = int64(math.MaxInt32)
	if maxImageBytes > maxInt32/4*3 {
		return math.MaxInt32
	}
	limit := maxImageBytes/3*4 + 4 + bodyLimitOverhead
	if limit < bodyLimitFloor {
		return bodyLimitFloor
	}
	if limit > maxInt32 {
		return math.MaxInt32
	}
	return int(limit)
}

func requestContext(baseCtx context.Context) fiber.Handler {
	return func(c fiber.Ctx) error {
		ctx, cancel := context.WithCancel(baseCtx)
		defer cancel()
		c.SetContext(ctx)
		return c.Next()
	}
}

func concurrencyLimiter(sem chan struct{}) fiber.Handler {
	return func(c fiber.Ctx) error {
		select {
		case sem <- struct{}{}:
			defer func() { <-sem }()
			return c.Next()
		default:
			return utils.ReportError(c, "server is busy", fiber.StatusServiceUnavailable)
		}
	}
}

func errorHandler(log *slog.Logger) fiber.ErrorHandler {
	return func(c fiber.Ctx, err error) error {
		status := fiber.StatusInternalServerError
		message := http.StatusText(status)
		var fe *fiber.Error
		if errors.As(err, &fe) && fe.Code < fiber.StatusInternalServerError {
			status = fe.Code
			message = fe.Message
		} else {
			log.Error("http error", "method", c.Method(), "path", c.Path(), "error", err)
		}
		return utils.ReportError(c, message, status)
	}
}

func requestLogger(log *slog.Logger) fiber.Handler {
	return func(c fiber.Ctx) error {
		start := time.Now()
		err := c.Next()
		status := c.Response().StatusCode()
		if err != nil {
			status = errorStatus(err)
		}
		attrs := []any{
			"method", c.Method(),
			"path", c.Path(),
			"status", status,
			"duration", time.Since(start),
		}
		if err != nil {
			log.Error("http request", append(attrs, "error", err)...)
			return err
		}
		log.Info("http request", attrs...)
		return nil
	}
}

func errorStatus(err error) int {
	var fe *fiber.Error
	if errors.As(err, &fe) {
		return fe.Code
	}
	return fiber.StatusInternalServerError
}
