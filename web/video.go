package web

import (
	"context"
	"errors"
	"fmt"
	"math"
	"mime/multipart"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"nsfw_sherlock/engine"
	"nsfw_sherlock/internal/video"
	"nsfw_sherlock/utils"

	"github.com/gofiber/fiber/v3"
	"golang.org/x/sync/errgroup"
)

const (
	videoMaxWidth      = 1280
	sensitivityMessage = "sensitivity must be between 0.1 and 10"
	videoUnavailable   = "Video scanning is unavailable because ffmpeg is not installed."
	videoNoFile        = "The form must contain one file in the file field."
)

var (
	videoAnalyzeTimeout   = 120 * time.Second
	videoFrameConcurrency = 4
)

type videoFrame struct {
	Index    int             `json:"index"`
	TimeMs   int64           `json:"timeMs"`
	Analysis engine.Analysis `json:"analysis"`
}

type videoSummary struct {
	Verdict          string  `json:"verdict"`
	NSFW             float32 `json:"nsfw"`
	FramesAnalyzed   int     `json:"framesAnalyzed"`
	ExplicitFrames   int     `json:"explicitFrames"`
	SuggestiveFrames int     `json:"suggestiveFrames"`
	ElapsedMs        int64   `json:"elapsedMs"`
}

type videoResponse struct {
	Status  string       `json:"status"`
	Message string       `json:"message"`
	Summary videoSummary `json:"summary"`
	Frames  []videoFrame `json:"frames"`
}

// videoCheck samples frames from an uploaded video and analyzes each frame.
//
// The form field "file" carries the video; the optional "sensitivity" field
// overrides the detection sensitivity for every frame.
func (h *handlers) videoCheck(c fiber.Ctx) error {
	if h.video == nil {
		return utils.ReportError(c, videoUnavailable, fiber.StatusServiceUnavailable)
	}
	file, err := c.FormFile("file")
	if err != nil {
		return utils.ReportError(c, videoNoFile, fiber.StatusBadRequest)
	}
	if err := h.rejectOversizedVideo(c, file); err != nil {
		return err
	}
	sensitivity, ok := sensitivityFromString(c.FormValue("sensitivity"))
	if !ok {
		return utils.ReportError(c, sensitivityMessage, fiber.StatusBadRequest)
	}
	name, err := saveUpload(c, file)
	if err != nil {
		return utils.ReportError(c, "the upload could not be stored", fiber.StatusBadRequest)
	}
	defer func() { _ = os.Remove(name) }()
	ctx, cancel := context.WithTimeout(c.Context(), videoAnalyzeTimeout)
	defer cancel()
	frames, err := h.video.Extract(ctx, name, h.videoLimits)
	if err != nil {
		return utils.ReportError(c, err.Error(), videoExtractStatus(err))
	}
	response, err := h.analyzeFrames(ctx, frames, sensitivity)
	if err != nil {
		return utils.ReportError(c, err.Error(), analyzeErrorStatus(err))
	}
	return c.JSON(response)
}

// picUpload analyzes an image sent as the raw request body.
//
// The optional "sensitivity" query parameter overrides the detection
// sensitivity, like the JSON endpoints do.
func (h *handlers) picUpload(c fiber.Ctx) error {
	data := c.Body()
	if len(data) == 0 {
		return utils.ReportError(c, "the request body must contain an image", fiber.StatusBadRequest)
	}
	sensitivity, ok := sensitivityFromString(c.Query("sensitivity"))
	if !ok {
		return utils.ReportError(c, sensitivityMessage, fiber.StatusBadRequest)
	}
	key := cacheKey("/pic/check", sensitivity, data)
	if h.cache != nil {
		if body, hit := h.cache.get(key); hit {
			return sendCachedJSON(c, body)
		}
	}
	analysis, err := h.analyzeData(c.Context(), data, sensitivity)
	if err != nil {
		return utils.ReportError(c, err.Error(), analyzeErrorStatus(err))
	}
	h.metrics.observeVerdict(analysis.Verdict)
	nsfwText, err := h.textNSFW(c.Context(), data)
	if err != nil {
		return utils.ReportError(c, err.Error(), fiber.StatusInternalServerError)
	}
	return h.sendJSON(c, key, checkResponse{
		Status:   "ok",
		Message:  successMessage,
		NSFWText: nsfwText,
		NSFWPic:  analysis.LegacyNSFW(),
	})
}

func (h *handlers) analyzeFrames(
	ctx context.Context,
	frames []video.Frame,
	sensitivity float64,
) (videoResponse, error) {
	start := time.Now()
	results := make([]videoFrame, len(frames))
	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(videoFrameConcurrency)
	for index, frame := range frames {
		group.Go(func() error {
			analysis, err := h.analyzeData(groupCtx, frame.Data, sensitivity)
			if err != nil {
				return err
			}
			h.metrics.observeVerdict(analysis.Verdict)
			results[index] = videoFrame{Index: frame.Index, TimeMs: frame.TimeMs, Analysis: analysis}
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return videoResponse{}, err
	}
	return videoResponse{
		Status:  "ok",
		Message: successMessage,
		Summary: summarizeVideo(results, time.Since(start)),
		Frames:  results,
	}, nil
}

func summarizeVideo(frames []videoFrame, elapsed time.Duration) videoSummary {
	summary := videoSummary{FramesAnalyzed: len(frames), ElapsedMs: elapsed.Milliseconds()}
	verdict := engine.VerdictSFW
	for _, frame := range frames {
		if frame.Analysis.NSFW > summary.NSFW {
			summary.NSFW = frame.Analysis.NSFW
		}
		switch frame.Analysis.Verdict {
		case engine.VerdictExplicit:
			summary.ExplicitFrames++
			verdict = engine.VerdictExplicit
		case engine.VerdictSuggestive:
			summary.SuggestiveFrames++
			if verdict != engine.VerdictExplicit {
				verdict = engine.VerdictSuggestive
			}
		}
	}
	summary.Verdict = verdict
	return summary
}

func (h *handlers) rejectOversizedVideo(
	c fiber.Ctx,
	file *multipart.FileHeader,
) error {
	limit := h.videoMaxBytes
	if limit <= 0 || file.Size <= limit {
		return nil
	}
	return utils.ReportError(
		c,
		fmt.Sprintf("the video exceeds the %d MiB limit", limit>>20),
		fiber.StatusRequestEntityTooLarge,
	)
}

func saveUpload(c fiber.Ctx, file *multipart.FileHeader) (string, error) {
	input, err := os.CreateTemp("", "nsfw-video-*")
	if err != nil {
		return "", err
	}
	name := input.Name()
	_ = input.Close()
	if err := c.SaveFile(file, name); err != nil {
		_ = os.Remove(name)
		return "", err
	}
	return name, nil
}

func sensitivityFromString(raw string) (float64, bool) {
	if raw == "" {
		return 0, true
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) ||
		value < 0.1 || value > 10 {
		return 0, false
	}
	return value, true
}

func videoExtractStatus(err error) int {
	switch {
	case errors.Is(err, video.ErrFFmpegMissing):
		return fiber.StatusServiceUnavailable
	case errors.Is(err, context.DeadlineExceeded):
		return fiber.StatusGatewayTimeout
	default:
		return fiber.StatusBadRequest
	}
}

func ffmpegAvailable(path string) bool {
	if strings.TrimSpace(path) == "" {
		path = "ffmpeg"
	}
	_, err := exec.LookPath(path)
	return err == nil
}
