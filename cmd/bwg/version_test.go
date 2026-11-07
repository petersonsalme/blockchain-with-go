package main

import (
	"bytes"
	"context"
	"log/slog"
	"testing"
)

func runVersion(t *testing.T, args ...string) (string, error) {
	t.Helper()

	var out bytes.Buffer

	err := execute(context.Background(), slog.New(slog.DiscardHandler), args, &out)

	return out.String(), err
}

func TestVersionDefaults(t *testing.T) {
	got, err := runVersion(t, "version")
	if err != nil {
		t.Fatal(err)
	}

	if want := "bwg dev (commit none, built unknown)\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestVersionPrintsBuildInfo(t *testing.T) {
	v, c, d := version, commit, date

	t.Cleanup(func() { version, commit, date = v, c, d })

	version, commit, date = "v9.9.9", "abc123", "2026-11-07T00:00:00Z"

	got, err := runVersion(t, "version")
	if err != nil {
		t.Fatal(err)
	}

	if want := "bwg v9.9.9 (commit abc123, built 2026-11-07T00:00:00Z)\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestVersionRejectsArgs(t *testing.T) {
	_, err := runVersion(t, "version", "x")
	if err == nil {
		t.Fatal("want an error for an extra argument")
	}
}
