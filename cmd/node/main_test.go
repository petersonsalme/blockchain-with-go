package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	_, port, _ := net.SplitHostPort(l.Addr().String())
	return port
}

func TestRunServesAndShutsDownGracefully(t *testing.T) {
	port := freePort(t)
	t.Setenv("PORT", port)
	t.Setenv("POW_DIFFICULTY", "1")
	t.Setenv("MINING_TIMEOUT", "5s")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run(ctx) }()

	url := "http://127.0.0.1:" + port + "/"
	var resp *http.Response
	var err error
	for i := 0; i < 100; i++ {
		resp, err = http.Get(url)
		if err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		cancel()
		t.Fatalf("server did not come up: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET / = %d, want 200", resp.StatusCode)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run returned %v after shutdown, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not return after context cancel")
	}
}

func TestRunFailsOnInvalidPort(t *testing.T) {
	t.Setenv("PORT", "not-a-port")
	t.Setenv("POW_DIFFICULTY", "1")

	if err := run(context.Background()); err == nil {
		t.Fatal("run succeeded with an invalid PORT, want error")
	}
}

func TestFatalIf(t *testing.T) {
	var got []any
	fatal := func(v ...any) { got = v }

	fatalIf(nil, fatal)
	if got != nil {
		t.Fatalf("fatal called for nil error: %v", got)
	}

	boom := errors.New("boom")
	fatalIf(boom, fatal)
	if len(got) != 1 || got[0] != boom {
		t.Fatalf("fatal called with %v, want [boom]", got)
	}
}

func TestShutdownContextDeadline(t *testing.T) {
	ctx, cancel := shutdownContext()
	defer cancel()

	dl, ok := ctx.Deadline()
	if !ok {
		t.Fatal("no deadline")
	}
	if d := time.Until(dl); d < 9*time.Second || d > 10*time.Second {
		t.Fatalf("deadline in %v, want ~10s", d)
	}
}

func TestBootstrapFailsWithoutDotEnv(t *testing.T) {
	t.Chdir(t.TempDir())

	if err := bootstrap(context.Background()); err == nil {
		t.Fatal("bootstrap succeeded without a .env file, want error")
	}
}

func TestBootstrapLoadsDotEnvAndRuns(t *testing.T) {
	port := freePort(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("PORT="+port+"\nPOW_DIFFICULTY=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	// godotenv does not override existing variables; clear them so the file is used.
	os.Unsetenv("PORT")
	os.Unsetenv("POW_DIFFICULTY")
	t.Cleanup(func() { os.Unsetenv("PORT"); os.Unsetenv("POW_DIFFICULTY") })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- bootstrap(ctx) }()

	url := "http://127.0.0.1:" + port + "/"
	up := false
	for i := 0; i < 100 && !up; i++ {
		if resp, err := http.Get(url); err == nil {
			resp.Body.Close()
			up = true
		} else {
			time.Sleep(50 * time.Millisecond)
		}
	}
	cancel()
	if !up {
		t.Fatal("server configured from .env did not come up")
	}
	if err := <-done; err != nil {
		t.Fatalf("bootstrap returned %v, want nil", err)
	}
}
