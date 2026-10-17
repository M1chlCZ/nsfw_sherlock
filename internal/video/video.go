// Package video extracts sample frames from video files with ffmpeg.
package video

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Errors reported by the extractor.
var (
	// ErrFFmpegMissing reports that the ffmpeg binary is not installed.
	ErrFFmpegMissing = errors.New("ffmpeg is not installed")
	// ErrNoFrames reports that ffmpeg produced no frames.
	ErrNoFrames = errors.New("no frames were extracted")
)

// Limits bound one extraction.
type Limits struct {
	// FPS is the number of frames sampled per second.
	FPS float64
	// MaxFrames caps the number of frames.
	MaxFrames int
	// MaxWidth scales wider frames down to this width.
	MaxWidth int
}

// Frame is one decoded sample of a video.
type Frame struct {
	// Index counts frames from one.
	Index int
	// TimeMs is the position of the frame in the video, in milliseconds.
	TimeMs int64
	// Data holds the encoded PNG image.
	Data []byte
}

// Extractor turns a video file into sample frames.
type Extractor interface {
	Extract(ctx context.Context, path string, limits Limits) ([]Frame, error)
}

// FFmpeg extracts frames by running the ffmpeg binary.
type FFmpeg struct {
	// Path is the ffmpeg binary; "ffmpeg" by default.
	Path string
}

// New returns an FFmpeg extractor; an empty path means ffmpeg from PATH.
func New(path string) *FFmpeg {
	if strings.TrimSpace(path) == "" {
		path = "ffmpeg"
	}
	return &FFmpeg{Path: path}
}

// Extract decodes the video at path into at most Limits.MaxFrames PNG frames.
//
// The extractor samples Limits.FPS frames per second, scales frames down to
// Limits.MaxWidth, and returns the frames in time order.
func (f *FFmpeg) Extract(ctx context.Context, path string, limits Limits) ([]Frame, error) {
	if _, err := exec.LookPath(f.Path); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrFFmpegMissing, f.Path)
	}
	limits = normalize(limits)
	dir, err := os.MkdirTemp("", "nsfw-frames-*")
	if err != nil {
		return nil, fmt.Errorf("video: cannot create the frame directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	command := exec.CommandContext(ctx, f.Path,
		"-v", "error",
		"-nostdin",
		"-i", path,
		"-vf", fmt.Sprintf(
			"fps=%s,scale=w='min(iw,%d)':h=-2",
			formatNumber(limits.FPS),
			limits.MaxWidth,
		),
		"-frames:v", strconv.Itoa(limits.MaxFrames),
		filepath.Join(dir, "frame-%04d.png"),
	)
	var stderr strings.Builder
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return nil, fmt.Errorf("video: ffmpeg: %s", message)
	}
	names, err := filepath.Glob(filepath.Join(dir, "frame-*.png"))
	if err != nil {
		return nil, fmt.Errorf("video: cannot list the frames: %w", err)
	}
	sort.Strings(names)
	frames := make([]Frame, 0, len(names))
	for index, name := range names {
		data, err := os.ReadFile(name)
		if err != nil {
			return nil, fmt.Errorf("video: cannot read a frame: %w", err)
		}
		frames = append(frames, Frame{
			Index:  index + 1,
			TimeMs: int64(float64(index) / limits.FPS * 1000),
			Data:   data,
		})
	}
	if len(frames) == 0 {
		return nil, ErrNoFrames
	}
	return frames, nil
}

func normalize(limits Limits) Limits {
	if limits.FPS <= 0 {
		limits.FPS = 1
	}
	if limits.MaxFrames <= 0 {
		limits.MaxFrames = 24
	}
	if limits.MaxWidth <= 0 {
		limits.MaxWidth = 1280
	}
	return limits
}

func formatNumber(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}
