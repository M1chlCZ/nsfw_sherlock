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
	authHeaderName  = "X-API-Key"
	rateLimitWindow = time.Minute
	maxRateBuckets  = 10_000
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
	mu      sync.Mutex
	limit   int
	window  time.Duration
	buckets map[string]*rateBucket
}

type rateBucket struct {
	start time.Time
	count int
}

func newRateLimiter(limit int) *rateLimiter {
	return &rateLimiter{limit: limit, window: rateLimitWindow, buckets: make(map[string]*rateBucket)}
}

func (l *rateLimiter) allow(key string, now time.Time) (bool, int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	bucket, ok := l.buckets[key]
	if !ok || now.Sub(bucket.start) >= l.window {
		if len(l.buckets) >= maxRateBuckets {
			l.prune(now)
		}
		l.buckets[key] = &rateBucket{start: now, count: 1}
		return true, 0
	}
	bucket.count++
	if bucket.count > l.limit {
		retry := int(bucket.start.Add(l.window).Sub(now).Seconds()) + 1
		return false, retry
	}
	return true, 0
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
		var key string
		if apiKey := c.Get(authHeaderName); apiKey != "" {
			sum := sha256.Sum256([]byte(apiKey))
			key = "k:" + hex.EncodeToString(sum[:])
		} else {
			key = "ip:" + c.IP()
		}
		ok, retry := l.allow(key, time.Now())
		if !ok {
			c.Set("Retry-After", strconv.Itoa(retry))
			return utils.ReportError(c, "rate limit exceeded", fiber.StatusTooManyRequests)
		}
		return c.Next()
	}
}
