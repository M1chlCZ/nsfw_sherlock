package web

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v3"
)

const metricsPath = "/metrics"

var durationBuckets = [...]float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

// metricsState gathers Prometheus metrics for the HTTP API.
type metricsState struct {
	mu         sync.Mutex
	requests   map[string]int64
	verdicts   map[string]int64
	histograms map[string]*latencyHistogram
	inflight   int64
	cache      *responseCache
}

type latencyHistogram struct {
	buckets [len(durationBuckets)]int64
	sum     float64
	count   int64
}

func newMetricsState(cache *responseCache) *metricsState {
	return &metricsState{
		requests:   make(map[string]int64),
		verdicts:   make(map[string]int64),
		histograms: make(map[string]*latencyHistogram),
		cache:      cache,
	}
}

func metricsMiddleware(m *metricsState) fiber.Handler {
	return func(c fiber.Ctx) error {
		if c.Path() == metricsPath {
			return c.Next()
		}
		start := time.Now()
		status := 0
		m.mu.Lock()
		m.inflight++
		m.mu.Unlock()
		defer func() {
			if status == 0 {
				status = fiber.StatusInternalServerError
			}
			m.observe(c.Route().Path, status, time.Since(start))
		}()
		err := c.Next()
		status = c.Response().StatusCode()
		if err != nil {
			status = errorStatus(err)
		}
		return err
	}
}

func (m *metricsState) observe(endpoint string, status int, duration time.Duration) {
	if endpoint == "" {
		endpoint = "unmatched"
	}
	elapsed := duration.Seconds()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.inflight--
	m.requests[endpoint+"\x00"+strconv.Itoa(status)]++
	histogram := m.histograms[endpoint]
	if histogram == nil {
		histogram = &latencyHistogram{}
		m.histograms[endpoint] = histogram
	}
	for i, bound := range durationBuckets {
		if elapsed <= bound {
			histogram.buckets[i]++
		}
	}
	histogram.sum += elapsed
	histogram.count++
}

func (m *metricsState) observeVerdict(verdict string) {
	if verdict == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.verdicts[verdict]++
}

func (m *metricsState) render() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var b strings.Builder
	b.WriteString("# HELP nsfw_requests_total Completed HTTP requests.\n")
	b.WriteString("# TYPE nsfw_requests_total counter\n")
	for _, key := range sortedKeys(m.requests) {
		endpoint, status, _ := strings.Cut(key, "\x00")
		fmt.Fprintf(&b, "nsfw_requests_total{endpoint=%q,status=%q} %d\n", endpoint, status, m.requests[key])
	}
	b.WriteString("# HELP nsfw_request_duration_seconds Request duration in seconds.\n")
	b.WriteString("# TYPE nsfw_request_duration_seconds histogram\n")
	endpoints := make([]string, 0, len(m.histograms))
	for endpoint := range m.histograms {
		endpoints = append(endpoints, endpoint)
	}
	sort.Strings(endpoints)
	for _, endpoint := range endpoints {
		histogram := m.histograms[endpoint]
		for i, bound := range durationBuckets {
			fmt.Fprintf(
				&b,
				"nsfw_request_duration_seconds_bucket{endpoint=%q,le=%q} %d\n",
				endpoint,
				strconv.FormatFloat(bound, 'g', -1, 64),
				histogram.buckets[i],
			)
		}
		fmt.Fprintf(
			&b,
			"nsfw_request_duration_seconds_bucket{endpoint=%q,le=\"+Inf\"} %d\n",
			endpoint,
			histogram.count,
		)
		fmt.Fprintf(&b, "nsfw_request_duration_seconds_sum{endpoint=%q} %g\n", endpoint, histogram.sum)
		fmt.Fprintf(&b, "nsfw_request_duration_seconds_count{endpoint=%q} %d\n", endpoint, histogram.count)
	}
	b.WriteString("# HELP nsfw_verdicts_total Analyzed images per verdict.\n")
	b.WriteString("# TYPE nsfw_verdicts_total counter\n")
	for _, verdict := range sortedKeys(m.verdicts) {
		fmt.Fprintf(&b, "nsfw_verdicts_total{verdict=%q} %d\n", verdict, m.verdicts[verdict])
	}
	if m.cache != nil {
		b.WriteString("# HELP nsfw_cache_hits_total Response cache hits.\n")
		b.WriteString("# TYPE nsfw_cache_hits_total counter\n")
		fmt.Fprintf(&b, "nsfw_cache_hits_total %d\n", m.cache.hits.Load())
		b.WriteString("# HELP nsfw_cache_misses_total Response cache misses.\n")
		b.WriteString("# TYPE nsfw_cache_misses_total counter\n")
		fmt.Fprintf(&b, "nsfw_cache_misses_total %d\n", m.cache.misses.Load())
		b.WriteString("# HELP nsfw_cache_entries Current response cache entries.\n")
		b.WriteString("# TYPE nsfw_cache_entries gauge\n")
		fmt.Fprintf(&b, "nsfw_cache_entries %d\n", m.cache.size())
	}
	b.WriteString("# HELP nsfw_inflight_requests Requests in flight.\n")
	b.WriteString("# TYPE nsfw_inflight_requests gauge\n")
	fmt.Fprintf(&b, "nsfw_inflight_requests %d\n", m.inflight)
	return b.String()
}

func (m *metricsState) handle(c fiber.Ctx) error {
	c.Set(fiber.HeaderContentType, "text/plain; version=0.0.4; charset=utf-8")
	return c.SendString(m.render())
}

func sortedKeys(values map[string]int64) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
