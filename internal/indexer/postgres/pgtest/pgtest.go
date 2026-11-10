// Package pgtest starts one embedded PostgreSQL per test binary (ADR-0038) and hands out
// an empty database per test, so `go test ./...` needs neither docker nor an environment
// variable. It is test support only: no production package imports it.
package pgtest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	"github.com/jackc/pgx/v5"
)

// shared is the one embedded server of the test binary, started on first use.
var shared struct { //nolint:gochecknoglobals // one Postgres per test binary is the point of this helper
	once    sync.Once
	server  *embeddedpostgres.EmbeddedPostgres
	baseURL string
	dataDir string
	err     error
	counter atomic.Int64
}

func start() {
	port, err := freePort()
	if err != nil {
		shared.err = err

		return
	}

	shared.dataDir, err = os.MkdirTemp("", "bwg-pg-*")
	if err != nil {
		shared.err = err

		return
	}

	cfg := embeddedpostgres.DefaultConfig().
		Version(embeddedpostgres.V18).
		Port(port).
		DataPath(filepath.Join(shared.dataDir, "data")).
		RuntimePath(filepath.Join(shared.dataDir, "run")).
		StartTimeout(2 * time.Minute).
		Logger(io.Discard)

	shared.server = embeddedpostgres.NewDatabase(cfg)
	shared.err = shared.server.Start()
	shared.baseURL = fmt.Sprintf("postgres://postgres:postgres@127.0.0.1:%d", port)
}

func freePort() (uint32, error) {
	l, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer func() { _ = l.Close() }()

	addr, ok := l.Addr().(*net.TCPAddr)
	if !ok {
		return 0, errors.New("pgtest: listener has no TCP address")
	}

	return uint32(addr.Port), nil //nolint:gosec // a TCP port fits in 16 bits
}

// NewDatabase returns the URL of a new, empty database on the shared server, started on first use.
func NewDatabase(tb testing.TB) string {
	tb.Helper()
	shared.once.Do(start)

	if shared.err != nil {
		tb.Fatalf("embedded postgres: %v", shared.err)
	}

	ctx := context.Background()

	conn, err := pgx.Connect(ctx, shared.baseURL+"/postgres?sslmode=disable")
	if err != nil {
		tb.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()

	name := "t" + strconv.FormatInt(shared.counter.Add(1), 10) + "_" + strconv.Itoa(os.Getpid())

	_, err = conn.Exec(ctx, "CREATE DATABASE "+name)
	if err != nil {
		tb.Fatal(err)
	}

	return shared.baseURL + "/" + name + "?sslmode=disable"
}

// Stop stops the shared server and removes its files; call it from TestMain after m.Run.
func Stop() {
	if shared.server != nil && shared.err == nil {
		_ = shared.server.Stop()
	}

	if shared.dataDir != "" {
		_ = os.RemoveAll(shared.dataDir)
	}
}
