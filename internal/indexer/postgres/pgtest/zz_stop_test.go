package pgtest_test

import (
	"context"
	"net"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/petersonsalme/blockchain-with-go/internal/indexer/postgres/pgtest"
)

// TestStopStopsTheServerAndRemovesItsFiles runs last (file order): it shuts the shared
// server down, so no test after it may use NewDatabase.
func TestStopStopsTheServerAndRemovesItsFiles(t *testing.T) {
	url := pgtest.NewDatabase(t)
	dir := pgtest.DataDir()

	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("data dir before Stop: %v", err)
	}

	pgtest.Stop()

	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("data dir still there after Stop: %v", err)
	}

	cfg, err := pgx.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}

	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(int(cfg.Port)))

	c, err := (&net.Dialer{Timeout: 2 * time.Second}).DialContext(context.Background(), "tcp", addr)
	if err == nil {
		_ = c.Close()
		t.Fatal("server still listens after Stop")
	}
}
