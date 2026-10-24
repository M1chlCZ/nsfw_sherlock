package web

import (
	"context"
	"encoding/base64"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"nsfw_sherlock/config"
	"nsfw_sherlock/engine"

	"github.com/gofiber/fiber/v3"
)

func TestMetricsRenderOnFreshState(t *testing.T) {
	state := newMetricsState(nil)
	out := state.render()
	for _, want := range []string{
		"# TYPE nsfw_requests_total counter",
		"# TYPE nsfw_request_duration_seconds histogram",
		`nsfw_request_duration_seconds_bucket{le="+Inf"} 0`,
		"nsfw_request_duration_seconds_count 0",
		"nsfw_inflight_requests 0",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("render() lacks %q:\n%s", want, out)
		}
	}
}

func TestMetricsObserveRequestsAndVerdicts(t *testing.T) {
	image := []byte("fake-image-bytes")
	payload := base64.StdEncoding.EncodeToString(image)
	analyzer := &fakeAnalyzer{analysis: engine.Analysis{Verdict: engine.VerdictSFW}}
	app := NewApp(
		config.Config{MaxImageBytes: 1 << 20, BatchMaxImages: 4, CacheEntries: 4},
		analyzer,
		func(context.Context, []byte) (bool, error) { return false, nil },
		testLogger(),
	)

	if status, body := postPic(t, app, "/pic/check", payload, "a.png"); status != http.StatusOK {
		t.Fatalf("/pic/check status = %d, want 200; body: %s", status, body)
	}
	invalidStatus, _ := doRequest(t, app, http.MethodPost, "/pic/check", `{"base64":"%%%%"}`)
	if invalidStatus != http.StatusBadRequest {
		t.Fatalf("invalid request status = %d, want 400", invalidStatus)
	}

	status, body, err := send(app, newJSONRequest(http.MethodGet, "/metrics", ""))
	if err != nil {
		t.Fatal(err)
	}
	if status != http.StatusOK {
		t.Fatalf("/metrics status = %d, want 200", status)
	}
	out := string(body)
	for _, want := range []string{
		`nsfw_requests_total{endpoint="/pic/check",status="200"} 1`,
		`nsfw_requests_total{endpoint="/pic/check",status="400"} 1`,
		`nsfw_verdicts_total{verdict="sfw"} 1`,
		"nsfw_request_duration_seconds_count 2",
		"nsfw_cache_entries 1",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("metrics lack %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, `endpoint="/metrics"`) {
		t.Error("the metrics endpoint must not count itself")
	}
}

func TestRequestIDHeaderAndLog(t *testing.T) {
	handler := &captureHandler{}
	logger := slog.New(handler)
	app := NewApp(config.Config{MaxImageBytes: 1 << 20, BatchMaxImages: 1}, &fakeAnalyzer{}, nil, logger)

	resp, err := app.Test(newJSONRequest(http.MethodGet, "/ping", ""), fiber.TestConfig{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.Header.Get("X-Request-ID") == "" {
		t.Error("response lacks the X-Request-ID header")
	}

	handler.mu.Lock()
	records := append([]slog.Record(nil), handler.records...)
	handler.mu.Unlock()
	found := false
	for _, record := range records {
		record.Attrs(func(attr slog.Attr) bool {
			if attr.Key == "requestId" && attr.Value.String() != "" {
				found = true
			}
			return true
		})
	}
	if !found {
		t.Error("log records lack the requestId attribute")
	}
}
