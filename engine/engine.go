package engine

import (
	"context"
	"errors"
	"fmt"
	"image"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
)

const maxEngineWarnings = 64

type classifierRunner interface {
	run(ctx context.Context, img image.Image) (map[string]float32, error)
}

type detectorRunner interface {
	run(ctx context.Context, img image.Image) ([]Detection, error)
}

type modelEntry struct {
	model      Model
	classifier classifierRunner
	detector   detectorRunner
}

// Engine owns the model sessions for one manifest profile and combines their outputs into an Analysis.
type Engine struct {
	entries []modelEntry
	profile string
	opts    Options
	rules   DetectionRules

	mu              sync.Mutex
	warnings        []string
	warnedModels    map[string]struct{}
	droppedWarnings int
	closers         []func() error
}

var buildEntryFn = buildEntry

// NewEngine verifies and loads every model in the resolved profile, creating one session pool per model.
func NewEngine(m *Manifest, opts Options) (*Engine, error) {
	if m == nil {
		return nil, errors.New("engine: nil manifest")
	}
	opts = opts.withDefaults()
	ids, err := m.Profile(opts.Profile)
	if err != nil {
		return nil, err
	}

	type pendingWarning struct{ model, message string }
	var (
		entries  []modelEntry
		closers  []func() error
		warnings []pendingWarning
	)
	closeCreated := func() error {
		var errs []error
		for _, close := range closers {
			if err := close(); err != nil {
				errs = append(errs, err)
			}
		}
		return errors.Join(errs...)
	}

	for _, id := range ids {
		model, err := m.Model(id)
		if err != nil {
			if closeErr := closeCreated(); closeErr != nil {
				return nil, errors.Join(err, closeErr)
			}
			return nil, err
		}
		entry, closeFn, err := buildEntryFn(model, opts)
		if err != nil {
			if model.Required && !opts.AllowDegraded {
				err = fmt.Errorf("engine: model %q: %w", id, err)
				if closeErr := closeCreated(); closeErr != nil {
					return nil, errors.Join(err, closeErr)
				}
				return nil, err
			}
			warnings = append(warnings, pendingWarning{model: id, message: fmt.Sprintf("engine: model %q: %v", id, err)})
			continue
		}
		entries = append(entries, entry)
		if closeFn != nil {
			closers = append(closers, closeFn)
		}
	}

	if len(entries) == 0 {
		reasons := make([]error, 0, len(warnings))
		for _, warning := range warnings {
			if opts.Log != nil {
				opts.Log.Warn(warning.message, "model", warning.model)
			}
			reasons = append(reasons, errors.New(warning.message))
		}
		var noModels error
		if joined := errors.Join(reasons...); joined != nil {
			noModels = fmt.Errorf("engine: profile %q: no models loaded: %w", opts.Profile, joined)
		} else {
			noModels = fmt.Errorf("engine: profile %q: no models loaded", opts.Profile)
		}
		if closeErr := closeCreated(); closeErr != nil {
			return nil, errors.Join(noModels, closeErr)
		}
		return nil, noModels
	}

	e, err := newEngineFromEntries(entries, opts.Profile, opts)
	if err != nil {
		_ = closeCreated()
		return nil, err
	}
	e.closers = closers
	for _, warning := range warnings {
		e.addWarning(warning.model, warning.message)
	}
	return e, nil
}

func newEngineFromEntries(entries []modelEntry, profile string, opts Options) (*Engine, error) {
	return &Engine{
		entries:      entries,
		profile:      profile,
		opts:         opts.withDefaults(),
		rules:        deriveRules(entries),
		warnedModels: make(map[string]struct{}),
	}, nil
}

func buildEntry(model Model, opts Options) (modelEntry, func() error, error) {
	path := filepath.Join(opts.ModelsDir, model.File)
	if opts.verifySHA() {
		if err := VerifyFile(path, model.SHA256); err != nil {
			return modelEntry{}, nil, err
		}
	}
	pool, err := newModelPool(path, model.InputNames, model.OutputNames, opts.PoolSize)
	if err != nil {
		return modelEntry{}, nil, err
	}
	closeFn := func() error {
		pool.close()
		return nil
	}

	entry := modelEntry{model: model}
	switch model.Kind {
	case KindClassifier:
		c, err := newClassifier(model, pool)
		if err != nil {
			pool.close()
			return modelEntry{}, nil, err
		}
		entry.classifier = c
	case KindDetector:
		d, err := newDetector(model, pool)
		if err != nil {
			pool.close()
			return modelEntry{}, nil, err
		}
		entry.detector = d
	default:
		pool.close()
		return modelEntry{}, nil, fmt.Errorf("unknown model kind %q", model.Kind)
	}
	return entry, closeFn, nil
}

func deriveRules(entries []modelEntry) DetectionRules {
	var rules DetectionRules
	explicit := make(map[string]struct{})
	suggestive := make(map[string]struct{})
	for _, entry := range entries {
		if entry.model.Kind != KindDetector || entry.model.Detector == nil {
			continue
		}
		for _, class := range entry.model.Detector.ExplicitClasses {
			if _, ok := explicit[class]; ok {
				continue
			}
			explicit[class] = struct{}{}
			rules.Explicit = append(rules.Explicit, class)
		}
		for _, class := range entry.model.Detector.SuggestiveClasses {
			if _, ok := suggestive[class]; ok {
				continue
			}
			suggestive[class] = struct{}{}
			rules.Suggestive = append(rules.Suggestive, class)
		}
	}
	return rules
}

// Analyze decodes data, runs every loaded model concurrently, and combines their outputs.
func (e *Engine) Analyze(ctx context.Context, data []byte) (Analysis, error) {
	if err := ctx.Err(); err != nil {
		return Analysis{}, err
	}
	start := time.Now()
	img, err := decodeImage(data, e.opts.Limits)
	if err != nil {
		return Analysis{}, err
	}

	type runResult struct {
		ok     bool
		scores map[string]float32
		dets   []Detection
	}
	results := make([]runResult, len(e.entries))
	g, gctx := errgroup.WithContext(ctx)
	for i, entry := range e.entries {
		i, entry := i, entry
		g.Go(func() error {
			if entry.classifier != nil {
				scores, err := entry.classifier.run(gctx, img)
				if err != nil {
					return e.runError(ctx, entry, err, gctx)
				}
				results[i].scores = scores
				results[i].ok = true
				return nil
			}
			if entry.detector != nil {
				found, err := entry.detector.run(gctx, img)
				if err != nil {
					return e.runError(ctx, entry, err, gctx)
				}
				results[i].dets = found
				results[i].ok = true
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return Analysis{}, err
	}
	if err := ctx.Err(); err != nil {
		return Analysis{}, err
	}

	var outputs []ModelOutput
	for i := len(e.entries) - 1; i >= 0; i-- {
		entry := e.entries[i]
		if entry.classifier == nil || !results[i].ok {
			continue
		}
		outputs = append(outputs, ModelOutput{
			ID:     entry.model.ID,
			Kind:   entry.model.Kind,
			Role:   entry.model.Role,
			Scores: results[i].scores,
		})
	}
	var dets []Detection
	for i := range e.entries {
		if !results[i].ok {
			continue
		}
		dets = append(dets, results[i].dets...)
	}

	analysis := Combine(e.profile, outputs, dets, e.rules)
	analysis.Models = make([]ModelInfo, 0, len(e.entries))
	for _, entry := range e.entries {
		analysis.Models = append(analysis.Models, ModelInfo{
			ID:     entry.model.ID,
			Kind:   entry.model.Kind,
			SHA256: entry.model.SHA256,
		})
	}
	analysis.ElapsedMs = time.Since(start).Milliseconds()
	return analysis, nil
}

func (e *Engine) runError(parent context.Context, entry modelEntry, err error, gctx context.Context) error {
	if entry.model.Required || parent.Err() != nil {
		return fmt.Errorf("engine: model %q: %w", entry.model.ID, err)
	}
	if gctx.Err() == nil {
		e.addWarning(entry.model.ID, fmt.Sprintf("engine: model %q: %v", entry.model.ID, err))
	}
	return nil
}

// Close closes every model pool, aggregating any close errors.
func (e *Engine) Close() error {
	errs := make([]error, 0, len(e.closers))
	for _, close := range e.closers {
		if err := close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Warnings returns a copy of the warnings recorded during loading and analysis.
func (e *Engine) Warnings() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := append([]string(nil), e.warnings...)
	if e.droppedWarnings > 0 {
		out = append(out, fmt.Sprintf("engine: %d warnings dropped", e.droppedWarnings))
	}
	return out
}

// Profile returns the name of the manifest profile the Engine loaded.
func (e *Engine) Profile() string {
	return e.profile
}

func (e *Engine) addWarning(modelID, message string) {
	e.mu.Lock()
	if modelID != "" {
		if _, seen := e.warnedModels[modelID]; seen {
			e.mu.Unlock()
			return
		}
		e.warnedModels[modelID] = struct{}{}
	}
	if len(e.warnings) < maxEngineWarnings {
		e.warnings = append(e.warnings, message)
	} else {
		e.droppedWarnings++
	}
	e.mu.Unlock()

	if e.opts.Log != nil {
		e.opts.Log.Warn(message, "model", modelID)
	}
}
