package web

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func waitForJobState(t *testing.T, manager *jobManager, id string, states ...string) jobView {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		view, err := manager.get(id)
		if err != nil {
			t.Fatalf("get(%s): %v", id, err)
		}
		for _, state := range states {
			if view.State == state {
				return view
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("job %s did not reach %v", id, states)
	return jobView{}
}

func TestJobManagerRunsWorkAndKeepsTheResult(t *testing.T) {
	manager := newJobManager(context.Background())
	view, err := manager.submit("batch", func(context.Context) (any, error) {
		return map[string]any{"verdict": "sfw"}, nil
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if view.State != jobQueued || view.Kind != "batch" || view.ID == "" {
		t.Fatalf("view = %+v", view)
	}
	done := waitForJobState(t, manager, view.ID, jobDone)
	if done.FinishedAt == "" {
		t.Errorf("FinishedAt is empty")
	}
	result, ok := done.Result.(map[string]any)
	if !ok || result["verdict"] != "sfw" {
		t.Errorf("result = %v", done.Result)
	}
}

func TestJobManagerReportsFailures(t *testing.T) {
	manager := newJobManager(context.Background())
	view, err := manager.submit("batch", func(context.Context) (any, error) {
		return nil, errors.New("model failed")
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	failed := waitForJobState(t, manager, view.ID, jobFailed)
	if !strings.Contains(failed.Error, "model failed") {
		t.Errorf("error = %q", failed.Error)
	}
}

func TestJobManagerCancelsQueuedJobs(t *testing.T) {
	manager := newJobManager(context.Background())
	release := make(chan struct{})
	defer close(release)
	blocking := func(context.Context) (any, error) {
		<-release
		return nil, nil
	}
	first, err := manager.submit("batch", blocking)
	if err != nil {
		t.Fatalf("submit first: %v", err)
	}
	second, err := manager.submit("batch", blocking)
	if err != nil {
		t.Fatalf("submit second: %v", err)
	}
	waitForJobState(t, manager, first.ID, jobRunning)
	waitForJobState(t, manager, second.ID, jobRunning)
	third, err := manager.submit("batch", blocking)
	if err != nil {
		t.Fatalf("submit third: %v", err)
	}
	if err := manager.cancel(third.ID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	canceled := waitForJobState(t, manager, third.ID, jobCanceled)
	if canceled.FinishedAt == "" {
		t.Errorf("FinishedAt is empty")
	}
}

func TestJobManagerCancelsRunningJobs(t *testing.T) {
	manager := newJobManager(context.Background())
	started := make(chan struct{})
	view, err := manager.submit("batch", func(ctx context.Context) (any, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	waitForJobState(t, manager, view.ID, jobRunning)
	<-started
	if err := manager.cancel(view.ID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	waitForJobState(t, manager, view.ID, jobCanceled)
}

func TestJobManagerRejectsCancelingFinishedJobs(t *testing.T) {
	manager := newJobManager(context.Background())
	view, err := manager.submit("batch", func(context.Context) (any, error) { return "ok", nil })
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	waitForJobState(t, manager, view.ID, jobDone)
	if err := manager.cancel(view.ID); err == nil {
		t.Fatalf("cancel of a finished job returned no error")
	}
	if err := manager.cancel("missing"); !errors.Is(err, errJobNotFound) {
		t.Fatalf("cancel of an unknown job = %v", err)
	}
}

func TestJobManagerDropsExpiredJobs(t *testing.T) {
	manager := newJobManager(context.Background())
	manager.ttl = 10 * time.Millisecond
	view, err := manager.submit("batch", func(context.Context) (any, error) { return nil, nil })
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	waitForJobState(t, manager, view.ID, jobDone)
	time.Sleep(30 * time.Millisecond)
	if _, err := manager.get(view.ID); !errors.Is(err, errJobNotFound) {
		t.Fatalf("get after the TTL = %v", err)
	}
}

func TestJobManagerLimitsTheQueue(t *testing.T) {
	manager := newJobManager(context.Background())
	manager.max = 1
	release := make(chan struct{})
	defer close(release)
	if _, err := manager.submit("batch", func(context.Context) (any, error) {
		<-release
		return nil, nil
	}); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if _, err := manager.submit("batch", func(context.Context) (any, error) { return nil, nil }); !errors.Is(err, errJobFull) {
		t.Fatalf("second submit = %v, want errJobFull", err)
	}
}

func TestNewJobIDIsUniqueHex(t *testing.T) {
	first := newJobID()
	second := newJobID()
	if first == second {
		t.Fatalf("ids are equal: %s", first)
	}
	if len(first) != 16 {
		t.Fatalf("id length = %d, want 16", len(first))
	}
	if strings.Trim(first, "0123456789abcdef") != "" {
		t.Fatalf("id is not hex: %s", first)
	}
}

func TestJobManagerRunsCleanupForCancelledQueuedJobs(t *testing.T) {
	manager := newJobManager(context.Background())
	release := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	blocking := func(context.Context) (any, error) {
		<-release
		return nil, nil
	}
	first, err := manager.submit("video", blocking)
	if err != nil {
		t.Fatalf("submit first: %v", err)
	}
	second, err := manager.submit("video", blocking)
	if err != nil {
		t.Fatalf("submit second: %v", err)
	}
	waitForJobState(t, manager, first.ID, jobRunning)
	waitForJobState(t, manager, second.ID, jobRunning)

	cleaned := make(chan struct{})
	third, err := manager.submitWithCleanup(
		"video",
		blocking,
		func() { close(cleaned) },
	)
	if err != nil {
		t.Fatalf("submit third: %v", err)
	}
	if err := manager.cancel(third.ID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	waitForJobState(t, manager, third.ID, jobCanceled)

	close(release)
	released = true
	select {
	case <-cleaned:
	case <-time.After(time.Second):
		t.Fatal("cleanup did not run for a cancelled queued job")
	}
}
