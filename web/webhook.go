package web

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"nsfw_sherlock/config"
)

const (
	webhookEventFinished   = "job.finished"
	webhookSignatureHeader = "X-Nsfw-Signature"
	webhookTimestampHeader = "X-Nsfw-Timestamp"
	webhookEventHeader     = "X-Nsfw-Event"
	webhookJobHeader       = "X-Nsfw-Job-Id"
	webhookSignaturePrefix = "sha256="
	webhookValidateTimeout = 3 * time.Second
	webhookDefaultTimeout  = 10 * time.Second
	maxCallbackURLLength   = 2048
	webhookConcurrency     = 8
)

var (
	errWebhookPrivate = errors.New(
		"callbackUrl points to a private address; set WEBHOOK_ALLOW_PRIVATE=true to allow it",
	)
	errWebhookScheme = errors.New("callbackUrl must use http or https")

	webhookBackoff = []time.Duration{2 * time.Second, 10 * time.Second, 30 * time.Second}
)

// webhookPayload is the JSON body sent to a callback URL when a job ends.
type webhookPayload struct {
	Event string  `json:"event"`
	Job   jobView `json:"job"`
}

// webhookSender validates callback URLs and delivers finished jobs to them.
type webhookSender struct {
	client    *http.Client
	secret    string
	attempts  int
	privateOK bool
	log       *slog.Logger
	delivered atomic.Int64
	failed    atomic.Int64
	backoff   []time.Duration
	slots     chan struct{}
}

func newWebhookSender(cfg config.Config, log *slog.Logger) *webhookSender {
	if log == nil {
		log = slog.Default()
	}
	timeout := time.Duration(cfg.WebhookTimeout) * time.Second
	if timeout <= 0 {
		timeout = webhookDefaultTimeout
	}
	attempts := max(cfg.WebhookRetries+1, 1)
	return &webhookSender{
		client: &http.Client{
			Timeout: timeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		secret:    cfg.WebhookSecret,
		attempts:  attempts,
		privateOK: cfg.WebhookPrivate,
		log:       log,
		backoff:   webhookBackoff,
		slots:     make(chan struct{}, webhookConcurrency),
	}
}

// validate reports whether rawURL is an acceptable callback target.
func (s *webhookSender) validate(ctx context.Context, rawURL string) error {
	if len(rawURL) > maxCallbackURLLength {
		return errors.New("callbackUrl is too long")
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("callbackUrl is not a valid URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errWebhookScheme
	}
	if parsed.Host == "" || parsed.Hostname() == "" {
		return errors.New("callbackUrl must include a host")
	}
	if parsed.User != nil {
		return errors.New("callbackUrl must not include credentials")
	}
	if s.privateOK {
		return nil
	}
	lookupCtx, cancel := context.WithTimeout(ctx, webhookValidateTimeout)
	defer cancel()
	addresses, err := net.DefaultResolver.LookupIPAddr(lookupCtx, parsed.Hostname())
	if err != nil {
		return fmt.Errorf("callbackUrl host cannot be resolved: %w", err)
	}
	if len(addresses) == 0 {
		return errors.New("callbackUrl host has no addresses")
	}
	for _, address := range addresses {
		if isPrivateAddress(address.IP) {
			return errWebhookPrivate
		}
	}
	return nil
}

func isPrivateAddress(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() {
		return true
	}
	ipv4 := ip.To4()
	if ipv4 == nil {
		return false
	}
	return ipv4[0] == 100 && ipv4[1] >= 64 && ipv4[1] <= 127
}

// deliver posts the finished job to callbackURL, retrying failed attempts.
func (s *webhookSender) deliver(ctx context.Context, callbackURL string, view jobView) {
	select {
	case s.slots <- struct{}{}:
	case <-ctx.Done():
		return
	}
	defer func() { <-s.slots }()
	body, err := json.Marshal(webhookPayload{Event: webhookEventFinished, Job: view})
	if err != nil {
		s.failed.Add(1)
		s.log.ErrorContext(ctx, "webhook: cannot marshal payload", "job", view.ID, "error", err)
		return
	}
	for attempt := range s.attempts {
		if attempt > 0 {
			var delay time.Duration
			if len(s.backoff) > 0 {
				delay = s.backoff[min(attempt-1, len(s.backoff)-1)]
			}
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
		if err := s.validate(ctx, callbackURL); err != nil {
			s.failed.Add(1)
			s.log.WarnContext(ctx, "webhook: invalid callback", "job", view.ID, "error", err)
			return
		}
		if err := s.post(ctx, callbackURL, body, view); err != nil {
			s.log.WarnContext(
				ctx,
				"webhook: delivery failed",
				"job", view.ID,
				"attempt", attempt+1,
				"error", err,
			)
			continue
		}
		s.delivered.Add(1)
		return
	}
	s.failed.Add(1)
	s.log.ErrorContext(ctx, "webhook: delivery gave up", "job", view.ID)
}

func (s *webhookSender) post(
	ctx context.Context,
	callbackURL string,
	body []byte,
	view jobView,
) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, callbackURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(webhookEventHeader, webhookEventFinished)
	req.Header.Set(webhookJobHeader, view.ID)
	if s.secret != "" {
		timestamp := strconv.FormatInt(time.Now().Unix(), 10)
		req.Header.Set(webhookTimestampHeader, timestamp)
		mac := hmac.New(sha256.New, []byte(s.secret))
		mac.Write([]byte(timestamp))
		mac.Write([]byte("."))
		mac.Write(body)
		req.Header.Set(webhookSignatureHeader, webhookSignaturePrefix+hex.EncodeToString(mac.Sum(nil)))
	}
	response, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4<<10))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("callback answered %d", response.StatusCode)
	}
	return nil
}

// trimCallback normalizes a callback URL from a request.
func trimCallback(raw string) string {
	return strings.TrimSpace(raw)
}
