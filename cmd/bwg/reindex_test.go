package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/onsi/gomega"
	bbolt "go.etcd.io/bbolt"

	"github.com/petersonsalme/blockchain-with-go/internal/storage/bolt"
	"github.com/petersonsalme/blockchain-with-go/internal/storage/storagetest"
)

// writeChain commits the five blocks of the shared fixture (its alternative chain) to dir/chain.db
// and returns the addresses it paid with their indexed transaction ids.
func writeChain(t *testing.T, dir string) map[string][]string {
	t.Helper()
	gomega.RegisterTestingT(t) // the fixture builder asserts with gomega

	_, blocks, _, deltas := storagetest.IndexedChains()

	repo, err := bolt.Open(filepath.Join(dir, "chain.db"))
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = repo.Close() }() // test repository; close error is irrelevant

	want := map[string][]string{}

	for i := range 5 {
		if err := repo.Commit(blocks[i], deltas[i]); err != nil {
			t.Fatal(err)
		}

		for _, tx := range blocks[i].Transactions {
			for _, o := range tx.Outputs {
				want[o.Address] = nil
			}
		}
	}

	for addr := range want {
		ids, err := repo.TxsByAddress(addr)
		if err != nil || len(ids) == 0 {
			t.Fatalf("TxsByAddress(%s) = %v, %v; want ids", addr, ids, err)
		}

		want[addr] = ids
	}

	return want
}

func dropBucket(t *testing.T, dir, name string) {
	t.Helper()

	db, err := bbolt.Open(filepath.Join(dir, "chain.db"), 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = db.Close() }() // test database; close error is irrelevant

	if err := db.Update(func(tx *bbolt.Tx) error { return tx.DeleteBucket([]byte(name)) }); err != nil {
		t.Fatal(err)
	}
}

func runReindexArgs(t *testing.T) (string, error) {
	t.Helper()

	var out bytes.Buffer

	err := execute(context.Background(), discardLogger(), []string{"node", "--reindex"}, &out)

	return out.String(), err
}

func TestReindexRebuildsDroppedIndex(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(t.TempDir()) // no .env here
	t.Setenv(dataDirEnvVar, dir)
	want := writeChain(t, dir)
	dropBucket(t, dir, "addrtx")

	out, err := runReindexArgs(t)
	if err != nil {
		t.Fatalf("--reindex returned %v, want nil", err)
	}

	if out != "reindexed: tip height 4\n" {
		t.Fatalf("output = %q, want %q", out, "reindexed: tip height 4\n")
	}

	repo, err := bolt.Open(filepath.Join(dir, "chain.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = repo.Close() }() // test repository; close error is irrelevant

	if ok, err := repo.IndexCurrent(); err != nil || !ok {
		t.Fatalf("IndexCurrent = %v, %v; want true", ok, err)
	}

	for addr, ids := range want {
		got, err := repo.TxsByAddress(addr)
		if err != nil || !reflect.DeepEqual(got, ids) {
			t.Fatalf("TxsByAddress(%s) = %v, %v; want %v", addr, got, err, ids)
		}
	}
}

func TestReindexTwiceLeavesTheSameState(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(t.TempDir())
	t.Setenv(dataDirEnvVar, dir)
	writeChain(t, dir)

	if _, err := runReindexArgs(t); err != nil {
		t.Fatal(err)
	}

	once := dumpIndexes(t, dir)
	if _, err := runReindexArgs(t); err != nil {
		t.Fatal(err)
	}

	if twice := dumpIndexes(t, dir); !reflect.DeepEqual(once, twice) {
		t.Fatal("second --reindex changed the index buckets")
	}

	if len(once) != 5 {
		t.Fatalf("dumped %d buckets, want 5", len(once))
	}
}

func dumpIndexes(t *testing.T, dir string) map[string]map[string]string {
	t.Helper()

	db, err := bbolt.Open(filepath.Join(dir, "chain.db"), 0o600, &bbolt.Options{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = db.Close() }() // test database; close error is irrelevant

	got := map[string]map[string]string{}

	err = db.View(func(tx *bbolt.Tx) error {
		for _, name := range []string{"utxos", "undo", "txs", "addrtx", "meta"} {
			m := map[string]string{}

			if err := tx.Bucket([]byte(name)).ForEach(func(k, v []byte) error { m[string(k)] = string(v); return nil }); err != nil {
				return err
			}

			got[name] = m
		}

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	return got
}

func TestReindexReadsDataDirFromDotEnv(t *testing.T) {
	dir := t.TempDir()
	writeChain(t, dir)

	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, ".env"), []byte("DATA_DIR="+dir+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Chdir(cwd)

	_ = os.Unsetenv(dataDirEnvVar)

	t.Cleanup(func() { _ = os.Unsetenv(dataDirEnvVar) })

	out, err := runReindexArgs(t)
	if err != nil || !strings.Contains(out, "tip height 4") {
		t.Fatalf("--reindex = %q, %v; want tip height 4", out, err)
	}
}

func TestReindexEmptyChainReportsIt(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(t.TempDir())
	t.Setenv(dataDirEnvVar, dir)

	out, err := runReindexArgs(t)
	if err != nil {
		t.Fatal(err)
	}

	if out != "reindexed: empty chain\n" {
		t.Fatalf("output = %q, want %q", out, "reindexed: empty chain\n")
	}
}

func TestReindexFailsWithoutDataDir(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv(dataDirEnvVar, "  ")

	out, err := runReindexArgs(t)
	if err == nil || !strings.Contains(err.Error(), dataDirEnvVar) {
		t.Fatalf("err = %v, want one naming %s", err, dataDirEnvVar)
	}

	if out != "" {
		t.Fatalf("output = %q, want none", out)
	}
}

func TestReindexFailsWhenDatabaseIsLocked(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(t.TempDir())
	t.Setenv(dataDirEnvVar, dir)

	holder, err := bolt.Open(filepath.Join(dir, "chain.db"))
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = holder.Close() }() // test repository; close error is irrelevant

	out, err := runReindexArgs(t)
	if err == nil {
		t.Fatal("--reindex on a locked chain.db returned nil, want the Open error")
	}

	if out != "" {
		t.Fatalf("output = %q, want none", out)
	}
}

func TestReindexDoesNotCreateMissingDataDir(t *testing.T) {
	t.Chdir(t.TempDir())
	missing := filepath.Join(t.TempDir(), "nope")
	t.Setenv(dataDirEnvVar, missing)

	if _, err := runReindexArgs(t); err == nil {
		t.Fatal("--reindex on a missing DATA_DIR returned nil")
	}

	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("DATA_DIR was created: %v", err)
	}
}

func TestExecuteRejectsUnknownFlag(t *testing.T) {
	var out bytes.Buffer
	if err := execute(context.Background(), discardLogger(), []string{"node", "--bogus"}, &out); err == nil {
		t.Fatal("unknown flag returned nil")
	}
}

func TestExecuteWithoutFlagBootstraps(t *testing.T) {
	t.Chdir(t.TempDir()) // no .env: bootstrap fails the same way as before the flag existed

	var out bytes.Buffer
	if err := execute(context.Background(), discardLogger(), []string{"node"}, &out); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err = %v, want the missing .env error from bootstrap", err)
	}
}

func TestNodeErrorsAreMarkedForTheFatalLog(t *testing.T) {
	t.Chdir(t.TempDir()) // no .env

	for _, args := range [][]string{{"node"}, {"node", "--bogus"}} {
		var (
			out     bytes.Buffer
			stopped nodeStopped
		)

		err := execute(context.Background(), discardLogger(), args, &out)
		if !errors.As(err, &stopped) {
			t.Fatalf("%v: err = %v, want a nodeStopped error", args, err)
		}

		if out.Len() != 0 {
			t.Fatalf("%v: output = %q, want no cobra text", args, out.String())
		}
	}
}
