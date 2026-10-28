package web

import (
	"context"
	"encoding/json"
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
	"nsfw_sherlock/internal/video"
	"nsfw_sherlock/utils"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/cors"
	fiberrecover "github.com/gofiber/fiber/v3/middleware/recover"
	"github.com/gofiber/fiber/v3/middleware/requestid"
	"golang.org/x/sync/errgroup"
)

const (
	successMessage    = "success"
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
	AnalyzeWith(ctx context.Context, data []byte, opts engine.AnalyzeOptions) (engine.Analysis, error)
}

// TextChecker reports whether an encoded image contains bad words.
type TextChecker func(ctx context.Context, img []byte) (bool, error)

type handlers struct {
	analyzer       Analyzer
	checker        TextChecker
	timeout        time.Duration
	batchMax       int
	cache          *responseCache
	metrics        *metricsState
	video          video.Extractor
	videoLimits    video.Limits
	videoMaxBytes  int64
	videoAvailable bool
	jobs           *jobManager
}

// NewApp builds the HTTP application with the legacy routes and middleware.
func NewApp(cfg config.Config, analyzer Analyzer, checker TextChecker, log *slog.Logger) *fiber.App {
	return newApp(context.Background(), cfg, analyzer, checker, log)
}

func newApp(
	baseCtx context.Context,
	cfg config.Config,
	analyzer Analyzer,
	checker TextChecker,
	log *slog.Logger,
) *fiber.App {
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
		batchMax: max(cfg.BatchMaxImages, 1),
		video:    video.New(cfg.FFmpegPath),
		videoLimits: video.Limits{
			FPS:       cfg.VideoSampleFPS,
			MaxFrames: cfg.VideoMaxFrames,
			MaxWidth:  videoMaxWidth,
		},
		videoMaxBytes:  cfg.VideoMaxBytes,
		videoAvailable: ffmpegAvailable(cfg.FFmpegPath),
		jobs:           newJobManager(baseCtx),
	}
	if cfg.CacheEntries > 0 {
		h.cache = newResponseCache(cfg.CacheEntries)
	}
	h.metrics = newMetricsState(h.cache)
	limit := concurrencyLimiter(make(chan struct{}, requestConcurrency))
	bodyLimit := batchBodyLimit(cfg.MaxImageBytes, h.batchMax)
	if videoLimit := int(cfg.VideoMaxBytes) + bodyLimitOverhead; videoLimit > bodyLimit {
		bodyLimit = videoLimit
	}

	app := fiber.New(fiber.Config{
		AppName:       "NSFW Detector API",
		StrictRouting: false,
		BodyLimit:     bodyLimit,
		ReadTimeout:   serverTimeout,
		WriteTimeout:  serverTimeout,
		IdleTimeout:   serverTimeout,
		Concurrency:   connectionLimit,
		ErrorHandler:  errorHandler(log),
	})
	app.Use(requestContext(baseCtx))
	app.Use(requestid.New())
	app.Use(requestLogger(log))
	app.Use(fiberrecover.New(fiberrecover.Config{EnableStackTrace: true}))
	app.Use(cors.New())
	app.Use(metricsMiddleware(h.metrics))
	if guard := apiKeyGuard(cfg.APIKeys); guard != nil {
		app.Use(guard)
	}
	if cfg.RateLimitRPM > 0 {
		app.Use(rateLimitMiddleware(newRateLimiter(cfg.RateLimitRPM, len(cfg.APIKeys) > 0)))
	}

	app.Get("/ping", h.ping)
	app.Get("/ready", h.ready)
	app.Get(metricsPath, h.metrics.handle)
	app.Post("/pic/check", limit, h.picCheck)
	app.Post("/pic/labels", limit, h.picLabels)
	app.Post("/pic/analyze", limit, h.picAnalyze)
	app.Post("/pic/batch", limit, h.picBatch)
	app.Post("/pic/upload", limit, h.picUpload)
	app.Post("/video/check", limit, h.videoCheck)
	app.Post("/jobs", limit, h.submitJob)
	app.Get("/jobs/:id", h.getJob)
	app.Delete("/jobs/:id", h.cancelJob)
	return app
}

// StartWebServer serves the API on :cfg.Port until ctx is cancelled.
func StartWebServer(
	ctx context.Context,
	cfg config.Config,
	analyzer Analyzer,
	checker TextChecker,
	log *slog.Logger,
) error {
	if log == nil {
		log = slog.Default()
	}
	serverCtx, cancelRequests := context.WithCancel(context.Background())
	defer cancelRequests()
	timeout := shutdownTimeout

	app := newApp(serverCtx, cfg, analyzer, checker, log)

	addr := fmt.Sprintf(":%d", cfg.Port)
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", addr)
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

	log.InfoContext(ctx, "web: shutting down HTTP server", "timeout", timeout)
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

type readyResponse struct {
	Status  string `json:"status"`
	Message string `json:"message"`
	Video   bool   `json:"video"`
}

// ready reports that the API serves requests and whether video scanning is available.
func (h *handlers) ready(c fiber.Ctx) error {
	return c.JSON(readyResponse{Status: "ok", Message: successMessage, Video: h.videoAvailable})
}

type picRequest struct {
	Base64      string  `json:"base64"`
	Filename    string  `json:"filename"`
	Sensitivity float64 `json:"sensitivity"`
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
	engine.Analysis

	Status   string `json:"status"`
	Message  string `json:"message"`
	NSFWText bool   `json:"nsfwText"`
}

func (h *handlers) picCheck(c fiber.Ctx) error {
	return h.picAnalysis(c, "/pic/check", func(analysis engine.Analysis, nsfwText bool) any {
		return checkResponse{
			Status:   "ok",
			Message:  successMessage,
			NSFWText: nsfwText,
			NSFWPic:  analysis.LegacyNSFW(),
		}
	})
}

func (h *handlers) picLabels(c fiber.Ctx) error {
	return h.picAnalysis(c, "/pic/labels", func(analysis engine.Analysis, nsfwText bool) any {
		return labelsResponse{
			Status:   "ok",
			Message:  successMessage,
			Drawings: analysis.Labels.Drawings,
			Hentai:   analysis.Labels.Hentai,
			Neutral:  analysis.Labels.Neutral,
			Porn:     analysis.Labels.Porn,
			Sexy:     analysis.Labels.Sexy,
			NSFWText: nsfwText,
		}
	})
}

func (h *handlers) picAnalyze(c fiber.Ctx) error {
	return h.picAnalysis(c, "/pic/analyze", func(analysis engine.Analysis, nsfwText bool) any {
		return analyzeResponse{
			Status:   "ok",
			Message:  successMessage,
			NSFWText: nsfwText,
			Analysis: analysis,
		}
	})
}

func (h *handlers) picAnalysis(c fiber.Ctx, endpoint string, build func(engine.Analysis, bool) any) error {
	data, sensitivity, ok := h.picData(c)
	if !ok {
		return nil
	}
	key := cacheKey(endpoint, sensitivity, data)
	if h.cache != nil {
		if body, hit := h.cache.get(key); hit {
			return sendCachedJSON(c, body)
		}
	}
	analysis, err := h.analyzeData(c.Context(), data, sensitivity)
	if err != nil {
		return utils.ReportError(c, err.Error(), analyzeErrorStatus(err))
	}
	h.metrics.observeVerdict(analysis.Verdict)
	nsfwText, err := h.textNSFW(c.Context(), data)
	if err != nil {
		return utils.ReportError(c, err.Error(), fiber.StatusInternalServerError)
	}
	return h.sendJSON(c, key, build(analysis, nsfwText))
}

func (h *handlers) analyzeData(ctx context.Context, data []byte, sensitivity float64) (engine.Analysis, error) {
	ctx, cancel := context.WithTimeout(ctx, h.timeout)
	defer cancel()
	analysis, err := h.analyzer.AnalyzeWith(ctx, data, engine.AnalyzeOptions{Sensitivity: sensitivity})
	if err != nil {
		return engine.Analysis{}, err
	}
	if analysis.Detections == nil {
		analysis.Detections = []engine.Detection{}
	}
	return analysis, nil
}

func (h *handlers) sendJSON(c fiber.Ctx, key string, value any) error {
	body, err := json.Marshal(value)
	if err != nil {
		return utils.ReportError(c, "internal error", fiber.StatusInternalServerError)
	}
	if h.cache != nil {
		h.cache.put(key, body)
	}
	return sendCachedJSON(c, body)
}

func sendCachedJSON(c fiber.Ctx, body []byte) error {
	c.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)
	return c.Send(body)
}

func (h *handlers) picData(c fiber.Ctx) ([]byte, float64, bool) {
	var req picRequest
	if err := c.Bind().JSON(&req); err != nil {
		_ = utils.ReportError(c, err.Error(), fiber.StatusBadRequest)
		return nil, 0, false
	}
	if req.Base64 == "" {
		_ = utils.ReportError(c, "Bad Request", fiber.StatusBadRequest)
		return nil, 0, false
	}
	if req.Sensitivity != 0 &&
		(math.IsNaN(req.Sensitivity) || math.IsInf(req.Sensitivity, 0) ||
			req.Sensitivity < 0.1 || req.Sensitivity > 10) {
		_ = utils.ReportError(c, "sensitivity must be between 0.1 and 10", fiber.StatusBadRequest)
		return nil, 0, false
	}
	data, err := utils.DecodePayload(req.Base64)
	if err != nil {
		_ = utils.ReportError(c, err.Error(), fiber.StatusBadRequest)
		return nil, 0, false
	}
	return data, req.Sensitivity, true
}

type batchRequest struct {
	Images []picRequest `json:"images"`
}

type batchResponse struct {
	Status  string `json:"status"`
	Message string `json:"message"`
	Results []any  `json:"results"`
}

func (h *handlers) picBatch(c fiber.Ctx) error {
	var req batchRequest
	if err := c.Bind().JSON(&req); err != nil {
		return utils.ReportError(c, err.Error(), fiber.StatusBadRequest)
	}
	if len(req.Images) == 0 {
		return utils.ReportError(c, "no images in the batch", fiber.StatusBadRequest)
	}
	if len(req.Images) > h.batchMax {
		return utils.ReportError(
			c,
			fmt.Sprintf("the batch limit is %d images", h.batchMax),
			fiber.StatusBadRequest,
		)
	}
	return c.JSON(batchResponse{
		Status:  "ok",
		Message: successMessage,
		Results: h.analyzeBatch(c.Context(), req.Images),
	})
}

func (h *handlers) analyzeBatch(ctx context.Context, images []picRequest) []any {
	results := make([]any, len(images))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(min(h.batchMax, requestConcurrency))
	for i := range images {
		g.Go(func() error {
			results[i] = h.checkOne(gctx, images[i])
			return nil
		})
	}
	_ = g.Wait()
	return results
}

func (h *handlers) checkOne(ctx context.Context, req picRequest) any {
	if req.Base64 == "" {
		return utils.ErrorResult("Bad Request")
	}
	if req.Sensitivity != 0 &&
		(math.IsNaN(req.Sensitivity) || math.IsInf(req.Sensitivity, 0) ||
			req.Sensitivity < 0.1 || req.Sensitivity > 10) {
		return utils.ErrorResult("sensitivity must be between 0.1 and 10")
	}
	data, err := utils.DecodePayload(req.Base64)
	if err != nil {
		return utils.ErrorResult(err.Error())
	}
	key := cacheKey("/pic/check", req.Sensitivity, data)
	if h.cache != nil {
		if body, hit := h.cache.get(key); hit {
			return json.RawMessage(body)
		}
	}
	analysis, err := h.analyzeData(ctx, data, req.Sensitivity)
	if err != nil {
		return utils.ErrorResult(err.Error())
	}
	h.metrics.observeVerdict(analysis.Verdict)
	nsfwText, err := h.textNSFW(ctx, data)
	if err != nil {
		return utils.ErrorResult(err.Error())
	}
	body, err := json.Marshal(checkResponse{
		Status:   "ok",
		Message:  successMessage,
		NSFWText: nsfwText,
		NSFWPic:  analysis.LegacyNSFW(),
	})
	if err != nil {
		return utils.ErrorResult("internal error")
	}
	if h.cache != nil {
		h.cache.put(key, body)
	}
	return json.RawMessage(body)
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
	case errors.Is(err, engine.ErrInvalidSensitivity):
		return fiber.StatusBadRequest
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

func batchBodyLimit(maxImageBytes int64, batchMax int) int {
	single := int64(bodyLimitFor(maxImageBytes))
	if batchMax < 1 {
		batchMax = 1
	}
	scaled := single * int64(batchMax)
	if scaled > math.MaxInt32 || scaled < single {
		return math.MaxInt32
	}
	return int(scaled)
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
		if rid := requestid.FromContext(c); rid != "" {
			attrs = append(attrs, "requestId", rid)
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
	if fe, ok := errors.AsType[*fiber.Error](err); ok {
		return fe.Code
	}
	return fiber.StatusInternalServerError
}
