package web

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"nsfw_sherlock/config"
	"nsfw_sherlock/engine"

	"github.com/gofiber/fiber/v3"
)

type fakeAnalyzer struct {
	analysis engine.Analysis
	err      error
	fn       func(ctx context.Context, data []byte) (engine.Analysis, error)

	mu    sync.Mutex
	calls int
	data  []byte
	ctx   context.Context
}

func (f *fakeAnalyzer) Analyze(ctx context.Context, data []byte) (engine.Analysis, error) {
	f.mu.Lock()
	f.calls++
	f.data = append([]byte(nil), data...)
	f.ctx = ctx
	f.mu.Unlock()
	if f.fn != nil {
		return f.fn(ctx, data)
	}
	return f.analysis, f.err
}

func (f *fakeAnalyzer) snapshot() (int, []byte, context.Context) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls, append([]byte(nil), f.data...), f.ctx
}

type captureHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *captureHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	h.records = append(h.records, r.Clone())
	h.mu.Unlock()
	return nil
}

func (h *captureHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *captureHandler) WithGroup(string) slog.Handler      { return h }

func (h *captureHandler) statuses() []int {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []int
	for _, record := range h.records {
		record.Attrs(func(attr slog.Attr) bool {
			if attr.Key == "status" {
				out = append(out, int(attr.Value.Int64()))
				return false
			}
			return true
		})
	}
	return out
}

func testLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

func override[T any](t *testing.T, target *T, value T) {
	t.Helper()
	previous := *target
	*target = value
	t.Cleanup(func() { *target = previous })
}

func testApp(t *testing.T, analyzer Analyzer, checker TextChecker) *fiber.App {
	t.Helper()
	return NewApp(config.Config{MaxImageBytes: 1 << 20}, analyzer, checker, testLogger())
}

func newJSONRequest(method, path, body string) *http.Request {
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	return req
}

func send(app *fiber.App, req *http.Request) (int, []byte, error) {
	resp, err := app.Test(req, fiber.TestConfig{Timeout: 5 * time.Second})
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, body, nil
}

func doRequest(t *testing.T, app *fiber.App, method, path, body string) (int, []byte) {
	t.Helper()
	status, data, err := send(app, newJSONRequest(method, path, body))
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return status, data
}

func picBody(payload, filename string) string {
	raw, _ := json.Marshal(picRequest{Base64: payload, Filename: filename})
	return string(raw)
}

func postPic(t *testing.T, app *fiber.App, path, payload, filename string) (int, []byte) {
	t.Helper()
	return doRequest(t, app, http.MethodPost, path, picBody(payload, filename))
}

func decodeJSON(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var fields map[string]any
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatalf("response is not valid JSON: %v\nbody: %s", err, body)
	}
	return fields
}

func assertLegacyError(t *testing.T, body []byte) {
	t.Helper()
	fields := decodeJSON(t, body)
	if len(fields) != 3 {
		t.Fatalf("error body has %d fields, want exactly 3: %v", len(fields), fields)
	}
	if fields["status"] != "FAIL" {
		t.Errorf("status = %v, want FAIL", fields["status"])
	}
	if fields["hasError"] != true {
		t.Errorf("hasError = %v, want true", fields["hasError"])
	}
	msg, _ := fields["errorMessage"].(string)
	if msg == "" {
		t.Errorf("errorMessage = %v, want non-empty string", fields["errorMessage"])
	}
}

func containsStatus(statuses []int, want int) bool {
	return slices.Contains(statuses, want)
}

func TestPing(t *testing.T) {
	app := testApp(t, &fakeAnalyzer{}, nil)
	status, body := doRequest(t, app, http.MethodGet, "/ping", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if got := string(body); got != `{"status":"OK"}` {
		t.Errorf("body = %s, want {\"status\":\"OK\"}", got)
	}
}

func TestPicCheckHappyPath(t *testing.T) {
	image := []byte("fake-image-bytes")
	payload := base64.StdEncoding.EncodeToString(image)

	tests := []struct {
		name         string
		analysis     engine.Analysis
		checker      TextChecker
		wantNSFWText bool
	}{
		{
			name:         "legacy unsafe labels",
			analysis:     engine.Analysis{Labels: engine.Labels{Porn: 0.9}},
			checker:      func(context.Context, []byte) (bool, error) { return true, nil },
			wantNSFWText: true,
		},
		{
			name:         "legacy safe labels",
			analysis:     engine.Analysis{Labels: engine.Labels{Neutral: 0.9}},
			checker:      func(context.Context, []byte) (bool, error) { return false, nil },
			wantNSFWText: false,
		},
		{
			name:         "nil checker reports no text",
			analysis:     engine.Analysis{Labels: engine.Labels{Porn: 0.9}},
			checker:      nil,
			wantNSFWText: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			analyzer := &fakeAnalyzer{analysis: tt.analysis}
			app := testApp(t, analyzer, tt.checker)
			status, body := postPic(t, app, "/pic/check", payload, "picture.png")
			if status != http.StatusOK {
				t.Fatalf("status = %d, want 200; body: %s", status, body)
			}

			want := map[string]any{
				"status":   "ok",
				"message":  "success",
				"nsfwText": tt.wantNSFWText,
				"nsfwPic":  tt.analysis.LegacyNSFW(),
			}
			if got := decodeJSON(t, body); !reflect.DeepEqual(got, want) {
				t.Errorf("body = %v, want %v", got, want)
			}

			calls, data, _ := analyzer.snapshot()
			if calls != 1 {
				t.Fatalf("Analyze calls = %d, want 1", calls)
			}
			if !bytes.Equal(data, image) {
				t.Errorf("Analyze data = %q, want %q", data, image)
			}
		})
	}
}

func TestCheckerReceivesDecodedBytes(t *testing.T) {
	image := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}
	got := make(chan []byte, 1)
	checker := func(_ context.Context, img []byte) (bool, error) {
		got <- append([]byte(nil), img...)
		return true, nil
	}
	app := testApp(t, &fakeAnalyzer{}, checker)

	status, body := postPic(t, app, "/pic/check", base64.StdEncoding.EncodeToString(image), "x.png")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", status, body)
	}
	select {
	case img := <-got:
		if !bytes.Equal(img, image) {
			t.Errorf("checker image = %v, want %v", img, image)
		}
	default:
		t.Fatal("checker was not called")
	}
}

func TestAnalyzeUsesDeadline(t *testing.T) {
	analyzer := &fakeAnalyzer{}
	app := testApp(t, analyzer, nil)
	postPic(t, app, "/pic/check", base64.StdEncoding.EncodeToString([]byte("img")), "")

	_, _, ctx := analyzer.snapshot()
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("Analyze context has no deadline")
	}
	if remaining := time.Until(deadline); remaining <= 0 || remaining > analyzeTimeout {
		t.Errorf("Analyze deadline in %s, want in (0, %s]", remaining, analyzeTimeout)
	}
}

func TestPicLabelsHappyPath(t *testing.T) {
	analysis := engine.Analysis{Labels: engine.Labels{
		Drawings: 0.125,
		Hentai:   0.25,
		Neutral:  0.5,
		Porn:     0.75,
		Sexy:     1,
	}}
	checker := func(context.Context, []byte) (bool, error) { return true, nil }
	app := testApp(t, &fakeAnalyzer{analysis: analysis}, checker)

	status, body := postPic(t, app, "/pic/labels", base64.StdEncoding.EncodeToString([]byte("img")), "picture.jpg")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", status, body)
	}
	want := map[string]any{
		"status":   "ok",
		"message":  "success",
		"drawings": 0.125,
		"hentai":   0.25,
		"neutral":  0.5,
		"porn":     0.75,
		"sexy":     1.0,
		"nsfwText": true,
	}
	if got := decodeJSON(t, body); !reflect.DeepEqual(got, want) {
		t.Errorf("body = %v, want %v", got, want)
	}
}

func TestPicAnalyzeRichResponse(t *testing.T) {
	analysis := engine.Analysis{
		Verdict: engine.VerdictExplicit,
		NSFW:    0.75,
		Labels: engine.Labels{
			Drawings: 0.125,
			Hentai:   0.25,
			Neutral:  0.5,
			Porn:     0.75,
			Sexy:     1,
		},
		Photo: &engine.ClassifierOutput{
			Model:  "photo-model",
			Scores: map[string]float32{"high": 0.75, "low": 0.5},
		},
		Anime: &engine.ClassifierOutput{
			Model:  "anime-model",
			Scores: map[string]float32{"r18": 0.25},
		},
		Detections: []engine.Detection{{
			Label: "EXPOSED",
			Score: 0.5,
			Box:   engine.Box{X: 1, Y: 2, W: 3, H: 4},
		}},
		Models:    []engine.ModelInfo{{ID: "photo-model", Kind: "classifier", SHA256: "abc123"}},
		ElapsedMs: 42,
	}
	checker := func(context.Context, []byte) (bool, error) { return false, nil }
	app := testApp(t, &fakeAnalyzer{analysis: analysis}, checker)

	status, body := postPic(t, app, "/pic/analyze", base64.StdEncoding.EncodeToString([]byte("img")), "")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", status, body)
	}
	fields := decodeJSON(t, body)

	wantKeys := []string{
		"status",
		"message",
		"nsfwText",
		"verdict",
		"nsfw",
		"labels",
		"photo",
		"anime",
		"detections",
		"models",
		"elapsedMs",
	}
	if len(fields) != len(wantKeys) {
		t.Errorf("response has %d fields, want %d: %v", len(fields), len(wantKeys), fields)
	}
	if fields["status"] != "ok" || fields["message"] != "success" {
		t.Errorf("envelope = %v/%v, want ok/success", fields["status"], fields["message"])
	}
	if fields["nsfwText"] != false {
		t.Errorf("nsfwText = %v, want false", fields["nsfwText"])
	}
	if fields["verdict"] != engine.VerdictExplicit {
		t.Errorf("verdict = %v, want %s", fields["verdict"], engine.VerdictExplicit)
	}
	if fields["nsfw"] != 0.75 {
		t.Errorf("nsfw = %v, want 0.75", fields["nsfw"])
	}
	if fields["elapsedMs"] != float64(42) {
		t.Errorf("elapsedMs = %v, want 42", fields["elapsedMs"])
	}

	wantLabels := map[string]any{"drawings": 0.125, "hentai": 0.25, "neutral": 0.5, "porn": 0.75, "sexy": 1.0}
	if labels, _ := fields["labels"].(map[string]any); !reflect.DeepEqual(labels, wantLabels) {
		t.Errorf("labels = %v, want %v", fields["labels"], wantLabels)
	}

	photo, _ := fields["photo"].(map[string]any)
	if photo["model"] != "photo-model" {
		t.Errorf("photo.model = %v, want photo-model", photo["model"])
	}
	scores, _ := photo["scores"].(map[string]any)
	if scores["high"] != 0.75 || scores["low"] != 0.5 {
		t.Errorf("photo.scores = %v, want high 0.75, low 0.5", photo["scores"])
	}

	anime, _ := fields["anime"].(map[string]any)
	if anime["model"] != "anime-model" {
		t.Errorf("anime.model = %v, want anime-model", anime["model"])
	}

	detections, ok := fields["detections"].([]any)
	if !ok || len(detections) != 1 {
		t.Fatalf("detections = %#v, want one-element array", fields["detections"])
	}
	detection, _ := detections[0].(map[string]any)
	if detection["label"] != "EXPOSED" || detection["score"] != 0.5 {
		t.Errorf("detection = %v, want EXPOSED/0.5", detection)
	}
	box, _ := detection["box"].(map[string]any)
	wantBox := map[string]any{"x": 1.0, "y": 2.0, "w": 3.0, "h": 4.0}
	if !reflect.DeepEqual(box, wantBox) {
		t.Errorf("box = %v, want %v", box, wantBox)
	}

	models, ok := fields["models"].([]any)
	if !ok || len(models) != 1 {
		t.Fatalf("models = %#v, want one-element array", fields["models"])
	}
	model, _ := models[0].(map[string]any)
	if model["id"] != "photo-model" || model["kind"] != "classifier" || model["sha256"] != "abc123" {
		t.Errorf("model = %v, want photo-model/classifier/abc123", model)
	}
}

func TestPicAnalyzeEmptyDetectionsStayArray(t *testing.T) {
	app := testApp(t, &fakeAnalyzer{}, nil)
	status, body := postPic(t, app, "/pic/analyze", base64.StdEncoding.EncodeToString([]byte("img")), "")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", status, body)
	}
	fields := decodeJSON(t, body)
	detections, ok := fields["detections"]
	if !ok {
		t.Fatalf("detections missing from body: %s", body)
	}
	arr, ok := detections.([]any)
	if !ok {
		t.Fatalf("detections = %#v, want array", detections)
	}
	if len(arr) != 0 {
		t.Errorf("detections = %v, want empty array", arr)
	}
}

func TestPicRequestErrors(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"invalid JSON", `{"base64":`},
		{"empty body", ``},
		{"array body", `[1,2,3]`},
		{"empty base64", `{"base64":""}`},
		{"invalid base64", `{"base64":"%%%%"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			analyzer := &fakeAnalyzer{}
			app := testApp(t, analyzer, nil)
			status, body := doRequest(t, app, http.MethodPost, "/pic/check", tt.body)
			if status != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body: %s", status, body)
			}
			assertLegacyError(t, body)
			if calls, _, _ := analyzer.snapshot(); calls != 0 {
				t.Errorf("Analyze calls = %d, want 0", calls)
			}
		})
	}
}

func TestPicEngineErrors(t *testing.T) {
	payload := base64.StdEncoding.EncodeToString([]byte("img"))
	tests := []struct {
		name       string
		err        error
		wantStatus int
	}{
		{
			"image too large",
			fmt.Errorf("engine: model %q: %w", "m", engine.ErrImageTooLarge),
			http.StatusRequestEntityTooLarge,
		},
		{
			"unsupported image",
			fmt.Errorf("engine: model %q: %w", "m", engine.ErrUnsupportedImage),
			http.StatusBadRequest,
		},
		{"deadline exceeded", fmt.Errorf("engine: %w", context.DeadlineExceeded), http.StatusGatewayTimeout},
		{"generic error", errors.New("boom"), http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := testApp(t, &fakeAnalyzer{err: tt.err}, nil)
			status, body := postPic(t, app, "/pic/check", payload, "")
			if status != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body: %s", status, tt.wantStatus, body)
			}
			assertLegacyError(t, body)
		})
	}

	t.Run("checker error", func(t *testing.T) {
		checker := func(context.Context, []byte) (bool, error) { return false, errors.New("ocr exploded") }
		app := testApp(t, &fakeAnalyzer{}, checker)
		status, body := postPic(t, app, "/pic/check", payload, "")
		if status != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500; body: %s", status, body)
		}
		assertLegacyError(t, body)
	})

	t.Run("labels and analyze share the mapping", func(t *testing.T) {
		for _, path := range []string{"/pic/labels", "/pic/analyze"} {
			app := testApp(t, &fakeAnalyzer{err: engine.ErrUnsupportedImage}, nil)
			status, body := postPic(t, app, path, payload, "")
			if status != http.StatusBadRequest {
				t.Fatalf("%s status = %d, want 400; body: %s", path, status, body)
			}
			assertLegacyError(t, body)
		}
	})
}

func TestWrongMethod(t *testing.T) {
	app := testApp(t, &fakeAnalyzer{}, nil)
	status, body := doRequest(t, app, http.MethodGet, "/pic/check", "")
	if status != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405; body: %s", status, body)
	}
}

func TestNewAppNilAnalyzerPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("NewApp(nil analyzer) did not panic")
		}
	}()
	NewApp(config.Config{MaxImageBytes: 1 << 20}, nil, nil, testLogger())
}

func TestBodyLimitFor(t *testing.T) {
	if got := bodyLimitFor(0); got != bodyLimitFloor {
		t.Errorf("bodyLimitFor(0) = %d, want floor %d", got, bodyLimitFloor)
	}
	if got := bodyLimitFor(1); got != bodyLimitFloor {
		t.Errorf("bodyLimitFor(1) = %d, want floor %d", got, bodyLimitFloor)
	}
	const maxImageBytes = int64(20 << 20)
	want := int(maxImageBytes/3*4 + 4 + bodyLimitOverhead)
	if got := bodyLimitFor(maxImageBytes); got != want {
		t.Errorf("bodyLimitFor(%d) = %d, want %d", maxImageBytes, got, want)
	}
	for _, huge := range []int64{math.MaxInt64, math.MaxInt32, int64(math.MaxInt32) + 1} {
		if got := bodyLimitFor(huge); got != math.MaxInt32 {
			t.Errorf("bodyLimitFor(%d) = %d, want max %d", huge, got, math.MaxInt32)
		}
	}
}

func TestRequestLoggerStatus(t *testing.T) {
	handler := &captureHandler{}
	app := NewApp(config.Config{MaxImageBytes: 1 << 20}, &fakeAnalyzer{}, nil, slog.New(handler))
	app.Get("/teapot", func(fiber.Ctx) error {
		return fiber.NewError(fiber.StatusTeapot, "short and stout")
	})

	status, body := doRequest(t, app, http.MethodGet, "/teapot", "")
	if status != fiber.StatusTeapot {
		t.Fatalf("status = %d, want 418; body: %s", status, body)
	}
	assertLegacyError(t, body)
	if msg := decodeJSON(t, body)["errorMessage"]; msg != "short and stout" {
		t.Errorf("errorMessage = %v, want preserved message", msg)
	}
	if statuses := handler.statuses(); !containsStatus(statuses, fiber.StatusTeapot) {
		t.Errorf("logged statuses = %v, want one 418", statuses)
	}
}

func TestRequestLoggerPanic(t *testing.T) {
	handler := &captureHandler{}
	app := NewApp(config.Config{MaxImageBytes: 1 << 20}, &fakeAnalyzer{}, nil, slog.New(handler))
	app.Get("/panic", func(fiber.Ctx) error {
		panic("kaboom")
	})

	status, body := doRequest(t, app, http.MethodGet, "/panic", "")
	if status != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body: %s", status, body)
	}
	assertLegacyError(t, body)
	if msg := decodeJSON(t, body)["errorMessage"]; msg != "Internal Server Error" {
		t.Errorf("errorMessage = %v, want generic message", msg)
	}
	if bytes.Contains(body, []byte("kaboom")) {
		t.Errorf("response leaked the panic value: %s", body)
	}
	if statuses := handler.statuses(); !containsStatus(statuses, http.StatusInternalServerError) {
		t.Errorf("logged statuses = %v, want one 500", statuses)
	}
}

func TestErrorHandlerHidesInternalErrors(t *testing.T) {
	app := NewApp(config.Config{MaxImageBytes: 1 << 20}, &fakeAnalyzer{}, nil, testLogger())
	app.Get("/secret", func(fiber.Ctx) error {
		return errors.New("db password hunter2")
	})

	status, body := doRequest(t, app, http.MethodGet, "/secret", "")
	if status != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body: %s", status, body)
	}
	assertLegacyError(t, body)
	if msg := decodeJSON(t, body)["errorMessage"]; msg != "Internal Server Error" {
		t.Errorf("errorMessage = %v, want generic message", msg)
	}
	if bytes.Contains(body, []byte("hunter2")) {
		t.Errorf("response leaked the internal error: %s", body)
	}
}

func TestRequestConcurrencyLimiter(t *testing.T) {
	override(t, &requestConcurrency, 1)

	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	analyzer := &fakeAnalyzer{fn: func(ctx context.Context, data []byte) (engine.Analysis, error) {
		entered <- struct{}{}
		<-release
		return engine.Analysis{}, nil
	}}
	app := testApp(t, analyzer, nil)
	payload := base64.StdEncoding.EncodeToString([]byte("img"))

	type result struct {
		status int
		body   []byte
		err    error
	}
	first := make(chan result, 1)
	go func() {
		status, body, err := send(app, newJSONRequest(http.MethodPost, "/pic/check", picBody(payload, "")))
		first <- result{status: status, body: body, err: err}
	}()

	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("first request did not reach the analyzer")
	}

	status, body := postPic(t, app, "/pic/check", payload, "")
	if status != http.StatusServiceUnavailable {
		t.Fatalf("second status = %d, want 503; body: %s", status, body)
	}
	assertLegacyError(t, body)

	close(release)
	select {
	case res := <-first:
		if res.err != nil {
			t.Fatalf("first request: %v", res.err)
		}
		if res.status != http.StatusOK {
			t.Fatalf("first status = %d, want 200; body: %s", res.status, res.body)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first request did not finish")
	}
}

func TestConcurrentRequestsBounded(t *testing.T) {
	override(t, &requestConcurrency, 2)

	var active, peak atomic.Int64
	analyzer := &fakeAnalyzer{fn: func(ctx context.Context, data []byte) (engine.Analysis, error) {
		current := active.Add(1)
		for {
			previous := peak.Load()
			if current <= previous || peak.CompareAndSwap(previous, current) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		active.Add(-1)
		return engine.Analysis{}, nil
	}}
	app := testApp(t, analyzer, nil)
	payload := base64.StdEncoding.EncodeToString([]byte("img"))

	const total = 6
	type result struct {
		status int
		err    error
	}
	results := make([]result, total)
	var wg sync.WaitGroup
	for i := range total {
		wg.Go(func() {
			status, _, err := send(app, newJSONRequest(http.MethodPost, "/pic/check", picBody(payload, "")))
			results[i] = result{status: status, err: err}
		})
	}
	wg.Wait()

	if p := peak.Load(); p > 2 {
		t.Errorf("peak concurrent analyses = %d, want <= 2", p)
	} else if p < 1 {
		t.Errorf("peak concurrent analyses = %d, want >= 1", p)
	}
	ok := 0
	for _, res := range results {
		if res.err != nil {
			t.Fatalf("request failed: %v", res.err)
		}
		switch res.status {
		case http.StatusOK:
			ok++
		case http.StatusServiceUnavailable:
		default:
			t.Fatalf("status = %d, want 200 or 503", res.status)
		}
	}
	if ok == 0 {
		t.Error("no request was admitted")
	}
}

func TestAnalyzeTimeoutEndToEnd(t *testing.T) {
	override(t, &analyzeTimeout, 30*time.Millisecond)

	analyzer := &fakeAnalyzer{fn: func(ctx context.Context, data []byte) (engine.Analysis, error) {
		<-ctx.Done()
		return engine.Analysis{}, ctx.Err()
	}}
	app := testApp(t, analyzer, nil)

	status, body := postPic(t, app, "/pic/check", base64.StdEncoding.EncodeToString([]byte("img")), "")
	if status != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want 504; body: %s", status, body)
	}
	assertLegacyError(t, body)
}

func TestOCRTimeoutEndToEnd(t *testing.T) {
	override(t, &ocrTimeout, 30*time.Millisecond)

	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	checker := func(ctx context.Context, img []byte) (bool, error) {
		entered <- struct{}{}
		<-release
		return false, nil
	}
	app := testApp(t, &fakeAnalyzer{}, checker)

	status, body := postPic(t, app, "/pic/check", base64.StdEncoding.EncodeToString([]byte("img")), "")
	if status != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body: %s", status, body)
	}
	assertLegacyError(t, body)
	<-entered
	close(release)
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	defer func() { _ = ln.Close() }()
	return ln.Addr().(*net.TCPAddr).Port
}

func waitForServer(t *testing.T, baseURL string) {
	t.Helper()
	client := &http.Client{Timeout: time.Second, Transport: &http.Transport{}}
	defer client.CloseIdleConnections()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.Get(baseURL + "/ping")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("server at %s did not answer /ping", baseURL)
}

func TestStartWebServerGracefulShutdown(t *testing.T) {
	cfg := config.Config{Port: freePort(t), MaxImageBytes: 1 << 20}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- StartWebServer(ctx, cfg, &fakeAnalyzer{}, nil, testLogger()) }()

	baseURL := fmt.Sprintf("http://127.0.0.1:%d", cfg.Port)
	waitForServer(t, baseURL)

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("StartWebServer() = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("StartWebServer did not return within 5s of cancellation")
	}

	if _, err := http.Get(baseURL + "/ping"); err == nil {
		t.Error("server still answers after graceful shutdown")
	}
}

func TestStartWebServerListenError(t *testing.T) {
	ln, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	defer func() { _ = ln.Close() }()
	port := ln.Addr().(*net.TCPAddr).Port

	err = StartWebServer(
		context.Background(),
		config.Config{Port: port, MaxImageBytes: 1 << 20},
		&fakeAnalyzer{},
		nil,
		testLogger(),
	)
	if err == nil {
		t.Fatal("StartWebServer() = nil, want listen error")
	}
}

func TestStartWebServerCancelBeforeServe(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- StartWebServer(ctx, config.Config{Port: 0, MaxImageBytes: 1 << 20}, &fakeAnalyzer{}, nil, testLogger())
	}()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("StartWebServer() = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("StartWebServer did not return for an already cancelled context")
	}
}

func TestStartWebServerCancelsInFlightRequests(t *testing.T) {
	entered := make(chan struct{}, 1)
	canceled := make(chan struct{}, 1)
	release := make(chan struct{})
	analyzer := &fakeAnalyzer{fn: func(ctx context.Context, data []byte) (engine.Analysis, error) {
		entered <- struct{}{}
		select {
		case <-ctx.Done():
			canceled <- struct{}{}
			return engine.Analysis{}, ctx.Err()
		case <-release:
			return engine.Analysis{}, nil
		}
	}}

	cfg := config.Config{Port: freePort(t), MaxImageBytes: 1 << 20}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serverErr := make(chan error, 1)
	go func() { serverErr <- StartWebServer(ctx, cfg, analyzer, nil, testLogger()) }()

	baseURL := fmt.Sprintf("http://127.0.0.1:%d", cfg.Port)
	waitForServer(t, baseURL)

	payload := base64.StdEncoding.EncodeToString([]byte("img"))
	clientErr := make(chan error, 1)
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{}}
	defer client.CloseIdleConnections()
	go func() {
		resp, err := client.Post(baseURL+"/pic/check", "application/json", strings.NewReader(picBody(payload, "")))
		if err != nil {
			clientErr <- err
			return
		}
		defer func() { _ = resp.Body.Close() }()
		_, _ = io.Copy(io.Discard, resp.Body)
		if resp.StatusCode != http.StatusInternalServerError {
			clientErr <- fmt.Errorf("status = %d, want 500", resp.StatusCode)
			return
		}
		clientErr <- nil
	}()

	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("request did not reach the analyzer")
	}

	cancel()

	select {
	case <-canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("in-flight Analyze did not observe cancellation")
	}
	if err := <-clientErr; err != nil {
		t.Errorf("client request: %v", err)
	}
	// Close pooled test connections before waiting for the server to drain them.
	client.CloseIdleConnections()
	select {
	case err := <-serverErr:
		if err != nil {
			t.Errorf("StartWebServer() = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("StartWebServer did not return")
	}
}

func TestStartWebServerShutdownDeadline(t *testing.T) {
	override(t, &shutdownTimeout, 50*time.Millisecond)

	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	analyzer := &fakeAnalyzer{fn: func(ctx context.Context, data []byte) (engine.Analysis, error) {
		entered <- struct{}{}
		<-release
		return engine.Analysis{}, nil
	}}

	cfg := config.Config{Port: freePort(t), MaxImageBytes: 1 << 20}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serverErr := make(chan error, 1)
	go func() { serverErr <- StartWebServer(ctx, cfg, analyzer, nil, testLogger()) }()

	baseURL := fmt.Sprintf("http://127.0.0.1:%d", cfg.Port)
	waitForServer(t, baseURL)

	payload := base64.StdEncoding.EncodeToString([]byte("img"))
	go func() {
		resp, err := http.Post(baseURL+"/pic/check", "application/json", strings.NewReader(picBody(payload, "")))
		if err == nil {
			_ = resp.Body.Close()
		}
	}()

	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("request did not reach the analyzer")
	}

	start := time.Now()
	cancel()
	var err error
	select {
	case err = <-serverErr:
	case <-time.After(5 * time.Second):
		t.Fatal("StartWebServer did not return")
	}
	close(release)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("StartWebServer() = %v, want context.DeadlineExceeded", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("shutdown took %s, want near the 50ms deadline", elapsed)
	}
}

func exactJSONBody(t *testing.T, size int) string {
	t.Helper()
	const (
		prefix = `{"base64":"`
		suffix = `"}`
	)
	b64len := size - len(prefix) - len(suffix)
	if b64len <= 0 {
		t.Fatalf("size %d too small", size)
	}
	for b64len%4 == 1 {
		b64len--
	}
	b64 := strings.Repeat("A", b64len)
	pad := size - len(prefix) - len(b64) - len(suffix)
	return prefix + b64 + suffix + strings.Repeat(" ", pad)
}

func TestBodyLimitBoundary(t *testing.T) {
	analyzer := &fakeAnalyzer{}
	baseURL := startWebServer(t, config.Config{Port: freePort(t), MaxImageBytes: 1}, analyzer, nil)
	limit := bodyLimitFor(1)

	atLimit := exactJSONBody(t, limit)
	resp, err := http.Post(baseURL+"/pic/check", "application/json", strings.NewReader(atLimit))
	if err != nil {
		t.Fatalf("post at limit: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status at limit = %d, want 200", resp.StatusCode)
	}
	if calls, _, _ := analyzer.snapshot(); calls != 1 {
		t.Errorf("Analyze calls at limit = %d, want 1", calls)
	}

	addr := strings.TrimPrefix(baseURL, "http://")
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := fmt.Fprintf(
		conn,
		"POST /pic/check HTTP/1.1\r\nHost: %s\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n",
		addr,
		limit+1,
	); err != nil {
		t.Fatalf("write request: %v", err)
	}
	oversized, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	defer func() { _ = oversized.Body.Close() }()
	if oversized.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status over limit = %d, want 413", oversized.StatusCode)
	}
	if calls, _, _ := analyzer.snapshot(); calls != 1 {
		t.Errorf("Analyze calls over limit = %d, want still 1", calls)
	}
}

func startWebServer(t *testing.T, cfg config.Config, analyzer Analyzer, checker TextChecker) string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- StartWebServer(ctx, cfg, analyzer, checker, testLogger()) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-errCh:
			if err != nil {
				t.Errorf("StartWebServer returned %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("StartWebServer did not return after cancellation")
		}
	})

	baseURL := fmt.Sprintf("http://127.0.0.1:%d", cfg.Port)
	waitForServer(t, baseURL)
	return baseURL
}
