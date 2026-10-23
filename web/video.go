package web

import (
	"context"
	"errors"
	"fmt"
	"mime/multipart"
	"os"
	"time"

	"nsfw_sherlock/engine"
	"nsfw_sherlock/internal/video"
	"nsfw_sherlock/utils"

	"github.com/gofiber/fiber/v3"
	"golang.org/x/sync/errgroup"
)

const (
	videoMaxWidth    = 1280
	videoUnavailable = "Video scanning is unavailable because ffmpeg is not installed."
	videoNoFile      = "The form must contain one file in the file field."
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
// The form field "file" carries the video. The response holds one analysis per
// frame and a summary with the worst verdict and the frame counts.
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
	input, err := os.CreateTemp("", "nsfw-video-*")
	if err != nil {
		return utils.ReportError(c, "internal error", fiber.StatusInternalServerError)
	}
	name := input.Name()
	_ = input.Close()
	defer func() { _ = os.Remove(name) }()
	if err := c.SaveFile(file, name); err != nil {
		return utils.ReportError(c, "the upload could not be stored", fiber.StatusBadRequest)
	}
	ctx, cancel := context.WithTimeout(c.Context(), videoAnalyzeTimeout)
	defer cancel()
	frames, err := h.video.Extract(ctx, name, h.videoLimits)
	if err != nil {
		status := fiber.StatusBadRequest
		switch {
		case errors.Is(err, video.ErrFFmpegMissing):
			status = fiber.StatusServiceUnavailable
		case errors.Is(err, context.DeadlineExceeded):
			status = fiber.StatusGatewayTimeout
		}
		return utils.ReportError(c, err.Error(), status)
	}
	start := time.Now()
	results := make([]videoFrame, len(frames))
	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(videoFrameConcurrency)
	for index, frame := range frames {
		group.Go(func() error {
			analysis, err := h.analyzeData(groupCtx, frame.Data)
			if err != nil {
				return err
			}
			results[index] = videoFrame{Index: frame.Index, TimeMs: frame.TimeMs, Analysis: analysis}
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return utils.ReportError(c, err.Error(), analyzeErrorStatus(err))
	}
	return c.JSON(videoResponse{
		Status:  "ok",
		Message: successMessage,
		Summary: summarizeVideo(results, time.Since(start)),
		Frames:  results,
	})
}

// picUpload analyzes an image sent as the raw request body.
func (h *handlers) picUpload(c fiber.Ctx) error {
	data := c.Body()
	if len(data) == 0 {
		return utils.ReportError(c, "the request body must contain an image", fiber.StatusBadRequest)
	}
	key := cacheKey("/pic/check", data)
	if h.cache != nil {
		if body, hit := h.cache.get(key); hit {
			return sendCachedJSON(c, body)
		}
	}
	analysis, err := h.analyzeData(c.Context(), data)
	if err != nil {
		return utils.ReportError(c, err.Error(), analyzeErrorStatus(err))
	}
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
