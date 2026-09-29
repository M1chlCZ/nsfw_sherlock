package utils

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
)

func TestReportErrorLegacyShape(t *testing.T) {
	tests := []struct {
		name       string
		err        string
		statusCode int
	}{
		{"bad request", "Bad Request", fiber.StatusBadRequest},
		{"internal", "decode failed", fiber.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := fiber.New()
			app.Get("/boom", func(c fiber.Ctx) error {
				return ReportError(c, tt.err, tt.statusCode)
			})

			resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/boom", nil))
			if err != nil {
				t.Fatalf("app.Test: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()

			if resp.StatusCode != tt.statusCode {
				t.Errorf("status = %d, want %d", resp.StatusCode, tt.statusCode)
			}
			if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
				t.Errorf("Content-Type = %q, want application/json", ct)
			}

			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("read body: %v", err)
			}

			want := `{"errorMessage":` + strconvQuote(t, tt.err) + `,"status":"FAIL","hasError":true}`
			if got := string(body); got != want {
				t.Errorf("body = %s, want %s", got, want)
			}

			var fields map[string]any
			if err := json.Unmarshal(body, &fields); err != nil {
				t.Fatalf("body is not valid JSON: %v", err)
			}
			if len(fields) != 3 {
				t.Errorf("body has %d fields, want exactly 3: %v", len(fields), fields)
			}
			if fields["errorMessage"] != tt.err {
				t.Errorf("errorMessage = %v, want %q", fields["errorMessage"], tt.err)
			}
			if fields["status"] != "FAIL" {
				t.Errorf("status = %v, want %q", fields["status"], "FAIL")
			}
			if fields["hasError"] != true {
				t.Errorf("hasError = %v, want true", fields["hasError"])
			}
		})
	}
}

func strconvQuote(t *testing.T, s string) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal %q: %v", s, err)
	}
	return string(b)
}
