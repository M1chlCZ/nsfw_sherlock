package web

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Job states reported by the job API.
const (
	jobQueued   = "queued"
	jobRunning  = "running"
	jobDone     = "done"
	jobFailed   = "failed"
	jobCanceled = "canceled"
)

var (
	errJobNotFound = errors.New("job not found")
	errJobFull     = errors.New("too many jobs; retry later")

	jobTTL         = 10 * time.Minute
	jobMaxEntries  = 64
	jobWorkerLimit = 2
)

var jobStates = [...]string{jobQueued, jobRunning, jobDone, jobFailed, jobCanceled}

func isTerminalJobState(state string) bool {
	return state == jobDone || state == jobFailed || state == jobCanceled
}

// jobOptions bounds the background work a manager accepts.
type jobOptions struct {
	Workers    int
	MaxEntries int
	TTL        time.Duration
}

func (o jobOptions) withDefaults() jobOptions {
	if o.Workers < 1 {
		o.Workers = jobWorkerLimit
	}
	if o.MaxEntries < 1 {
		o.MaxEntries = jobMaxEntries
	}
	if o.TTL <= 0 {
		o.TTL = jobTTL
	}
	return o
}

// jobWork runs one analysis; the job manager calls it in the background.
type jobWork func(ctx context.Context) (any, error)

type job struct {
	id       string
	kind     string
	state    string
	created  time.Time
	finished time.Time
	result   any
	err      string
	cancel   context.CancelFunc
	cleanup  func()
}

// jobView is the JSON shape of one job.
type jobView struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	State      string `json:"state"`
	Result     any    `json:"result,omitempty"`
	Error      string `json:"error,omitempty"`
	CreatedAt  string `json:"createdAt"`
	FinishedAt string `json:"finishedAt,omitempty"`
}

func (j *job) view() jobView {
	view := jobView{
		ID:        j.id,
		Kind:      j.kind,
		State:     j.state,
		Result:    j.result,
		Error:     j.err,
		CreatedAt: j.created.Format(time.RFC3339),
	}
	if !j.finished.IsZero() {
		view.FinishedAt = j.finished.Format(time.RFC3339)
	}
	return view
}

// jobManager runs analyses in the background and keeps their results for a while.
type jobManager struct {
	baseCtx context.Context

	mu          sync.Mutex
	jobs        map[string]*job
	order       []string
	subscribers map[string]map[chan jobView]struct{}
	workers     chan struct{}
	ttl         time.Duration
	max         int

	submitted int64
	rejected  int64
	finished  map[string]int64
	durations latencyHistogram
}

func newJobManager(baseCtx context.Context, options jobOptions) *jobManager {
	if baseCtx == nil {
		baseCtx = context.Background()
	}
	options = options.withDefaults()
	return &jobManager{
		baseCtx:     baseCtx,
		jobs:        make(map[string]*job),
		subscribers: make(map[string]map[chan jobView]struct{}),
		workers:     make(chan struct{}, options.Workers),
		ttl:         options.TTL,
		max:         options.MaxEntries,
		finished:    make(map[string]int64),
		durations:   *newLatencyHistogram(jobDurationBuckets),
	}
}

// submit registers a job and starts it in the background.
func (m *jobManager) submit(kind string, work jobWork) (jobView, error) {
	return m.submitWithCleanup(kind, work, nil)
}

// submitWithCleanup is submit with a callback that runs when the job ends,
// even when a queued job is cancelled before its work starts.
func (m *jobManager) submitWithCleanup(
	kind string,
	work jobWork,
	cleanup func(),
) (jobView, error) {
	m.mu.Lock()
	m.sweepLocked(time.Now())
	if len(m.jobs) >= m.max {
		m.rejected++
		m.mu.Unlock()
		return jobView{}, errJobFull
	}
	ctx, cancel := context.WithCancel(m.baseCtx)
	entry := &job{
		id:      newJobID(),
		kind:    kind,
		state:   jobQueued,
		created: time.Now(),
		cancel:  cancel,
		cleanup: cleanup,
	}
	m.jobs[entry.id] = entry
	m.order = append(m.order, entry.id)
	m.submitted++
	view := entry.view()
	m.mu.Unlock()
	go m.run(ctx, entry, work)
	return view, nil
}

func (m *jobManager) run(ctx context.Context, entry *job, work jobWork) {
	m.workers <- struct{}{}
	defer func() {
		<-m.workers
		if entry.cancel != nil {
			entry.cancel()
		}
		m.mu.Lock()
		cleanup := entry.cleanup
		entry.cleanup = nil
		m.mu.Unlock()
		if cleanup != nil {
			cleanup()
		}
	}()
	m.mu.Lock()
	if entry.state == jobCanceled {
		m.mu.Unlock()
		return
	}
	entry.state = jobRunning
	m.notifyLocked(entry)
	m.mu.Unlock()
	result, err := work(ctx)
	m.mu.Lock()
	defer m.mu.Unlock()
	switch {
	case err != nil && errors.Is(err, context.Canceled):
		m.finishLocked(entry, jobCanceled, "", nil)
	case err != nil:
		m.finishLocked(entry, jobFailed, err.Error(), nil)
	default:
		m.finishLocked(entry, jobDone, "", result)
	}
}

// finishLocked records a terminal state, its metrics, and its subscribers.
func (m *jobManager) finishLocked(entry *job, state, errMessage string, result any) {
	entry.state = state
	entry.err = errMessage
	entry.result = result
	entry.finished = time.Now()
	m.finished[state]++
	m.durations.observe(entry.finished.Sub(entry.created).Seconds())
	m.notifyLocked(entry)
}

// notifyLocked sends the current view to every subscriber of the job.
// A terminal state closes the subscription channels.
func (m *jobManager) notifyLocked(entry *job) {
	subscribers := m.subscribers[entry.id]
	if len(subscribers) == 0 {
		return
	}
	view := entry.view()
	terminal := isTerminalJobState(entry.state)
	for channel := range subscribers {
		select {
		case channel <- view:
		default:
		}
		if terminal {
			close(channel)
			delete(subscribers, channel)
		}
	}
	if terminal {
		delete(m.subscribers, entry.id)
	}
}

// get returns one job and drops expired entries.
func (m *jobManager) get(id string) (jobView, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sweepLocked(time.Now())
	entry, ok := m.jobs[id]
	if !ok {
		return jobView{}, errJobNotFound
	}
	return entry.view(), nil
}

// cancel stops a queued job or the running analysis.
func (m *jobManager) cancel(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, ok := m.jobs[id]
	if !ok {
		return errJobNotFound
	}
	switch entry.state {
	case jobQueued:
		if entry.cancel != nil {
			entry.cancel()
		}
		m.finishLocked(entry, jobCanceled, "", nil)
	case jobRunning:
		if entry.cancel != nil {
			entry.cancel()
		}
	default:
		return fmt.Errorf("the job is already %s", entry.state)
	}
	return nil
}

// list returns jobs newest first, optionally filtered by state, plus counts
// for every kept job.
func (m *jobManager) list(state string, limit int) ([]jobView, map[string]int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sweepLocked(time.Now())
	counts := make(map[string]int, len(jobStates)+1)
	for _, name := range jobStates {
		counts[name] = 0
	}
	views := make([]jobView, 0, min(limit, len(m.jobs)))
	for _, id := range slices.Backward(m.order) {
		entry, ok := m.jobs[id]
		if !ok {
			continue
		}
		counts[entry.state]++
		if state != "" && entry.state != state {
			continue
		}
		if len(views) < limit {
			views = append(views, entry.view())
		}
	}
	counts["total"] = len(m.jobs)
	return views, counts
}

// subscribe reports the current job state and then every change until the job
// reaches a terminal state. The returned function releases the subscription.
func (m *jobManager) subscribe(id string) (<-chan jobView, func(), error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sweepLocked(time.Now())
	entry, ok := m.jobs[id]
	if !ok {
		return nil, nil, errJobNotFound
	}
	channel := make(chan jobView, 8)
	channel <- entry.view()
	if isTerminalJobState(entry.state) {
		close(channel)
		return channel, func() {}, nil
	}
	subscribers := m.subscribers[id]
	if subscribers == nil {
		subscribers = make(map[chan jobView]struct{})
		m.subscribers[id] = subscribers
	}
	subscribers[channel] = struct{}{}
	unsubscribe := func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		subscribers := m.subscribers[id]
		if _, ok := subscribers[channel]; !ok {
			return
		}
		delete(subscribers, channel)
		close(channel)
		if len(subscribers) == 0 {
			delete(m.subscribers, id)
		}
	}
	return channel, unsubscribe, nil
}

// writeMetrics renders the background job metrics in Prometheus text format.
func (m *jobManager) writeMetrics(b *strings.Builder) {
	m.mu.Lock()
	defer m.mu.Unlock()
	current := make(map[string]int, len(jobStates))
	for _, entry := range m.jobs {
		current[entry.state]++
	}
	b.WriteString("# HELP nsfw_jobs_submitted_total Accepted background jobs.\n")
	b.WriteString("# TYPE nsfw_jobs_submitted_total counter\n")
	fmt.Fprintf(b, "nsfw_jobs_submitted_total %d\n", m.submitted)
	b.WriteString("# HELP nsfw_jobs_rejected_total Rejected background jobs.\n")
	b.WriteString("# TYPE nsfw_jobs_rejected_total counter\n")
	fmt.Fprintf(b, "nsfw_jobs_rejected_total %d\n", m.rejected)
	b.WriteString("# HELP nsfw_jobs_current Kept background jobs by state.\n")
	b.WriteString("# TYPE nsfw_jobs_current gauge\n")
	for _, state := range jobStates {
		fmt.Fprintf(b, "nsfw_jobs_current{state=%q} %d\n", state, current[state])
	}
	b.WriteString("# HELP nsfw_jobs_finished_total Finished background jobs by state.\n")
	b.WriteString("# TYPE nsfw_jobs_finished_total counter\n")
	for _, state := range []string{jobDone, jobFailed, jobCanceled} {
		fmt.Fprintf(b, "nsfw_jobs_finished_total{state=%q} %d\n", state, m.finished[state])
	}
	b.WriteString("# HELP nsfw_job_duration_seconds Job lifetime in seconds.\n")
	b.WriteString("# TYPE nsfw_job_duration_seconds histogram\n")
	writeHistogram(b, "nsfw_job_duration_seconds", "", &m.durations)
}

func (m *jobManager) sweepLocked(now time.Time) {
	for len(m.order) > 0 {
		id := m.order[0]
		entry, ok := m.jobs[id]
		if !ok {
			m.order = m.order[1:]
			continue
		}
		if entry.finished.IsZero() || now.Sub(entry.finished) <= m.ttl {
			break
		}
		delete(m.jobs, id)
		m.order = m.order[1:]
	}
}

func newJobID() string {
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	return hex.EncodeToString(raw)
}
