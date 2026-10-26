package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/petersonsalme/blockchain-with-go/client"
	"github.com/petersonsalme/blockchain-with-go/internal/chain"
	"github.com/petersonsalme/blockchain-with-go/internal/consensus"
	"github.com/petersonsalme/blockchain-with-go/internal/consensus/pow"
	"github.com/petersonsalme/blockchain-with-go/internal/domain"
	"github.com/petersonsalme/blockchain-with-go/internal/storage"
	"github.com/petersonsalme/blockchain-with-go/internal/storage/bolt"
	"github.com/petersonsalme/blockchain-with-go/pkg/wallet"
)

func TestParsePruning(t *testing.T) {
	ok := []struct {
		name, interval, keep string
		want                 storage.Pruning
	}{
		{"defaults", "", "", storage.Pruning{SnapshotInterval: 1000}},
		{"interval off", "0", "", storage.Pruning{}},
		{"explicit", "50", "100", storage.Pruning{SnapshotInterval: 50, KeepBlocks: 100}},
		{"keep 99 is refused below", "10", "0", storage.Pruning{SnapshotInterval: 10}},
	}
	for _, c := range ok {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv(snapshotIntervalEnvVar, c.interval)
			t.Setenv(pruneKeepBlocksEnvVar, c.keep)

			got, err := parsePruning()
			if err != nil || got != c.want {
				t.Fatalf("parsePruning() = %+v, %v; want %+v", got, err, c.want)
			}
		})
	}

	bad := []struct{ name, interval, keep, names string }{
		{"interval text", "abc", "", snapshotIntervalEnvVar},
		{"interval negative", "-1", "", snapshotIntervalEnvVar},
		{"interval float", "1.5", "", snapshotIntervalEnvVar},
		{"keep text", "10", "x", pruneKeepBlocksEnvVar},
		{"keep negative", "10", "-5", pruneKeepBlocksEnvVar},
		{"keep 1", "10", "1", pruneKeepBlocksEnvVar},
		{"keep 99", "10", "99", pruneKeepBlocksEnvVar},
		{"keep without interval", "0", "100", pruneKeepBlocksEnvVar},
	}
	for _, c := range bad {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv(snapshotIntervalEnvVar, c.interval)
			t.Setenv(pruneKeepBlocksEnvVar, c.keep)

			_, err := parsePruning()
			if err == nil || !strings.Contains(err.Error(), c.names) {
				t.Fatalf("parsePruning() error = %v, want one naming %s", err, c.names)
			}
		})
	}

	t.Run("keep 100 is the lowest allowed", func(t *testing.T) {
		t.Setenv(snapshotIntervalEnvVar, "10")
		t.Setenv(pruneKeepBlocksEnvVar, "100")

		got, err := parsePruning()
		if err != nil || got.KeepBlocks != 100 {
			t.Fatalf("parsePruning() = %+v, %v", got, err)
		}
	})
}

func TestRunFailsOnInvalidPruning(t *testing.T) {
	t.Setenv(dataDirEnvVar, t.TempDir())
	t.Setenv(snapshotIntervalEnvVar, "0")
	t.Setenv(pruneKeepBlocksEnvVar, "100")

	err := run(context.Background(), discardLogger())
	if err == nil || !strings.Contains(err.Error(), pruneKeepBlocksEnvVar) {
		t.Fatalf("run() error = %v, want one naming %s", err, pruneKeepBlocksEnvVar)
	}
}

func TestOpenRepositoryAppliesPruning(t *testing.T) {
	p := storage.Pruning{SnapshotInterval: 3, KeepBlocks: 2}

	for name, dir := range map[string]string{"memory": "", "bolt": t.TempDir()} {
		t.Run(name, func(t *testing.T) {
			repo, closeRepo, err := openRepository(dir, p)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = closeRepo() }()
			// The rule is only visible through its effect: the Commit of height 5 stores the snapshot of 3.
			eng := pow.New(1)

			svc, err := chain.NewService(repo, eng)
			if err != nil {
				t.Fatal(err)
			}

			for range 5 {
				_, err := svc.Mine(context.Background(), nil)
				if err != nil {
					t.Fatal(err)
				}
			}

			snap, err := repo.Snapshot()
			if err != nil || snap.Height != 3 {
				t.Fatalf("Snapshot() = %+v, %v; want height 3", snap, err)
			}

			if below, _ := repo.PrunedBelow(); below != 4 {
				t.Fatalf("PrunedBelow() = %d, want 4", below)
			}
		})
	}
}

// snapshotWorld is a full node that mined transfers among three wallets.
type snapshotWorld struct {
	dir   string
	addrs []string
}

// mineWorld mines n blocks of transfers on a full node at dir (snapshot every
// interval blocks, no pruning) and closes it. PoW difficulty is 1.
func mineWorld(t *testing.T, n, interval int) snapshotWorld {
	t.Helper()
	t.Chdir(t.TempDir()) // no .env here
	t.Setenv("POW_DIFFICULTY", "1")
	t.Setenv("POW_RETARGET_INTERVAL", "0")
	t.Setenv(consensusEnvVar, "pow")

	var (
		wallets []*wallet.Wallet
		alloc   []string
	)

	w := snapshotWorld{dir: t.TempDir()}

	for range 3 {
		k, err := wallet.New()
		if err != nil {
			t.Fatal(err)
		}

		wallets = append(wallets, k)
		w.addrs = append(w.addrs, k.Address())
		alloc = append(alloc, k.Address()+":1000")
	}

	t.Setenv(genesisAllocationsEnvVar, strings.Join(alloc, ","))
	t.Setenv(dataDirEnvVar, w.dir)

	repo, closeRepo, err := openRepository(w.dir, storage.Pruning{SnapshotInterval: interval})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closeRepo() }()

	svc, err := newService(repo, pow.New(1))
	if err != nil {
		t.Fatal(err)
	}

	for i := range n {
		from, to := wallets[i%3], wallets[(i+1)%3]
		_, owned := svc.Balance(from.Address())

		utxos := make([]client.Utxo, 0, len(owned))
		for _, u := range owned {
			utxos = append(utxos, client.Utxo{Txid: u.TxID, Index: u.Index, Amount: u.Amount})
		}

		tx, err := buildTransfer(from, utxos, to.Address(), uint64(7+i), 0, time.Now())
		if err != nil {
			t.Fatal(err)
		}

		_, err = svc.Mine(context.Background(), []domain.Transaction{tx})
		if err != nil {
			t.Fatalf("mine %d: %v", i, err)
		}
	}

	return w
}

// exportFile exports the snapshot of w.dir and returns the file and the hash line printed.
func exportFile(t *testing.T, w snapshotWorld) (string, string) {
	t.Helper()
	t.Setenv(dataDirEnvVar, w.dir)
	file := filepath.Join(t.TempDir(), "snap.json")

	var out bytes.Buffer

	err := execute(context.Background(), discardLogger(), []string{"node", "--snapshot-export", file}, &out)
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	return file, out.String()
}

func readFile(t *testing.T, file string) ([]*domain.Block, storage.Snapshot) {
	t.Helper()

	f, err := os.Open(file)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = f.Close() }()

	h, s, err := storage.ReadSnapshotFile(f)
	if err != nil {
		t.Fatal(err)
	}

	return h, s
}

func importInto(t *testing.T, dir, file, hash string) (string, error) {
	t.Helper()
	t.Setenv(dataDirEnvVar, dir)

	var out bytes.Buffer

	args := []string{"node", "--snapshot-import", file}
	if hash != "" {
		args = append(args, "--snapshot-hash", hash)
	}

	err := execute(context.Background(), discardLogger(), args, &out)

	return out.String(), err
}

// expectNoBlocks fails when dir/chain.db exists and holds a block.
func expectNoBlocks(t *testing.T, dir string) {
	t.Helper()

	path := filepath.Join(dir, "chain.db")

	_, err := os.Stat(path)
	if err != nil {
		return
	}

	repo, err := bolt.Open(path)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = repo.Close() }()

	if all, _ := repo.All(); len(all) != 0 {
		t.Fatalf("chain.db holds %d blocks, want none", len(all))
	}
}

func TestSnapshotExportPrintsAndWrites(t *testing.T) {
	w := mineWorld(t, 12, 5)
	file, out := exportFile(t, w)

	headers, snap := readFile(t, file)
	if snap.Height != 10 || len(headers) != 11 {
		t.Fatalf("height %d with %d headers, want 10 and 11", snap.Height, len(headers))
	}

	for _, h := range headers {
		if len(h.Transactions) != 0 {
			t.Fatalf("header %d carries transactions", h.Index)
		}
	}

	if want := "snapshot: height 10 hash " + snap.Hash + "\n"; out != want {
		t.Fatalf("output = %q, want %q", out, want)
	}
}

func TestSnapshotExportFails(t *testing.T) {
	t.Run("no DATA_DIR", func(t *testing.T) {
		t.Chdir(t.TempDir())
		t.Setenv(dataDirEnvVar, "")

		err := execute(context.Background(), discardLogger(), []string{"node", "--snapshot-export", filepath.Join(t.TempDir(), "s.json")}, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), dataDirEnvVar) {
			t.Fatalf("err = %v, want one naming %s", err, dataDirEnvVar)
		}
	})
	t.Run("no chain.db", func(t *testing.T) {
		t.Chdir(t.TempDir())
		dir := t.TempDir()
		t.Setenv(dataDirEnvVar, dir)

		file := filepath.Join(t.TempDir(), "s.json")

		err := execute(context.Background(), discardLogger(), []string{"node", "--snapshot-export", file}, &bytes.Buffer{})
		if err == nil {
			t.Fatal("want an error")
		}

		_, err = os.Stat(filepath.Join(dir, "chain.db"))
		if err == nil {
			t.Fatal("export created chain.db")
		}

		_, err = os.Stat(file)
		if err == nil {
			t.Fatal("export wrote a file")
		}
	})
	t.Run("no snapshot yet", func(t *testing.T) {
		w := mineWorld(t, 3, 1000)
		file := filepath.Join(t.TempDir(), "s.json")
		t.Setenv(dataDirEnvVar, w.dir)

		err := execute(context.Background(), discardLogger(), []string{"node", "--snapshot-export", file}, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "snapshot") {
			t.Fatalf("err = %v, want a no-snapshot error", err)
		}

		_, err = os.Stat(file)
		if err == nil {
			t.Fatal("export wrote a file")
		}
	})
}

func TestSnapshotImportRestores(t *testing.T) {
	w := mineWorld(t, 12, 5)
	file, _ := exportFile(t, w)
	_, snap := readFile(t, file)

	dir := t.TempDir()

	out, err := importInto(t, dir, file, snap.Hash)
	if err != nil {
		t.Fatalf("import: %v", err)
	}

	if out != "restored: height 10\n" {
		t.Fatalf("output = %q", out)
	}

	repo, err := bolt.OpenPruned(filepath.Join(dir, "chain.db"), storage.Pruning{SnapshotInterval: 5, KeepBlocks: 5})
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = repo.Close() }()

	if below, _ := repo.PrunedBelow(); below != 11 {
		t.Fatalf("PrunedBelow = %d, want 11", below)
	}

	if got, _ := repo.Snapshot(); got.Hash != snap.Hash {
		t.Fatalf("snapshot hash = %s, want %s", got.Hash, snap.Hash)
	}
	// An uppercase hash is the same hash.
	dir2 := t.TempDir()

	_, err = importInto(t, dir2, file, strings.ToUpper(snap.Hash))
	if err != nil {
		t.Fatalf("import with uppercase hash: %v", err)
	}
}

func TestSnapshotImportIntoEmptyChainDB(t *testing.T) {
	w := mineWorld(t, 12, 5)
	file, _ := exportFile(t, w)
	_, snap := readFile(t, file)
	dir := t.TempDir()

	repo, err := bolt.Open(filepath.Join(dir, "chain.db"))
	if err != nil {
		t.Fatal(err)
	}

	_ = repo.Close()

	_, err = importInto(t, dir, file, snap.Hash)
	if err != nil {
		t.Fatalf("import into empty chain.db: %v", err)
	}
}

// rewrite reads file, lets edit change it and writes it back with a consistent hash and block hash.
func rewriteSnapshot(t *testing.T, file string, edit func(h []*domain.Block, s *storage.Snapshot)) string {
	t.Helper()
	h, s := readFile(t, file)
	edit(h, &s)
	s.BlockHash = h[len(h)-1].Hash
	s.Hash = storage.SnapshotHash(s.BlockHash, s.UTXOs)
	out := filepath.Join(t.TempDir(), "edited.json")

	f, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = f.Close() }()

	err = storage.WriteSnapshotFile(f, h, s)
	if err != nil {
		t.Fatal(err)
	}

	return out
}

func TestSnapshotImportRejects(t *testing.T) {
	w := mineWorld(t, 12, 5)
	file, _ := exportFile(t, w)
	_, snap := readFile(t, file)

	t.Run("wrong -snapshot-hash", func(t *testing.T) {
		dir := t.TempDir()

		_, err := importInto(t, dir, file, strings.Repeat("0", 64))
		if err == nil || !strings.Contains(err.Error(), "snapshot hash mismatch") {
			t.Fatalf("err = %v, want snapshot hash mismatch", err)
		}

		expectNoBlocks(t, dir)
	})

	t.Run("missing -snapshot-hash", func(t *testing.T) {
		dir := t.TempDir()

		_, err := importInto(t, dir, file, "")
		if err == nil || !strings.Contains(err.Error(), "snapshot-hash") {
			t.Fatalf("err = %v, want one naming snapshot-hash", err)
		}

		expectNoBlocks(t, dir)
	})

	t.Run("tampered amount", func(t *testing.T) {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}

		var m map[string]any

		err = json.Unmarshal(raw, &m)
		if err != nil {
			t.Fatal(err)
		}

		u := m["utxos"].([]any)[0].(map[string]any)
		u["amount"] = u["amount"].(float64) + 1
		raw, _ = json.Marshal(m)

		tampered := filepath.Join(t.TempDir(), "tampered.json")

		err = os.WriteFile(tampered, raw, 0o600)
		if err != nil {
			t.Fatal(err)
		}

		dir := t.TempDir()
		// The operator holds the hash the file claims, so only the content check can catch it.
		_, err = importInto(t, dir, tampered, snap.Hash)
		if err == nil || !strings.Contains(err.Error(), storage.ErrInvalidSnapshot.Error()) {
			t.Fatalf("err = %v, want ErrInvalidSnapshot", err)
		}

		expectNoBlocks(t, dir)
	})

	t.Run("not a file", func(t *testing.T) {
		dir := t.TempDir()

		_, err := importInto(t, dir, filepath.Join(t.TempDir(), "missing.json"), snap.Hash)
		if err == nil {
			t.Fatal("want an error")
		}

		expectNoBlocks(t, dir)
	})

	t.Run("header with invalid proof of work", func(t *testing.T) {
		bad := rewriteSnapshot(t, file, func(h []*domain.Block, _ *storage.Snapshot) {
			eng := pow.New(1)

			last := h[len(h)-1]
			for last.Nonce = 0; ; last.Nonce++ {
				last.Hash = last.CalculateHash()
				if eng.Verify(h[:len(h)-1], last) != nil {
					return
				}
			}
		})
		_, s := readFile(t, bad)
		dir := t.TempDir()

		_, err := importInto(t, dir, bad, s.Hash)
		if err == nil || !strings.Contains(err.Error(), consensus.ErrInvalidProofOfWork.Error()) {
			t.Fatalf("err = %v, want ErrInvalidProofOfWork", err)
		}

		expectNoBlocks(t, dir)
	})

	t.Run("header chain verified for every height, not only the last", func(t *testing.T) {
		bad := rewriteSnapshot(t, file, func(h []*domain.Block, _ *storage.Snapshot) {
			eng := pow.New(1)

			mid := h[3]
			for mid.Nonce = 0; ; mid.Nonce++ {
				mid.Hash = mid.CalculateHash()
				if eng.Verify(h[:3], mid) != nil {
					break
				}
			}

			for i := 4; i < len(h); i++ { // relink without re-mining
				h[i].PrevHash = h[i-1].Hash
				h[i].Hash = h[i].CalculateHash()
			}
		})
		_, s := readFile(t, bad)

		dir := t.TempDir()

		_, err := importInto(t, dir, bad, s.Hash)
		if err == nil {
			t.Fatal("want an error")
		}

		expectNoBlocks(t, dir)
	})

	t.Run("wrong consensus engine", func(t *testing.T) {
		t.Setenv(consensusEnvVar, "poa")

		dir := t.TempDir()

		_, err := importInto(t, dir, file, snap.Hash)
		if err == nil {
			t.Fatal("want an error")
		}

		expectNoBlocks(t, dir)
	})

	t.Run("no DATA_DIR", func(t *testing.T) {
		_, err := importInto(t, "", file, snap.Hash)
		if err == nil || !strings.Contains(err.Error(), dataDirEnvVar) {
			t.Fatalf("err = %v, want one naming %s", err, dataDirEnvVar)
		}
	})

	t.Run("DATA_DIR that already holds blocks", func(t *testing.T) {
		other := mineWorld(t, 3, 1000)
		before := chainHashes(t, other.dir)

		_, err := importInto(t, other.dir, file, snap.Hash)
		if err == nil {
			t.Fatal("want an error")
		}

		if after := chainHashes(t, other.dir); !reflect.DeepEqual(before, after) {
			t.Fatalf("chain.db changed: %v -> %v", before, after)
		}
	})
}

func chainHashes(t *testing.T, dir string) []string {
	t.Helper()

	repo, err := bolt.Open(filepath.Join(dir, "chain.db"))
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = repo.Close() }()

	all, err := repo.All()
	if err != nil || len(all) == 0 {
		t.Fatalf("All() = %d blocks, %v", len(all), err)
	}

	var out []string
	for _, b := range all {
		out = append(out, b.Hash)
	}

	return out
}

func TestSnapshotFlagsAreExclusive(t *testing.T) {
	t.Chdir(t.TempDir())

	for _, args := range [][]string{
		{"node", "--snapshot-export", "a", "--snapshot-import", "b", "--snapshot-hash", "x"},
		{"node", "--snapshot-export", "a", "--reindex"},
		{"node", "--snapshot-import", "b", "--snapshot-hash", "x", "--reindex"},
		{"node", "--snapshot-hash", "x"},
	} {
		err := execute(context.Background(), discardLogger(), args, &bytes.Buffer{})
		if err == nil {
			t.Errorf("%v: want an error", args)
		}
	}
}

// TestSnapshotBootReachesTheStateOfTheFullNode is the acceptance test of ENG-29:
// a node booted from a snapshot file and fed the later blocks ends in the same
// state as the node that exported it, and the two accept each other's blocks.
func TestSnapshotBootReachesTheStateOfTheFullNode(t *testing.T) {
	const mined = 34

	w := mineWorld(t, mined, 10)
	file, _ := exportFile(t, w)

	_, snap := readFile(t, file)
	if snap.Height != 30 {
		t.Fatalf("snapshot height = %d, want 30", snap.Height)
	}

	dirB := t.TempDir()

	_, err := importInto(t, dirB, file, snap.Hash)
	if err != nil {
		t.Fatal(err)
	}

	pruning := storage.Pruning{SnapshotInterval: 10, KeepBlocks: 20}

	full, closeFull, err := openRepository(w.dir, storage.Pruning{SnapshotInterval: 10})
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = closeFull() }()

	imported, closeImported, err := openRepository(dirB, pruning)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = closeImported() }()

	eng := pow.New(1)

	fullSvc, err := chain.NewService(full, eng)
	if err != nil {
		t.Fatal(err)
	}

	importedSvc, err := chain.NewService(imported, eng)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()

	fullChain, err := fullSvc.Blocks()
	if err != nil {
		t.Fatal(err)
	}

	_, err = importedSvc.ReplaceChain(ctx, fullChain)
	if err != nil {
		t.Fatalf("ReplaceChain on the imported node: %v", err)
	}

	same := func(label string) {
		t.Helper()

		ft, _ := fullSvc.Tip()

		it, _ := importedSvc.Tip()
		if ft.Hash != it.Hash {
			t.Fatalf("%s: tips differ: %s vs %s", label, ft.Hash, it.Hash)
		}

		n := ft.Index + 1

		fs, err := full.ForkState(n)
		if err != nil {
			t.Fatal(err)
		}

		is, err := imported.ForkState(n)
		if err != nil {
			t.Fatal(err)
		}

		if !reflect.DeepEqual(fs, is) {
			t.Fatalf("%s: UTXO sets differ", label)
		}

		for _, a := range w.addrs {
			fb, _ := fullSvc.Balance(a)

			ib, _ := importedSvc.Balance(a)
			if fb != ib {
				t.Fatalf("%s: balance of %s: %d vs %d", label, a, fb, ib)
			}
		}
	}
	same("after sync")

	if tip, _ := importedSvc.Tip(); tip.Index != mined {
		t.Fatalf("tip = %d, want %d", tip.Index, mined)
	}

	// The imported node mines the next block and the full node takes it.
	next, err := importedSvc.Mine(ctx, nil)
	if err != nil {
		t.Fatalf("Mine on the imported node: %v", err)
	}

	blocks, err := importedSvc.Blocks()
	if err != nil {
		t.Fatal(err)
	}

	_, err = fullSvc.ReplaceChain(ctx, blocks)
	if err != nil {
		t.Fatalf("ReplaceChain on the full node: %v", err)
	}

	if tip, _ := fullSvc.Tip(); tip.Hash != next.Hash {
		t.Fatalf("full node tip = %s, want %s", tip.Hash, next.Hash)
	}

	same("after the imported node mined")
}
