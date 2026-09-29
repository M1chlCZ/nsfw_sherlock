package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeClassifier struct {
	scores  map[string]float32
	delay   time.Duration
	err     error
	done    chan error
	started chan<- struct{}
	release <-chan struct{}
}

func (f *fakeClassifier) run(ctx context.Context, img image.Image) (map[string]float32, error) {
	scores, err := f.runContext(ctx)
	if f.done != nil {
		f.done <- err
	}
	return scores, err
}

func (f *fakeClassifier) runContext(ctx context.Context) (map[string]float32, error) {
	if err := waitBarrier(ctx, f.started, f.release); err != nil {
		return nil, err
	}
	if err := sleepContext(ctx, f.delay); err != nil {
		return nil, err
	}
	if f.err != nil {
		return nil, f.err
	}
	return f.scores, nil
}

type fakeDetector struct {
	dets    []Detection
	delay   time.Duration
	err     error
	done    chan error
	started chan<- struct{}
	release <-chan struct{}
}

func (f *fakeDetector) run(ctx context.Context, img image.Image) ([]Detection, error) {
	dets, err := f.runContext(ctx)
	if f.done != nil {
		f.done <- err
	}
	return dets, err
}

func (f *fakeDetector) runContext(ctx context.Context) ([]Detection, error) {
	if err := waitBarrier(ctx, f.started, f.release); err != nil {
		return nil, err
	}
	if err := sleepContext(ctx, f.delay); err != nil {
		return nil, err
	}
	if f.err != nil {
		return nil, f.err
	}
	return f.dets, nil
}

func waitBarrier(ctx context.Context, started chan<- struct{}, release <-chan struct{}) error {
	if started != nil {
		select {
		case started <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

func classifierEntry(id, role string, required bool, runner classifierRunner) modelEntry {
	return modelEntry{
		model: Model{
			ID:       id,
			Kind:     KindClassifier,
			Role:     role,
			SHA256:   strings.Repeat("a", 64),
			Required: required,
		},
		classifier: runner,
	}
}

func modelIDs(infos []ModelInfo) []string {
	ids := make([]string, len(infos))
	for i, info := range infos {
		ids[i] = info.ID
	}
	return ids
}

func TestAnalyzeRunsModelsInParallel(t *testing.T) {
	started := make(chan struct{}, 3)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseAll := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseAll()

	photo := &fakeClassifier{
		scores:  map[string]float32{"neutral": 0.9, "high": 0.1},
		started: started,
		release: release,
	}
	anime := &fakeClassifier{
		scores:  map[string]float32{"safe": 0.8, "r15": 0.1, "r18": 0.1},
		started: started,
		release: release,
	}
	det := &fakeDetector{
		dets:    []Detection{{Label: "EXPOSED", Score: 0.5, Box: Box{X: 1, Y: 1, W: 2, H: 2}}},
		started: started,
		release: release,
	}

	entries := []modelEntry{
		classifierEntry("photo", RolePhoto, true, photo),
		classifierEntry("anime", RoleAnime, true, anime),
		{
			model: Model{
				ID:     "detector",
				Kind:   KindDetector,
				Role:   RoleRegions,
				SHA256: strings.Repeat("b", 64),
				Detector: &DetectorSpec{
					Classes:         []string{"EXPOSED"},
					ExplicitClasses: []string{"EXPOSED"},
				},
			},
			detector: det,
		},
	}
	e := newEngineFromEntries(entries, "balanced", Options{})

	png := quadPNG(t)
	type outcome struct {
		analysis Analysis
		err      error
	}
	done := make(chan outcome, 1)
	go func() {
		analysis, err := e.Analyze(context.Background(), png)
		done <- outcome{analysis, err}
	}()

	for i := range entries {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatalf("only %d of %d models started concurrently", i, len(entries))
		}
	}
	releaseAll()

	res := <-done
	if res.err != nil {
		t.Fatalf("Analyze() error = %v, want nil", res.err)
	}
	got := res.analysis
	if got.Verdict != VerdictExplicit {
		t.Errorf("Verdict = %q, want %q", got.Verdict, VerdictExplicit)
	}
	if got.Labels.Neutral != 0.9 || got.Labels.Porn != 0.1 {
		t.Errorf("Labels = %+v, want neutral 0.9 porn 0.1", got.Labels)
	}
	if got.Labels.Drawings != 0.8 || got.Labels.Hentai != 0.1 {
		t.Errorf("Labels = %+v, want drawings 0.8 hentai 0.1", got.Labels)
	}
	if len(got.Detections) != 1 || got.Detections[0].Label != "EXPOSED" {
		t.Errorf("Detections = %+v, want one EXPOSED detection", got.Detections)
	}
	if got, want := strings.Join(modelIDs(got.Models), ","), "photo,anime,detector"; got != want {
		t.Errorf("Models = %v, want [photo anime detector]", got)
	}
	if got.ElapsedMs < 0 {
		t.Errorf("ElapsedMs = %d, want >= 0", got.ElapsedMs)
	}
}

func TestAnalyzeRequiredModelError(t *testing.T) {
	boom := errors.New("boom")
	required := &fakeClassifier{err: boom, delay: 20 * time.Millisecond}
	cancelled := make(chan error, 1)
	optional := &fakeClassifier{scores: map[string]float32{"safe": 1}, delay: time.Hour, done: cancelled}

	e := newEngineFromEntries([]modelEntry{
		classifierEntry("required", RolePhoto, true, required),
		classifierEntry("optional", RoleAnime, false, optional),
	}, "balanced", Options{Log: discardLogger()})

	_, err := e.Analyze(context.Background(), quadPNG(t))
	if err == nil {
		t.Fatal("Analyze() error = nil, want required model error")
	}
	if !strings.Contains(err.Error(), `"required"`) {
		t.Errorf("Analyze() error = %v, want it to mention model %q", err, "required")
	}
	if !errors.Is(err, boom) {
		t.Errorf("Analyze() error = %v, want it to wrap %v", err, boom)
	}
	select {
	case got := <-cancelled:
		if !errors.Is(got, context.Canceled) {
			t.Errorf("cancelled model error = %v, want context.Canceled", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("other model was not cancelled after the required model failed")
	}
}

func TestAnalyzeOptionalModelError(t *testing.T) {
	photo := &fakeClassifier{scores: map[string]float32{"neutral": 0.9, "high": 0.1}}
	anime := &fakeClassifier{err: errors.New("anime unavailable")}

	e := newEngineFromEntries([]modelEntry{
		classifierEntry("photo", RolePhoto, false, photo),
		classifierEntry("anime", RoleAnime, false, anime),
	}, "balanced", Options{Log: discardLogger()})

	got, err := e.Analyze(context.Background(), quadPNG(t))
	if err != nil {
		t.Fatalf("Analyze() error = %v, want nil for optional model failure", err)
	}
	if got.Verdict != VerdictSFW {
		t.Errorf("Verdict = %q, want %q", got.Verdict, VerdictSFW)
	}
	if got.Labels.Neutral != 0.9 || got.Labels.Porn != 0.1 {
		t.Errorf("Labels = %+v, want neutral 0.9 porn 0.1 from the surviving model", got.Labels)
	}
	warnings := e.Warnings()
	if len(warnings) != 1 {
		t.Fatalf("Warnings() = %v, want one warning for the failed optional model", warnings)
	}
	if !strings.Contains(warnings[0], `"anime"`) {
		t.Errorf("Warnings() = %v, want one mentioning model %q", warnings, "anime")
	}
}

func TestAnalyzeContextTimeout(t *testing.T) {
	blocker := &fakeClassifier{scores: map[string]float32{"neutral": 1}, delay: time.Hour}
	e := newEngineFromEntries([]modelEntry{
		classifierEntry("slow", RolePhoto, true, blocker),
	}, "balanced", Options{Log: discardLogger()})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := e.Analyze(ctx, quadPNG(t))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Analyze() error = %v, want context.DeadlineExceeded", err)
	}
	if !strings.Contains(err.Error(), `"slow"`) {
		t.Errorf("Analyze() error = %v, want it to mention model %q", err, "slow")
	}
}

func TestAnalyzeParentCancellationWithOnlyOptionalModels(t *testing.T) {
	started := make(chan struct{}, 1)
	blocker := &fakeClassifier{scores: map[string]float32{"neutral": 1}, delay: time.Hour, started: started}
	e := newEngineFromEntries([]modelEntry{
		classifierEntry("optional", RolePhoto, false, blocker),
	}, "balanced", Options{Log: discardLogger()})

	ctx, cancel := context.WithCancel(context.Background())
	type outcome struct {
		analysis Analysis
		err      error
	}
	done := make(chan outcome, 1)
	png := quadPNG(t)
	go func() {
		analysis, err := e.Analyze(ctx, png)
		done <- outcome{analysis, err}
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("model did not start")
	}
	cancel()

	res := <-done
	if !errors.Is(res.err, context.Canceled) {
		t.Fatalf("Analyze() error = %v, want context.Canceled even though every model is optional", res.err)
	}
	if !reflect.DeepEqual(res.analysis, Analysis{}) {
		t.Errorf("Analyze() analysis = %+v, want the zero Analysis on cancellation", res.analysis)
	}
}

func TestAnalyzeCanceledContextSkipsDecode(t *testing.T) {
	e := newEngineFromEntries(nil, "balanced", Options{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := e.Analyze(ctx, []byte("not an image"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Analyze(canceled) error = %v, want context.Canceled", err)
	}
	if errors.Is(err, ErrUnsupportedImage) {
		t.Errorf("Analyze(canceled) error = %v, want the cancellation before decoding", err)
	}
}

func TestAnalyzeInvalidImage(t *testing.T) {
	t.Run("unsupported", func(t *testing.T) {
		e := newEngineFromEntries(nil, "balanced", Options{})
		_, err := e.Analyze(context.Background(), []byte("not an image"))
		if !errors.Is(err, ErrUnsupportedImage) {
			t.Fatalf("Analyze(garbage) error = %v, want ErrUnsupportedImage", err)
		}
	})

	t.Run("too large", func(t *testing.T) {
		e := newEngineFromEntries(nil, "balanced", Options{Limits: Limits{MaxBytes: 8}})
		_, err := e.Analyze(context.Background(), quadPNG(t))
		if !errors.Is(err, ErrImageTooLarge) {
			t.Fatalf("Analyze(oversized) error = %v, want ErrImageTooLarge", err)
		}
	})
}

func TestAnalyzeSameRoleResolutionIsDeterministic(t *testing.T) {
	first := &fakeClassifier{scores: map[string]float32{"neutral": 0.1, "high": 0.9}, delay: 30 * time.Millisecond}
	second := &fakeClassifier{scores: map[string]float32{"neutral": 0.9, "high": 0.1}}

	e := newEngineFromEntries([]modelEntry{
		classifierEntry("first", RolePhoto, false, first),
		classifierEntry("second", RolePhoto, false, second),
	}, "balanced", Options{Log: discardLogger()})

	png := quadPNG(t)
	var want Analysis
	for run := range 3 {
		got, err := e.Analyze(context.Background(), png)
		if err != nil {
			t.Fatalf("Analyze() run %d error = %v, want nil", run, err)
		}
		if got.Photo == nil || got.Photo.Model != "first" {
			t.Fatalf("Analyze() run %d Photo = %+v, want the manifest-first model %q to win", run, got.Photo, "first")
		}
		if got.Labels.Porn != 0.9 {
			t.Fatalf("Analyze() run %d Labels.Porn = %v, want 0.9 from the manifest-first model", run, got.Labels.Porn)
		}
		normalized := got
		normalized.ElapsedMs = 0
		if run == 0 {
			want = normalized
			continue
		}
		if !reflect.DeepEqual(normalized, want) {
			t.Fatalf("Analyze() run %d = %+v, want the same result as run 0 = %+v", run, normalized, want)
		}
	}
}

func TestAnalyzeFailedOptionalClassifierDoesNotOverrideFallback(t *testing.T) {
	t.Run("fallback wins", func(t *testing.T) {
		primary := &fakeClassifier{err: errors.New("primary down")}
		fallback := &fakeClassifier{scores: map[string]float32{"neutral": 0.9, "high": 0.9}}
		e := newEngineFromEntries([]modelEntry{
			classifierEntry("primary", RolePhoto, false, primary),
			classifierEntry("fallback", RolePhoto, false, fallback),
		}, "balanced", Options{Log: discardLogger()})

		got, err := e.Analyze(context.Background(), quadPNG(t))
		if err != nil {
			t.Fatalf("Analyze() error = %v, want nil", err)
		}
		if got.Photo == nil || got.Photo.Model != "fallback" {
			t.Fatalf("Photo = %+v, want the healthy same-role fallback to win", got.Photo)
		}
		if got.Verdict != VerdictExplicit {
			t.Errorf("Verdict = %q, want %q from the fallback scores", got.Verdict, VerdictExplicit)
		}
		if got.Labels.Neutral != 0.9 || got.Labels.Porn != 0.9 {
			t.Errorf("Labels = %+v, want the fallback's neutral 0.9 / porn 0.9", got.Labels)
		}
	})

	t.Run("detector result survives", func(t *testing.T) {
		broken := &fakeClassifier{err: errors.New("broken classifier")}
		entries := []modelEntry{
			classifierEntry("broken", RolePhoto, false, broken),
			{
				model: Model{
					ID:     "detector",
					Kind:   KindDetector,
					Role:   RoleRegions,
					SHA256: strings.Repeat("b", 64),
					Detector: &DetectorSpec{
						Classes:         []string{"EXPOSED"},
						ExplicitClasses: []string{"EXPOSED"},
					},
				},
				detector: &fakeDetector{
					dets: []Detection{{Label: "EXPOSED", Score: 0.6, Box: Box{X: 1, Y: 1, W: 2, H: 2}}},
				},
			},
		}
		e := newEngineFromEntries(entries, "balanced", Options{Log: discardLogger()})

		got, err := e.Analyze(context.Background(), quadPNG(t))
		if err != nil {
			t.Fatalf("Analyze() error = %v, want nil", err)
		}
		if got.Photo != nil {
			t.Errorf("Photo = %+v, want nil: a failed optional classifier must not contribute", got.Photo)
		}
		if got.Verdict != VerdictExplicit {
			t.Errorf("Verdict = %q, want %q from the detector alone", got.Verdict, VerdictExplicit)
		}
		if len(got.Detections) != 1 || got.Detections[0].Label != "EXPOSED" {
			t.Errorf("Detections = %+v, want the one EXPOSED detection", got.Detections)
		}
	})
}

func TestAnalyzeWarningsDedupedBoundedAndLogged(t *testing.T) {
	t.Run("deduped by model id", func(t *testing.T) {
		e := newEngineFromEntries([]modelEntry{
			classifierEntry("anime", RoleAnime, false, &fakeClassifier{err: errors.New("down")}),
		}, "balanced", Options{Log: discardLogger()})
		for range 3 {
			if _, err := e.Analyze(context.Background(), quadPNG(t)); err != nil {
				t.Fatalf("Analyze() error = %v, want nil", err)
			}
		}
		if got := e.Warnings(); len(got) != 1 {
			t.Errorf("Warnings() = %v after 3 failures of one model, want a single warning", got)
		}
	})

	t.Run("bounded", func(t *testing.T) {
		var entries []modelEntry
		for i := range maxEngineWarnings + 6 {
			id := fmt.Sprintf("m%03d", i)
			entries = append(entries, classifierEntry(id, RoleAnime, false, &fakeClassifier{err: errors.New("down")}))
		}
		e := newEngineFromEntries(entries, "balanced", Options{Log: discardLogger()})
		if _, err := e.Analyze(context.Background(), quadPNG(t)); err != nil {
			t.Fatalf("Analyze() error = %v, want nil", err)
		}
		got := e.Warnings()
		if len(got) != maxEngineWarnings+1 {
			t.Fatalf("Warnings() length = %d, want %d stored plus one dropped notice", len(got), maxEngineWarnings)
		}
		if last := got[len(got)-1]; !strings.Contains(last, "6 warnings dropped") {
			t.Errorf("Warnings() last = %q, want a dropped-warnings notice", last)
		}
	})

	t.Run("logged through Options.Log", func(t *testing.T) {
		var buf bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&buf, nil))
		e := newEngineFromEntries([]modelEntry{
			classifierEntry("anime", RoleAnime, false, &fakeClassifier{err: errors.New("down")}),
		}, "balanced", Options{Log: logger})
		if _, err := e.Analyze(context.Background(), quadPNG(t)); err != nil {
			t.Fatalf("Analyze() error = %v, want nil", err)
		}
		logged := buf.String()
		if !strings.Contains(logged, "level=WARN") || !strings.Contains(logged, "model=anime") {
			t.Errorf("log output = %q, want a WARN record for model=anime", logged)
		}
	})
}

func TestNewEngineFromEntriesProfileAndWarnings(t *testing.T) {
	e := newEngineFromEntries(nil, "fast", Options{})
	if got := e.Profile(); got != "fast" {
		t.Errorf("Profile() = %q, want %q", got, "fast")
	}

	e.addWarning("m", "first")
	got := e.Warnings()
	if len(got) != 1 || got[0] != "first" {
		t.Fatalf("Warnings() = %v, want [first]", got)
	}
	got[0] = "mutated"
	_ = append(got, "second")
	again := e.Warnings()
	if len(again) != 1 || again[0] != "first" {
		t.Errorf("Warnings() = %v after caller mutation, want [first] (copy semantics)", again)
	}
}

func stubModel(id string, required bool) Model {
	return Model{
		ID:          id,
		Kind:        KindClassifier,
		Role:        RolePhoto,
		File:        id + ".onnx",
		SHA256:      strings.Repeat("a", 64),
		InputNames:  []string{"input"},
		OutputNames: []string{"logits"},
		Required:    required,
	}
}

func stubManifest(models ...Model) *Manifest {
	ids := make([]string, len(models))
	for i, model := range models {
		ids[i] = model.ID
	}
	return &Manifest{
		Version:  1,
		Profiles: map[string]Profile{"balanced": {Models: ids}},
		Models:   models,
	}
}

func stubEntry(id string) modelEntry {
	return classifierEntry(id, RolePhoto, false, &fakeClassifier{scores: map[string]float32{"neutral": 1}})
}

func withBuildEntry(t *testing.T, fn func(Model, Options) (modelEntry, func() error, error)) {
	t.Helper()
	previous := buildEntryFn
	buildEntryFn = fn
	t.Cleanup(func() { buildEntryFn = previous })
}

func TestNewEngineOptionalFailureKeepsEarlierEntries(t *testing.T) {
	var closed []string
	withBuildEntry(t, func(m Model, opts Options) (modelEntry, func() error, error) {
		switch m.ID {
		case "good":
			return stubEntry("good"), func() error { closed = append(closed, "good"); return nil }, nil
		case "broken":
			return modelEntry{}, nil, errors.New("missing model file")
		default:
			return modelEntry{}, nil, fmt.Errorf("unexpected model %q", m.ID)
		}
	})

	e, err := NewEngine(
		stubManifest(stubModel("good", false), stubModel("broken", false)),
		Options{Log: discardLogger()},
	)
	if err != nil {
		t.Fatalf("NewEngine() error = %v, want nil for an optional model failure", err)
	}
	if len(closed) != 0 {
		t.Fatalf("closers called on the recoverable path: %v", closed)
	}
	warnings := e.Warnings()
	if len(warnings) != 1 || !strings.Contains(warnings[0], `"broken"`) {
		t.Fatalf("Warnings() = %v, want one mentioning %q", warnings, "broken")
	}

	got, err := e.Analyze(context.Background(), quadPNG(t))
	if err != nil {
		t.Fatalf("Analyze() error = %v, want nil", err)
	}
	if got.Labels.Neutral != 1 {
		t.Errorf("Analyze() = %+v, want the surviving entry's scores", got.Labels)
	}

	if err := e.Close(); err != nil {
		t.Fatalf("Close() error = %v, want nil", err)
	}
	if len(closed) != 1 || closed[0] != "good" {
		t.Errorf("Close() closed %v, want [good]", closed)
	}
}

func TestNewEngineFatalFailureClosesCreatedPools(t *testing.T) {
	t.Run("closes and errors", func(t *testing.T) {
		var closed []string
		withBuildEntry(t, func(m Model, opts Options) (modelEntry, func() error, error) {
			if m.ID == "good" {
				return stubEntry("good"), func() error { closed = append(closed, "good"); return nil }, nil
			}
			return modelEntry{}, nil, errors.New("incompatible model")
		})

		e, err := NewEngine(
			stubManifest(stubModel("good", false), stubModel("bad", true)),
			Options{Log: discardLogger()},
		)
		if err == nil {
			t.Fatal("NewEngine() error = nil, want a required model failure")
		}
		if e != nil {
			t.Errorf("NewEngine() engine = %+v, want nil on fatal failure", e)
		}
		if !strings.Contains(err.Error(), `"bad"`) {
			t.Errorf("NewEngine() error = %v, want it to mention %q", err, "bad")
		}
		if len(closed) != 1 || closed[0] != "good" {
			t.Errorf("closers called = %v, want [good]", closed)
		}
	})

	t.Run("close errors joined", func(t *testing.T) {
		boom := errors.New("close boom")
		withBuildEntry(t, func(m Model, opts Options) (modelEntry, func() error, error) {
			if m.ID == "good" {
				return stubEntry("good"), func() error { return boom }, nil
			}
			return modelEntry{}, nil, errors.New("incompatible model")
		})

		_, err := NewEngine(
			stubManifest(stubModel("good", false), stubModel("bad", true)),
			Options{Log: discardLogger()},
		)
		if !errors.Is(err, boom) {
			t.Fatalf("NewEngine() error = %v, want it to join the close error %v", err, boom)
		}
	})
}

func TestNewEngineUnknownModelIDClosesCreatedPools(t *testing.T) {
	boom := errors.New("close boom")
	withBuildEntry(t, func(m Model, opts Options) (modelEntry, func() error, error) {
		return stubEntry(m.ID), func() error { return boom }, nil
	})
	m := stubManifest(stubModel("good", false))
	m.Profiles["balanced"] = Profile{Models: []string{"good", "ghost"}}

	_, err := NewEngine(m, Options{Log: discardLogger()})
	if err == nil {
		t.Fatal("NewEngine() error = nil, want an unknown model error")
	}
	if !strings.Contains(err.Error(), `"ghost"`) {
		t.Errorf("NewEngine() error = %v, want it to mention %q", err, "ghost")
	}
	if !errors.Is(err, boom) {
		t.Errorf("NewEngine() error = %v, want it to join the close error %v", err, boom)
	}
}

func TestNewEngineAllowDegraded(t *testing.T) {
	withBuildEntry(t, func(m Model, opts Options) (modelEntry, func() error, error) {
		if m.ID == "bad" {
			return modelEntry{}, nil, errors.New("incompatible model")
		}
		return stubEntry("good"), func() error { return nil }, nil
	})

	e, err := NewEngine(
		stubManifest(stubModel("bad", true), stubModel("good", false)),
		Options{AllowDegraded: true, Log: discardLogger()},
	)
	if err != nil {
		t.Fatalf("NewEngine() error = %v, want nil with AllowDegraded", err)
	}
	warnings := e.Warnings()
	if len(warnings) != 1 || !strings.Contains(warnings[0], `"bad"`) {
		t.Fatalf("Warnings() = %v, want one mentioning the degraded model %q", warnings, "bad")
	}
	if _, err := e.Analyze(context.Background(), quadPNG(t)); err != nil {
		t.Errorf("Analyze() error = %v, want nil", err)
	}
}

func TestNewEngineNoModelsLoadedFails(t *testing.T) {
	withBuildEntry(t, func(m Model, opts Options) (modelEntry, func() error, error) {
		return modelEntry{}, nil, errors.New("incompatible model")
	})

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	e, err := NewEngine(
		stubManifest(stubModel("only", true)),
		Options{AllowDegraded: true, Log: logger},
	)
	if err == nil {
		t.Fatal("NewEngine() error = nil, want a no-models-loaded error")
	}
	if e != nil {
		t.Errorf("NewEngine() engine = %+v, want nil", e)
	}
	if !strings.Contains(err.Error(), "no models loaded") {
		t.Errorf("NewEngine() error = %v, want it to mention no models loaded", err)
	}
	if !strings.Contains(err.Error(), `"only"`) || !strings.Contains(err.Error(), "incompatible model") {
		t.Errorf("NewEngine() error = %v, want it to carry the per-model failure reason", err)
	}
	if logged := buf.String(); !strings.Contains(logged, "incompatible model") ||
		!strings.Contains(logged, "model=only") {
		t.Errorf("log output = %q, want the per-model failure logged", logged)
	}
}

func TestClose(t *testing.T) {
	e := newEngineFromEntries(nil, "balanced", Options{})
	if err := e.Close(); err != nil {
		t.Errorf("Close() with no closers error = %v, want nil", err)
	}

	boom := errors.New("close failed")
	var calls int
	e.closers = append(e.closers,
		func() error { calls++; return nil },
		func() error { calls++; return boom },
	)
	if err := e.Close(); !errors.Is(err, boom) {
		t.Errorf("Close() error = %v, want it to wrap %v", err, boom)
	}
	if calls != 2 {
		t.Errorf("Close() called %d closers, want 2", calls)
	}
}
