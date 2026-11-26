package bolttest_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/petersonsalme/blockchain-with-go/internal/domain"
	"github.com/petersonsalme/blockchain-with-go/internal/storage"
	"github.com/petersonsalme/blockchain-with-go/internal/storage/bolt"
	"github.com/petersonsalme/blockchain-with-go/internal/storage/bolt/bolttest"
)

func TestWriteLegacyIsReadByTheAdapterAsStale(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	blocks := []*domain.Block{
		{Index: 0, Hash: "h0"},
		{Index: 1, PrevHash: "h0", Hash: "h1"},
		{Index: 2, PrevHash: "h1"}, // no hash: the hashes bucket skips it
	}

	err := bolttest.WriteLegacy(path, blocks)
	if err != nil {
		t.Fatal(err)
	}

	repo, err := bolt.Open(path)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = repo.Close() }()

	all, err := repo.All()
	if err != nil {
		t.Fatal(err)
	}

	if len(all) != len(blocks) {
		t.Fatalf("All() holds %d blocks, want %d", len(all), len(blocks))
	}

	for i, b := range all {
		if b.Index != i || b.Hash != blocks[i].Hash || b.PrevHash != blocks[i].PrevHash {
			t.Fatalf("block %d = %+v, want %+v", i, b, blocks[i])
		}
	}

	got, err := repo.ByHash("h1")
	if err != nil || got.Index != 1 {
		t.Fatalf("ByHash(h1) = %v, %v; want block 1", got, err)
	}

	_, err = repo.ByHash("")
	if err == nil {
		t.Fatal("ByHash(\"\") found a block: the hashless block was indexed")
	}

	current, err := repo.IndexCurrent()
	if err != nil || current {
		t.Fatalf("IndexCurrent() = %v, %v; want false", current, err)
	}

	err = repo.Commit(&domain.Block{Index: 3, PrevHash: ""}, domain.UTXODelta{})
	if !errors.Is(err, storage.ErrIndexStale) {
		t.Fatalf("Commit on a legacy file = %v, want %v", err, storage.ErrIndexStale)
	}
}

func TestWriteLegacyRejects(t *testing.T) {
	t.Run("a nil block, before touching the file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "nil.db")

		err := bolttest.WriteLegacy(path, []*domain.Block{{Index: 0}, nil})
		if !errors.Is(err, bolttest.ErrNilBlock) {
			t.Fatalf("err = %v, want %v", err, bolttest.ErrNilBlock)
		}

		_, err = os.Stat(path)
		if !os.IsNotExist(err) {
			t.Fatalf("Stat = %v, want the file absent", err)
		}
	})

	t.Run("an existing file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "twice.db")

		err := bolttest.WriteLegacy(path, []*domain.Block{{Index: 0, Hash: "h0"}})
		if err != nil {
			t.Fatal(err)
		}

		err = bolttest.WriteLegacy(path, []*domain.Block{{Index: 0, Hash: "h0"}})
		if err == nil {
			t.Fatal("want an error: the blocks bucket already exists")
		}
	})

	t.Run("an unwritable path", func(t *testing.T) {
		err := bolttest.WriteLegacy(filepath.Join(t.TempDir(), "missing", "dir.db"), nil)
		if err == nil {
			t.Fatal("want an error")
		}
	})
}
