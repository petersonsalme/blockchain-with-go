package server

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/petersonsalme/blockchain-with-go/internal/chain"
	"github.com/petersonsalme/blockchain-with-go/internal/consensus/pow"
	"github.com/petersonsalme/blockchain-with-go/internal/storage/memory"
)

type logRecord struct {
	Level      string `json:"level"`
	Msg        string `json:"msg"`
	Method     string `json:"method"`
	Route      string `json:"route"`
	Status     int    `json:"status"`
	DurationMS *int64 `json:"duration_ms"`
}

func decodeLog(t *testing.T, buf *bytes.Buffer) []logRecord {
	t.Helper()

	var out []logRecord

	for line := range strings.SplitSeq(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}

		var r logRecord

		err := json.Unmarshal([]byte(line), &r)
		if err != nil {
			t.Fatalf("log line %q is not JSON: %v", line, err)
		}

		out = append(out, r)
	}

	return out
}

func TestRequestLogging(t *testing.T) {
	svc, err := chain.NewService(memory.New(), pow.NewProofOfWork(1))
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name       string
		method     string
		path       string
		rateLimit  int
		wantRoute  string
		wantStatus int
	}{
		{"ok", http.MethodGet, "/", 0, "GET /{$}", http.StatusOK},
		{"pattern not raw path", http.MethodGet, "/block/999", 0, "GET /block/{ref}", http.StatusNotFound},
		{"no route", http.MethodGet, "/nope/123", 0, "unmatched", http.StatusNotFound},
		{"method not allowed", http.MethodPut, "/blocks", 0, "/blocks", http.StatusMethodNotAllowed},
		{"rate limited", http.MethodGet, "/health", 1, "unmatched", http.StatusTooManyRequests},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer

			srv, err := NewServer(svc, Config{
				Logger:    slog.New(slog.NewJSONHandler(&buf, nil)),
				RateLimit: tc.rateLimit,
			})
			if err != nil {
				t.Fatal(err)
			}

			h := srv.Handler()

			var rec *httptest.ResponseRecorder

			serve := func() {
				req := httptest.NewRequestWithContext(context.Background(), tc.method, tc.path, nil)
				req.RemoteAddr = "192.0.2.1:1234"
				rec = httptest.NewRecorder()
				h.ServeHTTP(rec, req)
			}
			serve()

			if tc.rateLimit > 0 {
				buf.Reset()
				serve() // the first request spends the only token
			}

			logs := decodeLog(t, &buf)
			if len(logs) != 1 {
				t.Fatalf("got %d records, want exactly 1: %q", len(logs), buf.String())
			}

			got := logs[0]
			if got.Level != "INFO" || got.Msg != "http request" {
				t.Errorf("level/msg = %s/%q, want INFO/\"http request\"", got.Level, got.Msg)
			}

			if got.Method != tc.method {
				t.Errorf("method = %q, want %q", got.Method, tc.method)
			}

			if got.Route != tc.wantRoute {
				t.Errorf("route = %q, want %q", got.Route, tc.wantRoute)
			}

			if got.Status != tc.wantStatus || rec.Code != tc.wantStatus {
				t.Errorf("logged status %d, client status %d, want %d", got.Status, rec.Code, tc.wantStatus)
			}

			if got.DurationMS == nil || *got.DurationMS < 0 {
				t.Errorf("duration_ms = %v, want a value >= 0", got.DurationMS)
			}
		})
	}
}

func TestRequestLoggingStatusRecorder(t *testing.T) {
	serve := func(h http.HandlerFunc) logRecord {
		var buf bytes.Buffer
		logRequests(slog.New(slog.NewJSONHandler(&buf, nil)), h).
			ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil))

		logs := decodeLog(t, &buf)
		if len(logs) != 1 {
			t.Fatalf("got %d records, want 1", len(logs))
		}

		return logs[0]
	}

	t.Run("write without WriteHeader is 200", func(t *testing.T) {
		got := serve(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("x")) })
		if got.Status != http.StatusOK {
			t.Errorf("status = %d, want 200", got.Status)
		}
	})
	t.Run("no write at all is 200", func(t *testing.T) {
		if got := serve(func(http.ResponseWriter, *http.Request) {}); got.Status != http.StatusOK {
			t.Errorf("status = %d, want 200", got.Status)
		}
	})
	t.Run("first WriteHeader wins", func(t *testing.T) {
		got := serve(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusTeapot)
			w.WriteHeader(http.StatusInternalServerError)
		})
		if got.Status != http.StatusTeapot {
			t.Errorf("status = %d, want 418", got.Status)
		}
	})
	t.Run("WriteHeader after Write is ignored", func(t *testing.T) {
		got := serve(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("x"))
			w.WriteHeader(http.StatusInternalServerError)
		})
		if got.Status != http.StatusOK {
			t.Errorf("status = %d, want 200", got.Status)
		}
	})
	t.Run("Unwrap exposes the underlying writer", func(t *testing.T) {
		rec := httptest.NewRecorder()
		if (&statusRecorder{ResponseWriter: rec}).Unwrap() != http.ResponseWriter(rec) {
			t.Error("Unwrap did not return the wrapped writer")
		}
	})
}

func TestNilLoggerDiscards(t *testing.T) {
	srv, err := NewServer(brokenChain{}, Config{})
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/health", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}
