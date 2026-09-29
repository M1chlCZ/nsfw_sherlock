package logging

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestNewWithWriterRejectsInvalidLevel(t *testing.T) {
	_, err := NewWithWriter(&bytes.Buffer{}, "verbose", "text")
	if err == nil {
		t.Fatal(`NewWithWriter(_, "verbose", "text") = nil error, want error`)
	}
	if !strings.Contains(err.Error(), "verbose") {
		t.Errorf("error %q does not name the invalid level %q", err, "verbose")
	}
}

func TestNewWithWriterRejectsInvalidFormat(t *testing.T) {
	_, err := NewWithWriter(&bytes.Buffer{}, "info", "xml")
	if err == nil {
		t.Fatal(`NewWithWriter(_, "info", "xml") = nil error, want error`)
	}
	if !strings.Contains(err.Error(), "xml") {
		t.Errorf("error %q does not name the invalid format %q", err, "xml")
	}
}

func TestNewWithWriterRejectsInvalidLevelBeforeFormat(t *testing.T) {
	_, err := NewWithWriter(&bytes.Buffer{}, "nope", "nope")
	if err == nil {
		t.Fatal("NewWithWriter(_, \"nope\", \"nope\") = nil error, want error")
	}
	if !strings.Contains(err.Error(), `"nope"`) {
		t.Errorf("error %q does not name the invalid level", err)
	}
}

func TestNewWithWriterAcceptsEveryLevelAndFormat(t *testing.T) {
	for _, level := range []string{"debug", "info", "warn", "error"} {
		for _, format := range []string{"text", "json"} {
			if _, err := NewWithWriter(&bytes.Buffer{}, level, format); err != nil {
				t.Errorf("NewWithWriter(_, %q, %q) error = %v, want nil", level, format, err)
			}
		}
	}
}

func TestLevelMatchingIsCaseSensitive(t *testing.T) {
	if _, err := NewWithWriter(&bytes.Buffer{}, "INFO", "text"); err == nil {
		t.Error(`NewWithWriter(_, "INFO", "text") = nil error, want error`)
	}
	if _, err := NewWithWriter(&bytes.Buffer{}, "info", "JSON"); err == nil {
		t.Error(`NewWithWriter(_, "info", "JSON") = nil error, want error`)
	}
}

func TestInfoLevelSuppressesDebugAndEmitsWarn(t *testing.T) {
	var buf bytes.Buffer
	logger, err := NewWithWriter(&buf, "info", "text")
	if err != nil {
		t.Fatalf("NewWithWriter: %v", err)
	}

	logger.Debug("debug-secret")
	logger.Warn("warn-visible")

	out := buf.String()
	if strings.Contains(out, "debug-secret") {
		t.Errorf("debug record leaked through info level:\n%s", out)
	}
	if !strings.Contains(out, "warn-visible") {
		t.Errorf("warn record missing at info level:\n%s", out)
	}
	if !strings.Contains(out, "level=WARN") {
		t.Errorf("warn record has no WARN level field:\n%s", out)
	}
}

func TestErrorLevelSuppressesWarnAndEmitsError(t *testing.T) {
	var buf bytes.Buffer
	logger, err := NewWithWriter(&buf, "error", "text")
	if err != nil {
		t.Fatalf("NewWithWriter: %v", err)
	}

	logger.Warn("warn-hidden")
	logger.Error("error-visible")

	out := buf.String()
	if strings.Contains(out, "warn-hidden") {
		t.Errorf("warn record leaked through error level:\n%s", out)
	}
	if !strings.Contains(out, "error-visible") {
		t.Errorf("error record missing at error level:\n%s", out)
	}
	if !strings.Contains(out, "level=ERROR") {
		t.Errorf("error record has no ERROR level field:\n%s", out)
	}
}

func TestJSONOutputParsesAndCarriesLevelAndMsg(t *testing.T) {
	var buf bytes.Buffer
	logger, err := NewWithWriter(&buf, "debug", "json")
	if err != nil {
		t.Fatalf("NewWithWriter: %v", err)
	}

	logger.Info("picture check", "nsfw", true)

	line := strings.TrimSpace(buf.String())
	if line == "" {
		t.Fatal("json handler wrote nothing")
	}
	var rec struct {
		Level string `json:"level"`
		Msg   string `json:"msg"`
	}
	if err := json.Unmarshal([]byte(line), &rec); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, line)
	}
	if rec.Level != "INFO" {
		t.Errorf("level = %q, want %q", rec.Level, "INFO")
	}
	if rec.Msg != "picture check" {
		t.Errorf("msg = %q, want %q", rec.Msg, "picture check")
	}
}

func TestTextOutputContainsMsg(t *testing.T) {
	var buf bytes.Buffer
	logger, err := NewWithWriter(&buf, "debug", "text")
	if err != nil {
		t.Fatalf("NewWithWriter: %v", err)
	}

	logger.Info("picture check")

	if !strings.Contains(buf.String(), "picture check") {
		t.Errorf("text output does not contain msg:\n%s", buf.String())
	}
}

func TestNewReturnsLogger(t *testing.T) {
	logger, err := New("info", "json")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if logger == nil {
		t.Fatal("New returned nil logger with nil error")
	}
}
