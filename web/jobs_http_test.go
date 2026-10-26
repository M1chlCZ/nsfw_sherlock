package web

import (
	"bytes"
	"context"
	"encoding/base64"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"nsfw_sherlock/config"
	"nsfw_sherlock/engine"
	"nsfw_sherlock/internal/video"

	"github.com/gofiber/fiber/v3"
)

func multipartJobRequest(t *testing.T, data []byte) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "clip.mp4")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/jobs", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req
}

func waitForHTTPJob(t *testing.T, app *fiber.App, id string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		status, body := doRequest(t, app, http.MethodGet, "/jobs/"+id, "")
		if status != http.StatusOK {
			t.Fatalf("GET /jobs/%s = %d; body: %s", id, status, body)
		}
		fields := decodeJSON(t, body)
		job, ok := fields["job"].(map[string]any)
		if !ok {
			t.Fatalf("job = %v", fields["job"])
		}
		switch job["state"] {
		case "done", "failed", "canceled":
			return job
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("job %s did not finish", id)
	return nil
}

func jobVideoApp(analyzer Analyzer, extractor video.Extractor) *fiber.App {
	h := &handlers{
		analyzer:    analyzer,
		timeout:     time.Second,
		batchMax:    4,
		metrics:     newMetricsState(nil),
		video:       extractor,
		videoLimits: video.Limits{FPS: 2, MaxFrames: 8, MaxWidth: videoMaxWidth},
		jobs:        newJobManager(context.Background()),
	}
	app := fiber.New()
	app.Post("/jobs", h.submitJob)
	app.Get("/jobs/:id", h.getJob)
	app.Delete("/jobs/:id", h.cancelJob)
	return app
}

func testJobApp(t *testing.T, analyzer Analyzer) *fiber.App {
	t.Helper()
	return NewApp(
		config.Config{
			MaxImageBytes:  1 << 20,
			BatchMaxImages: 4,
			FFmpegPath:     "/nonexistent-ffmpeg",
		},
		analyzer,
		nil,
		testLogger(),
	)
}

func TestSubmitBatchJob(t *testing.T) {
	payload := base64.StdEncoding.EncodeToString([]byte("fake-image-bytes"))
	analyzer := &fakeAnalyzer{analysis: engine.Analysis{Labels: engine.Labels{Porn: 0.9}}}
	app := testJobApp(t, analyzer)
	request := `{"images":[{"base64":"` + payload + `"},{"base64":"` + payload + `"}]}`
	status, body := doRequest(t, app, http.MethodPost, "/jobs", request)
	if status != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body: %s", status, body)
	}
	fields := decodeJSON(t, body)
	job, ok := fields["job"].(map[string]any)
	if !ok {
		t.Fatalf("job = %v", fields["job"])
	}
	if job["state"] != "queued" || job["kind"] != "batch" {
		t.Fatalf("job = %v", job)
	}
	done := waitForHTTPJob(t, app, job["id"].(string))
	if done["state"] != "done" {
		t.Fatalf("job = %v", done)
	}
	results, ok := done["result"].([]any)
	if !ok || len(results) != 2 {
		t.Fatalf("result = %v", done["result"])
	}
	first, _ := results[0].(map[string]any)
	if first["nsfwPic"] != true || first["status"] != "ok" {
		t.Errorf("result[0] = %v", first)
	}
	if calls, _, _ := analyzer.snapshot(); calls != 2 {
		t.Errorf("Analyze calls = %d, want 2", calls)
	}
}

func TestSubmitVideoJob(t *testing.T) {
	extractor := &fakeExtractor{frames: []video.Frame{
		{Index: 1, TimeMs: 0, Data: []byte("explicit")},
	}}
	app := jobVideoApp(frameAnalyzer(), extractor)
	status, body, err := send(app, multipartJobRequest(t, []byte("video-bytes")))
	if err != nil {
		t.Fatal(err)
	}
	if status != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body: %s", status, body)
	}
	fields := decodeJSON(t, body)
	job := fields["job"].(map[string]any)
	if job["kind"] != "video" {
		t.Fatalf("job = %v", job)
	}
	done := waitForHTTPJob(t, app, job["id"].(string))
	if done["state"] != "done" {
		t.Fatalf("job = %v", done)
	}
	result, ok := done["result"].(map[string]any)
	if !ok {
		t.Fatalf("result = %v", done["result"])
	}
	summary, ok := result["summary"].(map[string]any)
	if !ok || summary["verdict"] != "explicit" {
		t.Errorf("summary = %v", result["summary"])
	}
}

func TestJobValidation(t *testing.T) {
	app := testJobApp(t, frameAnalyzer())
	if status, body := doRequest(t, app, http.MethodPost, "/jobs", `{}`); status != http.StatusBadRequest {
		t.Fatalf("empty batch status = %d; body: %s", status, body)
	}
	if status, body := doRequest(t, app, http.MethodGet, "/jobs/missing", ""); status != http.StatusNotFound {
		t.Fatalf("unknown job status = %d; body: %s", status, body)
	}
	if status, body := doRequest(t, app, http.MethodDelete, "/jobs/missing", ""); status != http.StatusNotFound {
		t.Fatalf("cancel unknown status = %d; body: %s", status, body)
	}
}

func TestCancelFinishedJobConflicts(t *testing.T) {
	payload := base64.StdEncoding.EncodeToString([]byte("fake-image-bytes"))
	app := testJobApp(t, &fakeAnalyzer{analysis: engine.Analysis{}})
	status, body := doRequest(t, app, http.MethodPost, "/jobs", `{"images":[{"base64":"`+payload+`"}]}`)
	if status != http.StatusAccepted {
		t.Fatalf("status = %d; body: %s", status, body)
	}
	job := decodeJSON(t, body)["job"].(map[string]any)
	waitForHTTPJob(t, app, job["id"].(string))
	status, body = doRequest(t, app, http.MethodDelete, "/jobs/"+job["id"].(string), "")
	if status != http.StatusConflict {
		t.Fatalf("cancel finished status = %d, want 409; body: %s", status, body)
	}
}

func TestSubmitVideoJobWithoutFFmpeg(t *testing.T) {
	h := &handlers{
		analyzer: frameAnalyzer(),
		timeout:  time.Second,
		batchMax: 4,
		metrics:  newMetricsState(nil),
		jobs:     newJobManager(context.Background()),
	}
	app := fiber.New()
	app.Post("/jobs", h.submitJob)
	status, body, err := send(app, multipartJobRequest(t, []byte("video-bytes")))
	if err != nil {
		t.Fatal(err)
	}
	if status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body: %s", status, body)
	}
}

func TestReadyReportsVideoAvailability(t *testing.T) {
	app := testJobApp(t, frameAnalyzer())
	status, body := doRequest(t, app, http.MethodGet, "/ready", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", status, body)
	}
	fields := decodeJSON(t, body)
	if fields["status"] != "ok" || fields["video"] != false {
		t.Errorf("body = %v", fields)
	}
}
