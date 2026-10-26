package web

import (
	"context"
	"errors"
	"reflect"
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
	manager := newJobManager(context.Background(), jobOptions{})
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
	manager := newJobManager(context.Background(), jobOptions{})
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
	manager := newJobManager(context.Background(), jobOptions{})
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
	manager := newJobManager(context.Background(), jobOptions{})
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
	manager := newJobManager(context.Background(), jobOptions{})
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
	manager := newJobManager(context.Background(), jobOptions{})
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
	manager := newJobManager(context.Background(), jobOptions{})
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

func TestJobOptionsApplyDefaults(t *testing.T) {
	manager := newJobManager(context.Background(), jobOptions{})
	if cap(manager.workers) != jobWorkerLimit || manager.max != jobMaxEntries || manager.ttl != jobTTL {
		t.Fatalf("defaults = workers %d, max %d, ttl %s", cap(manager.workers), manager.max, manager.ttl)
	}
	manager = newJobManager(context.Background(), jobOptions{
		Workers:    3,
		MaxEntries: 7,
		TTL:        time.Minute,
	})
	if cap(manager.workers) != 3 || manager.max != 7 || manager.ttl != time.Minute {
		t.Fatalf("options = workers %d, max %d, ttl %s", cap(manager.workers), manager.max, manager.ttl)
	}
}

func TestJobManagerListsNewestFirstAndCounts(t *testing.T) {
	manager := newJobManager(context.Background(), jobOptions{Workers: 1})
	release := make(chan struct{})
	first, err := manager.submit("batch", func(context.Context) (any, error) {
		<-release
		return "first", nil
	})
	if err != nil {
		t.Fatalf("submit first: %v", err)
	}
	waitForJobState(t, manager, first.ID, jobRunning)
	second, err := manager.submit("batch", func(context.Context) (any, error) { return nil, nil })
	if err != nil {
		t.Fatalf("submit second: %v", err)
	}
	if err := manager.cancel(second.ID); err != nil {
		t.Fatalf("cancel second: %v", err)
	}
	close(release)
	waitForJobState(t, manager, first.ID, jobDone)
	waitForJobState(t, manager, second.ID, jobCanceled)

	views, counts := manager.list("", 10)
	if len(views) != 2 {
		t.Fatalf("list returned %d jobs, want 2", len(views))
	}
	if views[0].ID != second.ID || views[1].ID != first.ID {
		t.Fatalf("list order = %s, %s; want newest first", views[0].ID, views[1].ID)
	}
	wantCounts := map[string]int{
		jobQueued:   0,
		jobRunning:  0,
		jobDone:     1,
		jobFailed:   0,
		jobCanceled: 1,
		"total":     2,
	}
	if !reflect.DeepEqual(counts, wantCounts) {
		t.Errorf("counts = %v, want %v", counts, wantCounts)
	}

	filtered, filteredCounts := manager.list(jobDone, 10)
	if len(filtered) != 1 || filtered[0].ID != first.ID {
		t.Fatalf("filtered list = %+v", filtered)
	}
	if !reflect.DeepEqual(filteredCounts, wantCounts) {
		t.Errorf("filtered counts = %v, want global counts %v", filteredCounts, wantCounts)
	}

	limited, _ := manager.list("", 1)
	if len(limited) != 1 || limited[0].ID != second.ID {
		t.Fatalf("limited list = %+v", limited)
	}
}

func TestJobManagerSubscribesToStateChanges(t *testing.T) {
	manager := newJobManager(context.Background(), jobOptions{Workers: 1})
	releaseFirst := make(chan struct{})
	first, err := manager.submit("batch", func(context.Context) (any, error) {
		<-releaseFirst
		return nil, nil
	})
	if err != nil {
		t.Fatalf("submit first: %v", err)
	}
	waitForJobState(t, manager, first.ID, jobRunning)

	started := make(chan struct{})
	releaseSecond := make(chan struct{})
	second, err := manager.submit("batch", func(context.Context) (any, error) {
		close(started)
		<-releaseSecond
		return "result", nil
	})
	if err != nil {
		t.Fatalf("submit second: %v", err)
	}
	events, unsubscribe, err := manager.subscribe(second.ID)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer unsubscribe()
	if view := <-events; view.State != jobQueued {
		t.Fatalf("first event = %+v, want queued", view)
	}

	close(releaseFirst)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("the second job did not start")
	}
	if view := <-events; view.State != jobRunning {
		t.Fatalf("second event = %+v, want running", view)
	}

	close(releaseSecond)
	if view := <-events; view.State != jobDone {
		t.Fatalf("third event = %+v, want done", view)
	}
	if _, ok := <-events; ok {
		t.Fatal("the event channel stayed open after a terminal state")
	}

	if _, _, err := manager.subscribe("missing"); !errors.Is(err, errJobNotFound) {
		t.Fatalf("subscribe to an unknown job = %v", err)
	}
}

func TestJobManagerUnsubscribeClosesTheChannel(t *testing.T) {
	manager := newJobManager(context.Background(), jobOptions{})
	release := make(chan struct{})
	defer close(release)
	view, err := manager.submit("batch", func(context.Context) (any, error) {
		<-release
		return nil, nil
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	waitForJobState(t, manager, view.ID, jobRunning)
	events, unsubscribe, err := manager.subscribe(view.ID)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	<-events
	unsubscribe()
	if _, ok := <-events; ok {
		t.Fatal("unsubscribe left the event channel open")
	}
}

func TestJobManagerWritesMetrics(t *testing.T) {
	manager := newJobManager(context.Background(), jobOptions{Workers: 1, MaxEntries: 1})
	release := make(chan struct{})
	blocking, err := manager.submit("batch", func(context.Context) (any, error) {
		<-release
		return nil, nil
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	waitForJobState(t, manager, blocking.ID, jobRunning)
	if _, err := manager.submit("batch", func(context.Context) (any, error) { return nil, nil }); !errors.Is(err, errJobFull) {
		t.Fatalf("second submit = %v, want errJobFull", err)
	}
	var b strings.Builder
	manager.writeMetrics(&b)
	out := b.String()
	for _, want := range []string{
		"nsfw_jobs_submitted_total 1",
		"nsfw_jobs_rejected_total 1",
		`nsfw_jobs_current{state="queued"} 0`,
		`nsfw_jobs_finished_total{state="done"} 0`,
		"nsfw_job_duration_seconds_count 0",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("metrics lack %q:\n%s", want, out)
		}
	}
	close(release)
	deadline := time.Now().Add(time.Second)
	for {
		view, err := manager.get(blocking.ID)
		if err == nil && view.State == jobDone {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the job did not finish")
		}
		time.Sleep(time.Millisecond)
	}
	b.Reset()
	manager.writeMetrics(&b)
	out = b.String()
	for _, want := range []string{
		`nsfw_jobs_current{state="done"} 1`,
		`nsfw_jobs_finished_total{state="done"} 1`,
		"nsfw_job_duration_seconds_count 1",
		`nsfw_job_duration_seconds_bucket{le="+Inf"} 1`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("metrics lack %q:\n%s", want, out)
		}
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
	manager := newJobManager(context.Background(), jobOptions{})
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
