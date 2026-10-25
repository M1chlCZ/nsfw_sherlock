package web

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
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

	mu      sync.Mutex
	jobs    map[string]*job
	order   []string
	workers chan struct{}
	ttl     time.Duration
	max     int
}

func newJobManager(baseCtx context.Context) *jobManager {
	if baseCtx == nil {
		baseCtx = context.Background()
	}
	return &jobManager{
		baseCtx: baseCtx,
		jobs:    make(map[string]*job),
		workers: make(chan struct{}, jobWorkerLimit),
		ttl:     jobTTL,
		max:     jobMaxEntries,
	}
}

// submit registers a job and starts it in the background.
func (m *jobManager) submit(kind string, work jobWork) (jobView, error) {
	m.mu.Lock()
	m.sweepLocked(time.Now())
	if len(m.jobs) >= m.max {
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
	}
	m.jobs[entry.id] = entry
	m.order = append(m.order, entry.id)
	view := entry.view()
	m.mu.Unlock()
	go m.run(ctx, entry, work)
	return view, nil
}

func (m *jobManager) run(ctx context.Context, entry *job, work jobWork) {
	m.workers <- struct{}{}
	defer func() { <-m.workers }()
	m.mu.Lock()
	if entry.state == jobCanceled {
		m.mu.Unlock()
		return
	}
	entry.state = jobRunning
	m.mu.Unlock()
	result, err := work(ctx)
	m.mu.Lock()
	defer m.mu.Unlock()
	entry.finished = time.Now()
	switch {
	case err != nil && errors.Is(err, context.Canceled):
		entry.state = jobCanceled
	case err != nil:
		entry.state = jobFailed
		entry.err = err.Error()
	default:
		entry.state = jobDone
		entry.result = result
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
		entry.state = jobCanceled
		entry.finished = time.Now()
		if entry.cancel != nil {
			entry.cancel()
		}
	case jobRunning:
		if entry.cancel != nil {
			entry.cancel()
		}
	default:
		return fmt.Errorf("the job is already %s", entry.state)
	}
	return nil
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
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(raw)
}
