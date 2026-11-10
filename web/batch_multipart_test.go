package web

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"nsfw_sherlock/config"
	"nsfw_sherlock/engine"
)

func multipartBatchRequest(t *testing.T, files [][]byte, sensitivity string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for i := range files {
		part, err := writer.CreateFormFile("file", fmt.Sprintf("image-%d.jpg", i))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(files[i]); err != nil {
			t.Fatal(err)
		}
	}
	if sensitivity != "" {
		if err := writer.WriteField("sensitivity", sensitivity); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/pic/batch", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req
}

func TestBatchMultipart(t *testing.T) {
	analyzer := &fakeAnalyzer{analysis: engine.Analysis{Verdict: engine.VerdictSFW}}
	app := NewApp(
		config.Config{MaxImageBytes: 1 << 20, BatchMaxImages: 4, CacheEntries: 4},
		analyzer,
		nil,
		testLogger(),
	)

	status, body, err := send(app, multipartBatchRequest(t, [][]byte{[]byte("one"), []byte("two")}, ""))
	if err != nil {
		t.Fatal(err)
	}
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", status, body)
	}
	fields := decodeJSON(t, body)
	results, ok := fields["results"].([]any)
	if !ok || len(results) != 2 {
		t.Fatalf("results = %v, want two results", fields["results"])
	}
	for i, result := range results {
		entry, _ := result.(map[string]any)
		if entry["status"] != "ok" {
			t.Errorf("result %d = %v, want an ok result", i, entry)
		}
	}
	if calls, _, _ := analyzer.snapshot(); calls != 2 {
		t.Fatalf("analyzer calls = %d, want 2", calls)
	}

	payload := base64.StdEncoding.EncodeToString([]byte("one"))
	if status, body := postPic(t, app, "/pic/check", payload, "one.jpg"); status != http.StatusOK {
		t.Fatalf("cached /pic/check status = %d, want 200; body: %s", status, body)
	}
	if calls, _, _ := analyzer.snapshot(); calls != 2 {
		t.Errorf("analyzer calls after a cached request = %d, want 2", calls)
	}
}

func TestBatchMultipartLimits(t *testing.T) {
	app := NewApp(
		config.Config{MaxImageBytes: 1 << 20, BatchMaxImages: 2},
		&fakeAnalyzer{},
		nil,
		testLogger(),
	)

	status, body, err := send(app, multipartBatchRequest(t, [][]byte{[]byte("a"), []byte("b"), []byte("c")}, ""))
	if err != nil {
		t.Fatal(err)
	}
	if status != http.StatusBadRequest {
		t.Fatalf("too many files status = %d, want 400; body: %s", status, body)
	}

	status, body, err = send(app, multipartBatchRequest(t, nil, ""))
	if err != nil {
		t.Fatal(err)
	}
	if status != http.StatusBadRequest {
		t.Fatalf("empty batch status = %d, want 400; body: %s", status, body)
	}

	status, body, err = send(app, multipartBatchRequest(t, [][]byte{[]byte("a")}, "0.01"))
	if err != nil {
		t.Fatal(err)
	}
	if status != http.StatusBadRequest {
		t.Fatalf("invalid sensitivity status = %d, want 400; body: %s", status, body)
	}
}

func TestBatchMultipartRejectsOversizedImage(t *testing.T) {
	analyzer := &fakeAnalyzer{}
	app := NewApp(
		config.Config{MaxImageBytes: 4, BatchMaxImages: 2},
		analyzer,
		nil,
		testLogger(),
	)

	status, body, err := send(app, multipartBatchRequest(t, [][]byte{[]byte("too-large")}, ""))
	if err != nil {
		t.Fatal(err)
	}
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", status, body)
	}
	fields := decodeJSON(t, body)
	results, _ := fields["results"].([]any)
	if len(results) != 1 {
		t.Fatalf("results = %v, want one result", fields["results"])
	}
	entry, _ := results[0].(map[string]any)
	if entry["hasError"] != true {
		t.Errorf("result = %v, want an error for the oversized image", entry)
	}
	if calls, _, _ := analyzer.snapshot(); calls != 0 {
		t.Errorf("analyzer calls = %d, want 0 for an oversized image", calls)
	}
}
