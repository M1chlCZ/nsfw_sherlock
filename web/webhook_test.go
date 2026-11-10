package web

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"nsfw_sherlock/config"
)

func TestIsPrivateAddress(t *testing.T) {
	for _, raw := range []string{
		"127.0.0.1",
		"10.1.2.3",
		"172.16.5.4",
		"192.168.0.9",
		"169.254.1.1",
		"100.64.0.1",
		"0.0.0.0",
		"::1",
		"fc00::1",
		"fe80::1",
	} {
		if !isPrivateAddress(net.ParseIP(raw)) {
			t.Errorf("%s is private, want blocked", raw)
		}
	}
	for _, raw := range []string{"8.8.8.8", "1.1.1.1", "2001:4860:4860::8888"} {
		if isPrivateAddress(net.ParseIP(raw)) {
			t.Errorf("%s is public, want allowed", raw)
		}
	}
}

func TestWebhookValidate(t *testing.T) {
	sender := newWebhookSender(config.Config{}, testLogger())
	for _, raw := range []string{
		"ftp://example.com/hook",
		"https://user:pass@example.com/hook",
		"https:///hook",
		"://broken",
	} {
		if err := sender.validate(t.Context(), raw); err == nil {
			t.Errorf("validate(%q) accepted an invalid URL", raw)
		}
	}
	if err := sender.validate(t.Context(), "http://127.0.0.1:9000/hook"); err == nil {
		t.Error("validate() accepted a loopback address")
	}
	if err := sender.validate(t.Context(), "http://8.8.8.8/hook"); err != nil {
		t.Errorf("validate() rejected a public address: %v", err)
	}

	sender = newWebhookSender(config.Config{WebhookPrivate: true}, testLogger())
	if err := sender.validate(t.Context(), "http://127.0.0.1:9000/hook"); err != nil {
		t.Errorf("validate() rejected a loopback address with WEBHOOK_ALLOW_PRIVATE: %v", err)
	}
}

func TestWebhookDeliverySignsPayload(t *testing.T) {
	type received struct {
		body      []byte
		event     string
		job       string
		timestamp string
		signature string
	}
	calls := make(chan received, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		calls <- received{
			body:      body,
			event:     r.Header.Get(webhookEventHeader),
			job:       r.Header.Get(webhookJobHeader),
			timestamp: r.Header.Get(webhookTimestampHeader),
			signature: r.Header.Get(webhookSignatureHeader),
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	sender := newWebhookSender(config.Config{
		WebhookSecret:  "top-secret",
		WebhookPrivate: true,
	}, testLogger())
	view := jobView{ID: "job-1", Kind: "batch", State: jobDone}
	sender.deliver(t.Context(), server.URL, view)

	got := <-calls
	if got.event != webhookEventFinished {
		t.Errorf("event = %q, want %q", got.event, webhookEventFinished)
	}
	if got.job != "job-1" {
		t.Errorf("job header = %q, want job-1", got.job)
	}
	mac := hmac.New(sha256.New, []byte("top-secret"))
	mac.Write([]byte(got.timestamp))
	mac.Write([]byte("."))
	mac.Write(got.body)
	want := webhookSignaturePrefix + hex.EncodeToString(mac.Sum(nil))
	if got.signature != want {
		t.Errorf("signature = %q, want %q", got.signature, want)
	}
	var payload webhookPayload
	if err := json.Unmarshal(got.body, &payload); err != nil {
		t.Fatalf("payload is not valid JSON: %v", err)
	}
	if payload.Event != webhookEventFinished || payload.Job.ID != "job-1" {
		t.Errorf("payload = %+v, want the finished job", payload)
	}
	if sender.delivered.Load() != 1 || sender.failed.Load() != 0 {
		t.Errorf(
			"counters = %d delivered, %d failed, want 1/0",
			sender.delivered.Load(),
			sender.failed.Load(),
		)
	}
}

func TestWebhookDeliveryRetries(t *testing.T) {
	override(t, &webhookBackoff, []time.Duration{time.Millisecond})
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	sender := newWebhookSender(config.Config{
		WebhookPrivate: true,
		WebhookRetries: 2,
		WebhookTimeout: 1,
	}, testLogger())
	sender.deliver(t.Context(), server.URL, jobView{ID: "job-2", State: jobFailed})

	if calls.Load() != 3 {
		t.Errorf("callback calls = %d, want 3", calls.Load())
	}
	if sender.delivered.Load() != 1 || sender.failed.Load() != 0 {
		t.Errorf(
			"counters = %d delivered, %d failed, want 1/0",
			sender.delivered.Load(),
			sender.failed.Load(),
		)
	}
}

func TestWebhookDeliveryGivesUp(t *testing.T) {
	override(t, &webhookBackoff, []time.Duration{time.Millisecond})
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()

	sender := newWebhookSender(config.Config{
		WebhookPrivate: true,
		WebhookRetries: 0,
		WebhookTimeout: 1,
	}, testLogger())
	sender.deliver(t.Context(), server.URL, jobView{ID: "job-3", State: jobDone})

	if calls.Load() != 1 {
		t.Errorf("callback calls = %d, want 1", calls.Load())
	}
	if sender.failed.Load() != 1 || sender.delivered.Load() != 0 {
		t.Errorf(
			"counters = %d delivered, %d failed, want 0/1",
			sender.delivered.Load(),
			sender.failed.Load(),
		)
	}
}

func TestWebhookFromBatchJob(t *testing.T) {
	calls := make(chan webhookPayload, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload webhookPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("callback payload: %v", err)
		}
		calls <- payload
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	app := NewApp(
		config.Config{
			MaxImageBytes:  1 << 20,
			BatchMaxImages: 4,
			WebhookPrivate: true,
			WebhookTimeout: 1,
		},
		&fakeAnalyzer{},
		nil,
		testLogger(),
	)
	body := `{"images":[{"base64":"ZmFrZS1pbWFnZQ==","filename":"a.png"}],"callbackUrl":"` + server.URL + `"}`
	status, response := doRequest(t, app, http.MethodPost, "/jobs", body)
	if status != http.StatusAccepted {
		t.Fatalf("POST /jobs status = %d, want 202; body: %s", status, response)
	}

	select {
	case payload := <-calls:
		if payload.Event != webhookEventFinished {
			t.Errorf("event = %q, want %q", payload.Event, webhookEventFinished)
		}
		if payload.Job.State != jobDone {
			t.Errorf("job state = %q, want %q", payload.Job.State, jobDone)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the callback was not delivered")
	}
}

func TestWebhookRejectsPrivateCallback(t *testing.T) {
	app := NewApp(config.Config{MaxImageBytes: 1 << 20, BatchMaxImages: 4}, &fakeAnalyzer{}, nil, testLogger())
	body := `{"images":[{"base64":"ZmFrZS1pbWFnZQ=="}],"callbackUrl":"http://127.0.0.1:9/hook"}`
	status, response := doRequest(t, app, http.MethodPost, "/jobs", body)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", status, response)
	}
	if !strings.Contains(string(response), "WEBHOOK_ALLOW_PRIVATE") {
		t.Errorf("response does not explain the private-address rule: %s", response)
	}
}
