package video

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func writeScript(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ffmpeg")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	return path
}

const frameScript = `out=""
for arg in "$@"; do out="$arg"; done
dir=$(dirname "$out")
printf 'frame-one' > "$dir/frame-0001.png"
printf 'frame-two' > "$dir/frame-0002.png"
`

func TestExtractReadsFramesInOrder(t *testing.T) {
	extractor := New(writeScript(t, frameScript))
	frames, err := extractor.Extract(context.Background(), "clip.mp4", Limits{FPS: 2, MaxFrames: 10})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(frames) != 2 {
		t.Fatalf("frames = %d, want 2", len(frames))
	}
	if frames[0].Index != 1 || frames[1].Index != 2 {
		t.Fatalf("indices = %d, %d; want 1, 2", frames[0].Index, frames[1].Index)
	}
	if frames[0].TimeMs != 0 || frames[1].TimeMs != 500 {
		t.Fatalf("times = %d, %d; want 0, 500", frames[0].TimeMs, frames[1].TimeMs)
	}
	if string(frames[0].Data) != "frame-one" || string(frames[1].Data) != "frame-two" {
		t.Fatalf("data = %q, %q", frames[0].Data, frames[1].Data)
	}
}

func TestExtractReportsMissingFFmpeg(t *testing.T) {
	extractor := New(filepath.Join(t.TempDir(), "missing-ffmpeg"))
	_, err := extractor.Extract(context.Background(), "clip.mp4", Limits{})
	if !errors.Is(err, ErrFFmpegMissing) {
		t.Fatalf("err = %v, want ErrFFmpegMissing", err)
	}
}

func TestExtractReportsFFmpegFailure(t *testing.T) {
	extractor := New(writeScript(t, "echo boom >&2\nexit 1\n"))
	_, err := extractor.Extract(context.Background(), "clip.mp4", Limits{})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v, want the stderr text", err)
	}
	if errors.Is(err, ErrFFmpegMissing) {
		t.Fatalf("failure reported as a missing ffmpeg")
	}
}

func TestExtractReportsNoFrames(t *testing.T) {
	extractor := New(writeScript(t, "exit 0\n"))
	_, err := extractor.Extract(context.Background(), "clip.mp4", Limits{})
	if !errors.Is(err, ErrNoFrames) {
		t.Fatalf("err = %v, want ErrNoFrames", err)
	}
}

func TestNormalizeFillsDefaults(t *testing.T) {
	limits := normalize(Limits{})
	if limits.FPS != 1 || limits.MaxFrames != 24 || limits.MaxWidth != 1280 {
		t.Fatalf("normalize = %+v", limits)
	}
}

func TestExtractRunsRealFFmpeg(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	clip := filepath.Join(t.TempDir(), "clip.mp4")
	command := exec.Command(ffmpeg, "-v", "error", "-f", "lavfi", "-i",
		"testsrc=duration=2:size=320x240:rate=5", "-pix_fmt", "yuv420p", clip)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("create clip: %v: %s", err, output)
	}
	frames, err := New(ffmpeg).Extract(context.Background(), clip, Limits{FPS: 1, MaxFrames: 4})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(frames) == 0 || len(frames) > 4 {
		t.Fatalf("frames = %d, want 1..4", len(frames))
	}
	if len(frames[0].Data) == 0 {
		t.Fatal("the first frame is empty")
	}
	if frames[0].TimeMs != 0 {
		t.Fatalf("first frame time = %d, want 0", frames[0].TimeMs)
	}
}
