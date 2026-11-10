package web

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"sync"
	"time"

	"nsfw_sherlock/utils"

	"github.com/gofiber/fiber/v3"
)

const (
	authHeaderName           = "X-API-Key"
	rateLimitWindow          = time.Minute
	maxRateBuckets           = 10_000
	rateLimitLimitHeader     = "X-Ratelimit-Limit"
	rateLimitRemainingHeader = "X-Ratelimit-Remaining"
	rateLimitResetHeader     = "X-Ratelimit-Reset"
)

func authExempt(c fiber.Ctx) bool {
	return c.Method() == fiber.MethodOptions || c.Path() == "/ping" || c.Path() == metricsPath
}

func apiKeyGuard(keys []string) fiber.Handler {
	if len(keys) == 0 {
		return nil
	}
	allowed := make(map[[sha256.Size]byte]struct{}, len(keys))
	for _, key := range keys {
		allowed[sha256.Sum256([]byte(key))] = struct{}{}
	}
	return func(c fiber.Ctx) error {
		if authExempt(c) {
			return c.Next()
		}
		key := c.Get(authHeaderName)
		if key == "" {
			return utils.ReportError(c, "missing API key", fiber.StatusUnauthorized)
		}
		if _, ok := allowed[sha256.Sum256([]byte(key))]; !ok {
			return utils.ReportError(c, "invalid API key", fiber.StatusUnauthorized)
		}
		return c.Next()
	}
}

type rateLimiter struct {
	mu         sync.Mutex
	limit      int
	window     time.Duration
	apiKeyAuth bool
	buckets    map[string]*rateBucket
}

type rateBucket struct {
	start time.Time
	count int
}

// rateDecision is the outcome of one rate-limit check.
type rateDecision struct {
	allowed   bool
	limit     int
	remaining int
	reset     time.Time
	retry     int
}

func newRateLimiter(limit int, apiKeyAuth bool) *rateLimiter {
	return &rateLimiter{
		limit:      limit,
		window:     rateLimitWindow,
		apiKeyAuth: apiKeyAuth,
		buckets:    make(map[string]*rateBucket),
	}
}

func (l *rateLimiter) allow(key string, now time.Time) (bool, int) {
	decision := l.check(key, now)
	return decision.allowed, decision.retry
}

// check records one request and reports the current window state.
func (l *rateLimiter) check(key string, now time.Time) rateDecision {
	l.mu.Lock()
	defer l.mu.Unlock()
	bucket, ok := l.buckets[key]
	if !ok || now.Sub(bucket.start) >= l.window {
		if len(l.buckets) >= maxRateBuckets {
			l.prune(now)
		}
		bucket = &rateBucket{start: now}
		l.buckets[key] = bucket
	}
	bucket.count++
	decision := rateDecision{limit: l.limit, reset: bucket.start.Add(l.window)}
	if bucket.count > l.limit {
		decision.retry = int(decision.reset.Sub(now).Seconds()) + 1
		return decision
	}
	decision.allowed = true
	decision.remaining = l.limit - bucket.count
	return decision
}

func (l *rateLimiter) prune(now time.Time) {
	for key, bucket := range l.buckets {
		if now.Sub(bucket.start) >= 2*l.window {
			delete(l.buckets, key)
		}
	}
}

func rateLimitMiddleware(l *rateLimiter) fiber.Handler {
	return func(c fiber.Ctx) error {
		if authExempt(c) {
			return c.Next()
		}
		key := "ip:" + c.IP()
		if l.apiKeyAuth {
			if apiKey := c.Get(authHeaderName); apiKey != "" {
				sum := sha256.Sum256([]byte(apiKey))
				key = "k:" + hex.EncodeToString(sum[:])
			}
		}
		decision := l.check(key, time.Now())
		c.Set(rateLimitLimitHeader, strconv.Itoa(decision.limit))
		c.Set(rateLimitRemainingHeader, strconv.Itoa(decision.remaining))
		c.Set(rateLimitResetHeader, strconv.FormatInt(decision.reset.Unix(), 10))
		if !decision.allowed {
			c.Set("Retry-After", strconv.Itoa(decision.retry))
			return utils.ReportError(c, "rate limit exceeded", fiber.StatusTooManyRequests)
		}
		return c.Next()
	}
}
