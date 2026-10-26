package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/petersonsalme/blockchain-with-go/internal/consensus/pow"
	"github.com/petersonsalme/blockchain-with-go/internal/domain"
	"github.com/petersonsalme/blockchain-with-go/internal/storage"
	"github.com/petersonsalme/blockchain-with-go/internal/storage/bolt"
	"github.com/petersonsalme/blockchain-with-go/internal/storage/memory"
	"github.com/petersonsalme/blockchain-with-go/pkg/wallet"
)

func freePort(t *testing.T) string {
	t.Helper()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = l.Close() }() // test listener; close error is irrelevant

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
	go func() { done <- run(ctx, discardLogger()) }()

	url := "http://127.0.0.1:" + port + "/"

	var (
		resp *http.Response
		err  error
	)
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

	_ = resp.Body.Close() // response body of a test request; close error is irrelevant
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

	if err := run(context.Background(), discardLogger()); err == nil {
		t.Fatal("run succeeded with an invalid PORT, want error")
	}
}

func discardLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

func TestFatalIf(t *testing.T) {
	var buf bytes.Buffer

	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	code := -1
	exit := func(c int) { code = c }

	fatalIf(nil, logger, exit)

	if code != -1 || buf.Len() != 0 {
		t.Fatalf("nil error: exit code %d, log %q; want no exit and no log", code, buf.String())
	}

	fatalIf(errors.New("boom"), logger, exit)

	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}

	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatalf("log is not JSON: %v: %q", err, buf.String())
	}

	if rec["level"] != "ERROR" || rec["error"] != "boom" {
		t.Fatalf("log = %v, want level ERROR and error boom", rec)
	}
}

func TestRunLogsListeningAddress(t *testing.T) {
	port := freePort(t)
	t.Setenv("PORT", port)
	t.Setenv("POW_DIFFICULTY", "1")

	var buf syncBuffer

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() { done <- run(ctx, slog.New(slog.NewJSONHandler(&buf, nil))) }()

	want := `"addr":":` + port + `"`
	for i := 0; i < 100 && !strings.Contains(buf.String(), want); i++ {
		time.Sleep(50 * time.Millisecond)
	}

	cancel()

	if err := <-done; err != nil {
		t.Fatalf("run returned %v, want nil", err)
	}

	if got := buf.String(); !strings.Contains(got, `"msg":"listening"`) || !strings.Contains(got, want) {
		t.Fatalf("log = %q, want a listening record with %s", got, want)
	}
}

// syncBuffer is a bytes.Buffer safe for the run goroutine and the test to share.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.String()
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

	if err := bootstrap(context.Background(), discardLogger()); err == nil {
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
	// Unsetenv only fails for an invalid name; these names are constants.
	_ = os.Unsetenv("PORT")
	_ = os.Unsetenv("POW_DIFFICULTY")

	t.Cleanup(func() { _ = os.Unsetenv("PORT"); _ = os.Unsetenv("POW_DIFFICULTY") })

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() { done <- bootstrap(ctx, discardLogger()) }()

	url := "http://127.0.0.1:" + port + "/"

	up := false
	for i := 0; i < 100 && !up; i++ {
		if resp, err := http.Get(url); err == nil {
			_ = resp.Body.Close() // response body of a test request; close error is irrelevant
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

func newAddress(t *testing.T) string {
	t.Helper()

	w, err := wallet.New()
	if err != nil {
		t.Fatal(err)
	}

	return w.Address()
}

func TestParseGenesisAllocations(t *testing.T) {
	a, b := newAddress(t), newAddress(t)

	for _, empty := range []string{"", "   "} {
		outs, err := parseGenesisAllocations(empty)
		if err != nil || len(outs) != 0 {
			t.Fatalf("parse(%q) = %v, %v, want no outputs", empty, outs, err)
		}
	}

	outs, err := parseGenesisAllocations(a + ":10, " + b + ":20")
	if err != nil {
		t.Fatal(err)
	}

	want := []domain.TxOut{{Amount: 10, Address: a}, {Amount: 20, Address: b}}
	if !reflect.DeepEqual(outs, want) {
		t.Fatalf("outs = %v, want %v", outs, want)
	}

	for _, in := range []string{a + " : 10", a + " :10", a + ": 10", a + "\t:\t10"} {
		outs, err := parseGenesisAllocations(in)
		if err != nil || !reflect.DeepEqual(outs, []domain.TxOut{{Amount: 10, Address: a}}) {
			t.Fatalf("parse(%q) = %v, %v", in, outs, err)
		}
	}

	invalid := map[string]string{
		"bad address":   "nothex:5",
		"zero":          a + ":0",
		"non numeric":   a + ":abc",
		"negative":      a + ":-1",
		"overflow":      a + ":18446744073709551616",
		"no colon":      a,
		"empty value":   a + ":",
		"blank value":   a + ":  ",
		"blank address": "  :5",
		"empty item":    a + ":5,",
		"second bad":    a + ":5,bad:5",
		"empty amount":  ":5",
	}
	for name, in := range invalid {
		t.Run(name, func(t *testing.T) {
			outs, err := parseGenesisAllocations(in)
			if err == nil || outs != nil {
				t.Fatalf("parse(%q) = %v, %v, want error", in, outs, err)
			}

			if bad := invalidItem(in); !strings.Contains(err.Error(), fmt.Sprintf("%q", bad)) {
				t.Fatalf("error %v does not cite the invalid item %q", err, bad)
			}
		})
	}

	_, err = parseGenesisAllocations(a + ":5,bad:5")
	if err == nil || !strings.Contains(err.Error(), "bad:5") {
		t.Fatalf("error %v does not cite the invalid item", err)
	}
}

// invalidItem returns the offending item of an invalid list: the last one,
// since every case puts the bad item last (or is a single item).
func invalidItem(list string) string {
	items := strings.Split(list, ",")
	return strings.TrimSpace(items[len(items)-1])
}

func TestRunFailsOnInvalidGenesisAllocations(t *testing.T) {
	t.Setenv("PORT", freePort(t))
	t.Setenv("POW_DIFFICULTY", "1")
	t.Setenv(genesisAllocationsEnvVar, "bad:5")

	err := run(context.Background(), discardLogger())
	if err == nil || !strings.Contains(err.Error(), "bad:5") {
		t.Fatalf("run = %v, want error citing bad:5", err)
	}
}

func TestBuildServiceGenesisAllocations(t *testing.T) {
	t.Setenv("POW_DIFFICULTY", "1")
	a := newAddress(t)
	eng := pow.NewProofOfWorkFromEnv()

	t.Run("applied on empty repository", func(t *testing.T) {
		t.Setenv(genesisAllocationsEnvVar, a+":7")

		repo := memory.New()
		if _, err := newService(repo, eng); err != nil {
			t.Fatal(err)
		}

		blocks, _ := repo.All()
		if len(blocks) != 1 || len(blocks[0].Transactions) != 1 || blocks[0].Transactions[0].Outputs[0].Amount != 7 {
			t.Fatalf("genesis = %+v, want one tx with 7", blocks)
		}
	})

	t.Run("unset leaves genesis empty", func(t *testing.T) {
		t.Setenv(genesisAllocationsEnvVar, "")

		repo := memory.New()
		if _, err := newService(repo, eng); err != nil {
			t.Fatal(err)
		}

		blocks, _ := repo.All()
		if len(blocks) != 1 || len(blocks[0].Transactions) != 0 {
			t.Fatalf("genesis = %+v, want no transactions", blocks)
		}
	})

	t.Run("ignored on populated repository", func(t *testing.T) {
		t.Setenv(genesisAllocationsEnvVar, "")

		repo := memory.New()
		if _, err := newService(repo, eng); err != nil {
			t.Fatal(err)
		}

		before, _ := repo.All()

		t.Setenv(genesisAllocationsEnvVar, a+":7")

		if _, err := newService(repo, eng); err != nil {
			t.Fatal(err)
		}

		after, _ := repo.All()
		if !reflect.DeepEqual(before, after) {
			t.Fatalf("chain changed: %+v -> %+v", before, after)
		}
	})
}

func TestOpenRepository(t *testing.T) {
	t.Run("empty DATA_DIR chooses memory adapter", func(t *testing.T) {
		repo, closeRepo, err := openRepository("", storage.Pruning{})
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := closeRepo(); err != nil {
				t.Fatal(err)
			}
		}()

		if _, ok := repo.(*memory.Repository); !ok {
			t.Fatalf("repo = %T, want *memory.Repository", repo)
		}
	})

	t.Run("non-empty DATA_DIR creates directory and chooses bolt adapter", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "nested", "data")

		repo, closeRepo, err := openRepository(dir, storage.Pruning{})
		if err != nil {
			t.Fatal(err)
		}

		defer func() {
			if err := closeRepo(); err != nil {
				t.Fatal(err)
			}
		}()

		if _, ok := repo.(*bolt.Repository); !ok {
			t.Fatalf("repo = %T, want *bolt.Repository", repo)
		}

		dirInfo, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("stat dir %s: %v", dir, err)
		}

		if perm := dirInfo.Mode().Perm(); perm != 0o700 {
			t.Fatalf("dir perm = %#o, want %#o", perm, 0o700)
		}

		dbPath := filepath.Join(dir, "chain.db")

		info, err := os.Stat(dbPath)
		if err != nil || info.IsDir() {
			t.Fatalf("db file %s not found: %v", dbPath, err)
		}
	})
}

func TestRunServesAndShutsDownGracefullyWithDataDir(t *testing.T) {
	port := freePort(t)
	dataDir := t.TempDir()
	t.Setenv("PORT", port)
	t.Setenv("POW_DIFFICULTY", "1")
	t.Setenv("MINING_TIMEOUT", "5s")
	t.Setenv("DATA_DIR", dataDir)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() { done <- run(ctx, discardLogger()) }()

	url := "http://127.0.0.1:" + port + "/"

	var (
		resp *http.Response
		err  error
	)
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

	_ = resp.Body.Close() // response body of a test request; close error is irrelevant
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

	dbPath := filepath.Join(dataDir, "chain.db")
	if info, err := os.Stat(dbPath); err != nil || info.IsDir() {
		t.Fatalf("chain.db does not exist: %v", err)
	}
}

func TestRunReleasesBoltLockWhenBootstrapFails(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("PORT", freePort(t))
	t.Setenv("POW_DIFFICULTY", "1")
	t.Setenv("DATA_DIR", dataDir)
	t.Setenv(genesisAllocationsEnvVar, "bad:5")

	err := run(context.Background(), discardLogger())
	if err == nil {
		t.Fatal("run succeeded, want error")
	}

	// Because closeRepo() was deferred, the file lock must be released immediately.
	reopened, openErr := bolt.Open(filepath.Join(dataDir, "chain.db"))
	if openErr != nil {
		t.Fatalf("database file still locked after run failure: %v", openErr)
	}

	_ = reopened.Close()
}

func TestFinish(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		code   int
		logged bool
	}{
		{"nil", nil, -1, false},
		{"node error", nodeStopped{errors.New("boom")}, 1, true},
		{"other command error", errors.New("bad flag"), 1, false},
	}
	for _, c := range cases {
		var buf bytes.Buffer

		code := -1

		finish(c.err, slog.New(slog.NewJSONHandler(&buf, nil)), func(n int) { code = n })

		if code != c.code || (buf.Len() > 0) != c.logged {
			t.Errorf("%s: exit code %d, log %q; want code %d, logged %v", c.name, code, buf.String(), c.code, c.logged)
		}
	}
}

func TestParseMinerAddress(t *testing.T) {
	a := newAddress(t)
	for _, tc := range []struct{ name, raw, want string }{
		{"empty", "", ""},
		{"spaces only", "  \t", ""},
		{"valid", a, a},
		{"valid with spaces", "  " + a + " ", a},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseMinerAddress(tc.raw)
			if err != nil || got != tc.want {
				t.Fatalf("parseMinerAddress(%q) = %q, %v; want %q", tc.raw, got, err, tc.want)
			}
		})
	}

	t.Run("invalid cites the variable", func(t *testing.T) {
		_, err := parseMinerAddress("not-an-address")
		if err == nil || !strings.Contains(err.Error(), "MINER_ADDRESS") || !strings.Contains(err.Error(), "not-an-address") {
			t.Fatalf("err = %v, want one citing MINER_ADDRESS", err)
		}
	})
}

func TestBuildServiceMinerAddress(t *testing.T) {
	t.Setenv("POW_DIFFICULTY", "1")
	t.Setenv(genesisAllocationsEnvVar, "")

	eng := pow.NewProofOfWorkFromEnv()

	t.Run("set pays the coinbase to the address", func(t *testing.T) {
		a := newAddress(t)
		t.Setenv(minerAddressEnvVar, " "+a+" ")

		svc, err := newService(memory.New(), eng)
		if err != nil {
			t.Fatal(err)
		}

		b, err := svc.MinePending(context.Background())
		if err != nil {
			t.Fatal(err)
		}

		if len(b.Transactions) != 1 || b.Transactions[0].Outputs[0].Address != a ||
			b.Transactions[0].Outputs[0].Amount != domain.Subsidy(b.Index) {
			t.Fatalf("block = %+v, want one coinbase paying the subsidy to %s", b.Transactions, a)
		}
	})

	for _, raw := range []string{"", "   "} {
		t.Run(fmt.Sprintf("blank %q adds no coinbase", raw), func(t *testing.T) {
			t.Setenv(minerAddressEnvVar, raw)

			svc, err := newService(memory.New(), eng)
			if err != nil {
				t.Fatal(err)
			}

			b, err := svc.MinePending(context.Background())
			if err != nil {
				t.Fatal(err)
			}

			if len(b.Transactions) != 0 {
				t.Fatalf("transactions = %+v, want none", b.Transactions)
			}
		})
	}

	t.Run("invalid stops the service at startup", func(t *testing.T) {
		t.Setenv(minerAddressEnvVar, "bad")

		if _, err := newService(memory.New(), eng); err == nil || !strings.Contains(err.Error(), "MINER_ADDRESS") {
			t.Fatalf("err = %v, want one citing MINER_ADDRESS", err)
		}
	})

	t.Run("run fails on an invalid address", func(t *testing.T) {
		t.Setenv("PORT", freePort(t))
		t.Setenv(minerAddressEnvVar, "bad")

		err := run(context.Background(), discardLogger())
		if err == nil || !strings.Contains(err.Error(), "MINER_ADDRESS") {
			t.Fatalf("run = %v, want error citing MINER_ADDRESS", err)
		}
	})
}
