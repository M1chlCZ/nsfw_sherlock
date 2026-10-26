package web

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"nsfw_sherlock/utils"

	"github.com/gofiber/fiber/v3"
)

const (
	jobListDefaultLimit = 100
	jobListMaxLimit     = 1000
	sseKeepAlive        = 15 * time.Second
)

type jobResponse struct {
	Status  string  `json:"status"`
	Message string  `json:"message"`
	Job     jobView `json:"job"`
}

type jobListResponse struct {
	Status  string         `json:"status"`
	Message string         `json:"message"`
	Jobs    []jobView      `json:"jobs"`
	Counts  map[string]int `json:"counts"`
}

// submitJob starts a background analysis.
//
// A JSON body starts a batch job with the same shape as /pic/batch; a
// multipart body starts a video job with the file in the "file" field.
func (h *handlers) submitJob(c fiber.Ctx) error {
	if strings.HasPrefix(c.Get(fiber.HeaderContentType), fiber.MIMEMultipartForm) {
		return h.submitVideoJob(c)
	}
	return h.submitBatchJob(c)
}

func (h *handlers) submitBatchJob(c fiber.Ctx) error {
	var req batchRequest
	if err := c.Bind().JSON(&req); err != nil {
		return utils.ReportError(c, err.Error(), fiber.StatusBadRequest)
	}
	if len(req.Images) == 0 {
		return utils.ReportError(c, "no images in the batch", fiber.StatusBadRequest)
	}
	if len(req.Images) > h.batchMax {
		return utils.ReportError(
			c,
			fmt.Sprintf("the batch limit is %d images", h.batchMax),
			fiber.StatusBadRequest,
		)
	}
	view, err := h.jobs.submit("batch", func(ctx context.Context) (any, error) {
		results := h.analyzeBatch(ctx, req.Images)
		return results, ctx.Err()
	})
	if err != nil {
		return utils.ReportError(c, err.Error(), fiber.StatusTooManyRequests)
	}
	return c.Status(fiber.StatusAccepted).JSON(jobResponse{
		Status:  "ok",
		Message: successMessage,
		Job:     view,
	})
}

func (h *handlers) submitVideoJob(c fiber.Ctx) error {
	if h.video == nil {
		return utils.ReportError(c, videoUnavailable, fiber.StatusServiceUnavailable)
	}
	file, err := c.FormFile("file")
	if err != nil {
		return utils.ReportError(c, videoNoFile, fiber.StatusBadRequest)
	}
	sensitivity, ok := sensitivityFromString(c.FormValue("sensitivity"))
	if !ok {
		return utils.ReportError(c, sensitivityMessage, fiber.StatusBadRequest)
	}
	name, err := saveUpload(c, file)
	if err != nil {
		return utils.ReportError(c, "the upload could not be stored", fiber.StatusBadRequest)
	}
	view, err := h.jobs.submitWithCleanup("video", func(ctx context.Context) (any, error) {
		ctx, cancel := context.WithTimeout(ctx, videoAnalyzeTimeout)
		defer cancel()
		frames, err := h.video.Extract(ctx, name, h.videoLimits)
		if err != nil {
			return nil, err
		}
		return h.analyzeFrames(ctx, frames, sensitivity)
	}, func() { _ = os.Remove(name) })
	if err != nil {
		_ = os.Remove(name)
		return utils.ReportError(c, err.Error(), fiber.StatusTooManyRequests)
	}
	return c.Status(fiber.StatusAccepted).JSON(jobResponse{
		Status:  "ok",
		Message: successMessage,
		Job:     view,
	})
}

func (h *handlers) listJobs(c fiber.Ctx) error {
	state := c.Query("state")
	if state != "" && !validJobState(state) {
		return utils.ReportError(
			c,
			fmt.Sprintf("unknown job state %q", state),
			fiber.StatusBadRequest,
		)
	}
	limit := jobListDefaultLimit
	if raw := c.Query("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 {
			return utils.ReportError(c, "limit must be a positive integer", fiber.StatusBadRequest)
		}
		limit = min(parsed, jobListMaxLimit)
	}
	jobs, counts := h.jobs.list(state, limit)
	return c.JSON(jobListResponse{
		Status:  "ok",
		Message: successMessage,
		Jobs:    jobs,
		Counts:  counts,
	})
}

func (h *handlers) jobEvents(c fiber.Ctx) error {
	events, unsubscribe, err := h.jobs.subscribe(c.Params("id"))
	if errors.Is(err, errJobNotFound) {
		return utils.ReportError(c, errJobNotFound.Error(), fiber.StatusNotFound)
	}
	if err != nil {
		return utils.ReportError(c, "internal error", fiber.StatusInternalServerError)
	}

	c.Set(fiber.HeaderContentType, "text/event-stream")
	c.Set(fiber.HeaderCacheControl, "no-cache")
	c.Set(fiber.HeaderConnection, "keep-alive")
	c.Set("X-Accel-Buffering", "no")
	c.Status(fiber.StatusOK)

	err = c.SendStreamWriter(func(w *bufio.Writer) {
		defer unsubscribe()
		if _, err := w.WriteString("retry: 3000\n\n"); err != nil {
			return
		}
		if err := w.Flush(); err != nil {
			return
		}
		ticker := time.NewTicker(sseKeepAlive)
		defer ticker.Stop()
		for {
			select {
			case view, ok := <-events:
				if !ok {
					return
				}
				if err := writeJobEvent(w, view); err != nil {
					return
				}
				if isTerminalJobState(view.State) {
					return
				}
			case <-ticker.C:
				if _, err := w.WriteString(": keepalive\n\n"); err != nil {
					return
				}
				if err := w.Flush(); err != nil {
					return
				}
			}
		}
	})
	if err != nil {
		unsubscribe()
		return err
	}
	return nil
}

func writeJobEvent(w *bufio.Writer, view jobView) error {
	body, err := json.Marshal(jobResponse{Status: "ok", Message: successMessage, Job: view})
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", view.State, body); err != nil {
		return err
	}
	return w.Flush()
}

func validJobState(state string) bool {
	for _, name := range jobStates {
		if state == name {
			return true
		}
	}
	return false
}

func (h *handlers) getJob(c fiber.Ctx) error {
	view, err := h.jobs.get(c.Params("id"))
	if errors.Is(err, errJobNotFound) {
		return utils.ReportError(c, errJobNotFound.Error(), fiber.StatusNotFound)
	}
	return c.JSON(jobResponse{Status: "ok", Message: successMessage, Job: view})
}

func (h *handlers) cancelJob(c fiber.Ctx) error {
	err := h.jobs.cancel(c.Params("id"))
	switch {
	case errors.Is(err, errJobNotFound):
		return utils.ReportError(c, errJobNotFound.Error(), fiber.StatusNotFound)
	case err != nil:
		return utils.ReportError(c, err.Error(), fiber.StatusConflict)
	default:
		return c.JSON(fiber.Map{statusField: "ok", "message": successMessage})
	}
}
