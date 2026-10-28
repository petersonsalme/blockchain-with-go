package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestDashboardRejectsBadNodes(t *testing.T) {
	tests := []struct{ name, nodes, mention string }{
		{"empty", "", "NODES"},
		{"no equals", "node1", "node1"},
		{"no name", "=http://x", "=http://x"},
		{"duplicate", "a=http://x,a=http://y", `"a"`},
		{"bad name", "A=http://x", "A=http://x"},
		{"scheme", "a=ftp://x", "ftp://x"},
		{"path", "a=http://x/p", "http://x/p"},
		{"query", "a=http://x?q", "http://x?q"},
		{"fragment", "a=http://x#f", "http://x#f"},
		{"userinfo", "a=http://u@x", "http://u@x"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("NODES", tc.nodes)
			t.Setenv("PORT", "0")

			var out bytes.Buffer

			err := execute(context.Background(), slog.New(slog.DiscardHandler), []string{"dashboard"}, &out)
			if err == nil {
				t.Fatal("want error before listening")
			}

			if !strings.Contains(err.Error(), tc.mention) {
				t.Errorf("error %q does not mention %q", err, tc.mention)
			}
		})
	}
}

func TestDashboardRejectsBadPort(t *testing.T) {
	t.Setenv("NODES", "a=http://x")
	t.Setenv("PORT", "not-a-port")

	err := execute(context.Background(), slog.New(slog.DiscardHandler), []string{"dashboard"}, io.Discard)
	if err == nil {
		t.Fatal("want a listen error")
	}
}

func TestServeDashboardServesUntilContextDone(t *testing.T) {
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	go func() {
		done <- serveDashboard(ctx, slog.New(slog.DiscardHandler), ln, "a=http://127.0.0.1:1")
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+ln.Addr().String()+"/", nil)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Security-Policy") == "" {
		t.Errorf("status %d, CSP %q", resp.StatusCode, resp.Header.Get("Content-Security-Policy"))
	}

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("serveDashboard = %v, want nil after shutdown", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serveDashboard did not return after the context ended")
	}
}

func TestDashboardCommandRunsAndStops(t *testing.T) {
	t.Setenv("NODES", "a=http://127.0.0.1:1")
	t.Setenv("PORT", "0")

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	err := execute(ctx, slog.New(slog.DiscardHandler), []string{"dashboard"}, io.Discard)
	if err != nil {
		t.Fatalf("dashboard = %v, want nil on cancel", err)
	}
}
