package web

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"nsfw_sherlock/engine"
	"nsfw_sherlock/internal/video"

	"github.com/gofiber/fiber/v3"
)

type fakeExtractor struct {
	frames []video.Frame
	err    error

	path   string
	limits video.Limits
}

func (f *fakeExtractor) Extract(
	_ context.Context,
	path string,
	limits video.Limits,
) ([]video.Frame, error) {
	f.path = path
	f.limits = limits
	return f.frames, f.err
}

func videoApp(analyzer Analyzer, extractor video.Extractor) *fiber.App {
	h := &handlers{
		analyzer:    analyzer,
		timeout:     time.Second,
		batchMax:    4,
		metrics:     newMetricsState(nil),
		video:       extractor,
		videoLimits: video.Limits{FPS: 2, MaxFrames: 8, MaxWidth: videoMaxWidth},
	}
	app := fiber.New()
	app.Post("/video/check", h.videoCheck)
	app.Post("/pic/upload", h.picUpload)
	return app
}

func multipartVideoRequest(
	t *testing.T,
	data []byte,
	fields map[string]string,
	withFile bool,
) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for key, value := range fields {
		if err := writer.WriteField(key, value); err != nil {
			t.Fatal(err)
		}
	}
	if withFile {
		part, err := writer.CreateFormFile("file", "clip.mp4")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/video/check", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req
}

func frameAnalyzer() *fakeAnalyzer {
	return &fakeAnalyzer{fn: func(_ context.Context, data []byte) (engine.Analysis, error) {
		switch string(data) {
		case "explicit":
			return engine.Analysis{Verdict: engine.VerdictExplicit, NSFW: 0.9}, nil
		case "suggestive":
			return engine.Analysis{Verdict: engine.VerdictSuggestive, NSFW: 0.6}, nil
		default:
			return engine.Analysis{Verdict: engine.VerdictSFW, NSFW: 0.1}, nil
		}
	}}
}

func TestVideoCheckSummarizesFrames(t *testing.T) {
	extractor := &fakeExtractor{frames: []video.Frame{
		{Index: 1, TimeMs: 0, Data: []byte("sfw")},
		{Index: 2, TimeMs: 500, Data: []byte("explicit")},
		{Index: 3, TimeMs: 1000, Data: []byte("suggestive")},
	}}
	app := videoApp(frameAnalyzer(), extractor)
	status, body, err := send(app, multipartVideoRequest(t, []byte("video-bytes"), nil, true))
	if err != nil {
		t.Fatal(err)
	}
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", status, body)
	}
	fields := decodeJSON(t, body)
	summary, ok := fields["summary"].(map[string]any)
	if !ok {
		t.Fatalf("summary = %v", fields["summary"])
	}
	if summary["verdict"] != "explicit" || summary["nsfw"] != 0.9 ||
		summary["framesAnalyzed"] != float64(3) ||
		summary["explicitFrames"] != float64(1) ||
		summary["suggestiveFrames"] != float64(1) {
		t.Errorf("summary = %v", summary)
	}
	if _, ok := summary["elapsedMs"].(float64); !ok {
		t.Errorf("elapsedMs = %v", summary["elapsedMs"])
	}
	frames, ok := fields["frames"].([]any)
	if !ok || len(frames) != 3 {
		t.Fatalf("frames = %v", fields["frames"])
	}
	second, _ := frames[1].(map[string]any)
	analysis, _ := second["analysis"].(map[string]any)
	if analysis["verdict"] != "explicit" {
		t.Errorf("frames[1] = %v", second)
	}
	if extractor.limits.FPS != 2 || extractor.limits.MaxFrames != 8 {
		t.Errorf("limits = %+v", extractor.limits)
	}
}

func TestVideoCheckPassesSensitivity(t *testing.T) {
	analyzer := frameAnalyzer()
	app := videoApp(analyzer, &fakeExtractor{frames: []video.Frame{{Index: 1, Data: []byte("sfw")}}})
	status, body, err := send(app, multipartVideoRequest(
		t,
		[]byte("video-bytes"),
		map[string]string{"sensitivity": "2"},
		true,
	))
	if err != nil {
		t.Fatal(err)
	}
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", status, body)
	}
	if options := analyzer.lastOptions(); options.Sensitivity != 2 {
		t.Errorf("sensitivity = %v, want 2", options.Sensitivity)
	}
}

func TestVideoCheckValidatesSensitivity(t *testing.T) {
	app := videoApp(frameAnalyzer(), &fakeExtractor{})
	status, body, err := send(app, multipartVideoRequest(
		t,
		[]byte("video-bytes"),
		map[string]string{"sensitivity": "0.05"},
		true,
	))
	if err != nil {
		t.Fatal(err)
	}
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", status, body)
	}
}

func TestVideoCheckRejectsOversizedUploads(t *testing.T) {
	h := &handlers{
		analyzer:      frameAnalyzer(),
		timeout:       time.Second,
		batchMax:      4,
		metrics:       newMetricsState(nil),
		video:         &fakeExtractor{},
		videoLimits:   video.Limits{FPS: 2, MaxFrames: 8, MaxWidth: videoMaxWidth},
		videoMaxBytes: 4,
	}
	app := fiber.New()
	app.Post("/video/check", h.videoCheck)
	status, body, err := send(app, multipartVideoRequest(t, []byte("video-bytes"), nil, true))
	if err != nil {
		t.Fatal(err)
	}
	if status != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413; body: %s", status, body)
	}
}

func TestVideoCheckRequiresAFile(t *testing.T) {
	app := videoApp(frameAnalyzer(), &fakeExtractor{})
	status, body, err := send(app, multipartVideoRequest(t, nil, nil, false))
	if err != nil {
		t.Fatal(err)
	}
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", status, body)
	}
	fields := decodeJSON(t, body)
	if fields["errorMessage"] != videoNoFile {
		t.Errorf("errorMessage = %v", fields["errorMessage"])
	}
}

func TestVideoCheckReportsMissingFFmpeg(t *testing.T) {
	app := videoApp(frameAnalyzer(), &fakeExtractor{err: video.ErrFFmpegMissing})
	status, body, err := send(app, multipartVideoRequest(t, []byte("video-bytes"), nil, true))
	if err != nil {
		t.Fatal(err)
	}
	if status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body: %s", status, body)
	}
}

func TestVideoCheckUsesTheAnalyzerError(t *testing.T) {
	analyzer := &fakeAnalyzer{err: engine.ErrUnsupportedImage}
	extractor := &fakeExtractor{frames: []video.Frame{{Index: 1, Data: []byte("frame")}}}
	app := videoApp(analyzer, extractor)
	status, body, err := send(app, multipartVideoRequest(t, []byte("video-bytes"), nil, true))
	if err != nil {
		t.Fatal(err)
	}
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", status, body)
	}
}

func TestPicUploadAnalyzesTheRawBody(t *testing.T) {
	analyzer := &fakeAnalyzer{analysis: engine.Analysis{Labels: engine.Labels{Porn: 0.9}}}
	app := videoApp(analyzer, &fakeExtractor{})
	req := httptest.NewRequest(
		http.MethodPost,
		"/pic/upload?sensitivity=2",
		bytes.NewReader([]byte("image")),
	)
	req.Header.Set("Content-Type", "image/png")
	status, body, err := send(app, req)
	if err != nil {
		t.Fatal(err)
	}
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", status, body)
	}
	fields := decodeJSON(t, body)
	if fields["nsfwPic"] != true || fields["status"] != "ok" {
		t.Errorf("body = %v", fields)
	}
	if options := analyzer.lastOptions(); options.Sensitivity != 2 {
		t.Errorf("sensitivity = %v, want 2", options.Sensitivity)
	}
}

func TestPicUploadRejectsAnEmptyBody(t *testing.T) {
	app := videoApp(frameAnalyzer(), &fakeExtractor{})
	req := httptest.NewRequest(http.MethodPost, "/pic/upload", nil)
	status, body, err := send(app, req)
	if err != nil {
		t.Fatal(err)
	}
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", status, body)
	}
}

func TestSensitivityFromString(t *testing.T) {
	if value, ok := sensitivityFromString(""); !ok || value != 0 {
		t.Errorf("empty = %v, %v", value, ok)
	}
	if value, ok := sensitivityFromString("2.5"); !ok || value != 2.5 {
		t.Errorf("2.5 = %v, %v", value, ok)
	}
	if _, ok := sensitivityFromString("0.05"); ok {
		t.Errorf("0.05 accepted")
	}
	if _, ok := sensitivityFromString("fast"); ok {
		t.Errorf("text accepted")
	}
	if _, ok := sensitivityFromString("NaN"); ok {
		t.Errorf("NaN accepted")
	}
	if _, ok := sensitivityFromString("Inf"); ok {
		t.Errorf("Inf accepted")
	}
}

func TestSummarizeVideoDefaultsToSFW(t *testing.T) {
	summary := summarizeVideo([]videoFrame{
		{Analysis: engine.Analysis{Verdict: engine.VerdictSFW, NSFW: 0.2}},
		{Analysis: engine.Analysis{Verdict: engine.VerdictSFW, NSFW: 0.3}},
	}, time.Millisecond)
	if summary.Verdict != engine.VerdictSFW || summary.NSFW != 0.3 ||
		summary.FramesAnalyzed != 2 || summary.ExplicitFrames != 0 {
		t.Fatalf("summary = %+v", summary)
	}
}
