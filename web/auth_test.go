package web

import (
	"encoding/base64"
	"net/http"
	"testing"
	"time"

	"nsfw_sherlock/config"

	"github.com/gofiber/fiber/v3"
)

const testAPIKey = "secret-key"

func TestAPIKeyGuard(t *testing.T) {
	image := []byte("fake-image-bytes")
	payload := base64.StdEncoding.EncodeToString(image)
	app := NewApp(
		config.Config{MaxImageBytes: 1 << 20, BatchMaxImages: 4, APIKeys: []string{testAPIKey}},
		&fakeAnalyzer{},
		nil,
		testLogger(),
	)

	status, body := postPic(t, app, "/pic/check", payload, "a.png")
	if status != http.StatusUnauthorized {
		t.Fatalf("no key status = %d, want 401; body: %s", status, body)
	}
	assertLegacyError(t, body)

	req := newJSONRequest(http.MethodPost, "/pic/check", picBody(payload, "a.png"))
	req.Header.Set("X-Api-Key", "wrong")
	status, body, err := send(app, req)
	if err != nil {
		t.Fatal(err)
	}
	if status != http.StatusUnauthorized {
		t.Fatalf("wrong key status = %d, want 401; body: %s", status, body)
	}

	req = newJSONRequest(http.MethodPost, "/pic/check", picBody(payload, "a.png"))
	req.Header.Set("X-Api-Key", testAPIKey)
	status, body, err = send(app, req)
	if err != nil {
		t.Fatal(err)
	}
	if status != http.StatusOK {
		t.Fatalf("valid key status = %d, want 200; body: %s", status, body)
	}

	openStatus, _, openErr := send(app, newJSONRequest(http.MethodGet, "/ping", ""))
	if openErr != nil || openStatus != http.StatusOK {
		t.Fatalf("/ping status = %d, err = %v, want 200", openStatus, openErr)
	}
	openStatus, _, openErr = send(app, newJSONRequest(http.MethodGet, "/metrics", ""))
	if openErr != nil || openStatus != http.StatusOK {
		t.Fatalf("/metrics status = %d, err = %v, want 200", openStatus, openErr)
	}
}

func TestAPIKeyGuardDisabled(t *testing.T) {
	payload := base64.StdEncoding.EncodeToString([]byte("fake-image-bytes"))
	app := testApp(t, &fakeAnalyzer{}, nil)
	if status, body := postPic(t, app, "/pic/check", payload, "a.png"); status != http.StatusOK {
		t.Fatalf("status = %d, want 200 without configured keys; body: %s", status, body)
	}
}

func TestRateLimiterAllowAndRollover(t *testing.T) {
	limiter := newRateLimiter(2, true)
	now := time.Now()
	for i := range 2 {
		if ok, _ := limiter.allow("k", now); !ok {
			t.Fatalf("request %d was limited, want allowed", i+1)
		}
	}
	if ok, retry := limiter.allow("k", now); ok || retry < 1 {
		t.Fatalf("third request = (%v, %d), want limited with a Retry-After", ok, retry)
	}
	if ok, _ := limiter.allow("k", now.Add(2*time.Minute)); !ok {
		t.Error("a new window must reset the counter")
	}
	if ok, _ := limiter.allow("other", now); !ok {
		t.Error("a different key must have its own bucket")
	}
}

func TestRateLimitMiddleware(t *testing.T) {
	image := []byte("fake-image-bytes")
	payload := base64.StdEncoding.EncodeToString(image)
	app := NewApp(
		config.Config{MaxImageBytes: 1 << 20, BatchMaxImages: 4, RateLimitRPM: 2},
		&fakeAnalyzer{},
		nil,
		testLogger(),
	)

	for i := range 2 {
		if status, body := postPic(t, app, "/pic/check", payload, "a.png"); status != http.StatusOK {
			t.Fatalf("request %d status = %d, want 200; body: %s", i+1, status, body)
		}
	}
	resp, err := app.Test(
		newJSONRequest(http.MethodPost, "/pic/check", picBody(payload, "a.png")),
		fiber.TestConfig{Timeout: 5 * time.Second},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("third request status = %d, want 429", resp.StatusCode)
	}
	if resp.Header.Get("Retry-After") == "" {
		t.Error("the 429 response lacks the Retry-After header")
	}
	if status, _, err := send(app, newJSONRequest(http.MethodGet, "/ping", "")); err != nil || status != http.StatusOK {
		t.Fatalf("/ping status = %d, err = %v, want 200 outside the limit", status, err)
	}
}

func TestRateLimitIgnoresKeysWhenAuthIsDisabled(t *testing.T) {
	image := []byte("fake-image-bytes")
	payload := base64.StdEncoding.EncodeToString(image)
	app := NewApp(
		config.Config{MaxImageBytes: 1 << 20, BatchMaxImages: 4, RateLimitRPM: 1},
		&fakeAnalyzer{},
		nil,
		testLogger(),
	)

	for i := range 2 {
		req := newJSONRequest(http.MethodPost, "/pic/check", picBody(payload, "a.png"))
		req.Header.Set("X-API-Key", "rotating-"+string(rune('a'+i)))
		status, body, err := send(app, req)
		if err != nil {
			t.Fatal(err)
		}
		want := http.StatusOK
		if i == 1 {
			want = http.StatusTooManyRequests
		}
		if status != want {
			t.Fatalf("request %d status = %d, want %d; body: %s", i+1, status, want, body)
		}
	}
}

func TestRateLimitUsesKeysWhenAuthIsEnabled(t *testing.T) {
	image := []byte("fake-image-bytes")
	payload := base64.StdEncoding.EncodeToString(image)
	app := NewApp(
		config.Config{
			MaxImageBytes:  1 << 20,
			BatchMaxImages: 4,
			RateLimitRPM:   1,
			APIKeys:        []string{"key-one", "key-two"},
		},
		&fakeAnalyzer{},
		nil,
		testLogger(),
	)

	for _, key := range []string{"key-one", "key-two"} {
		req := newJSONRequest(http.MethodPost, "/pic/check", picBody(payload, "a.png"))
		req.Header.Set("X-API-Key", key)
		status, body, err := send(app, req)
		if err != nil {
			t.Fatal(err)
		}
		if status != http.StatusOK {
			t.Fatalf("key %s status = %d, want 200; body: %s", key, status, body)
		}
	}
}
