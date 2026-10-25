package storage_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/petersonsalme/blockchain-with-go/internal/domain"
	"github.com/petersonsalme/blockchain-with-go/internal/storage"
)

func fileFixture() ([]*domain.Block, storage.Snapshot) {
	var headers []*domain.Block
	prev := ""
	for i := 0; i < 3; i++ {
		b := &domain.Block{Index: i, Timestamp: time.Unix(1_700_000_000+int64(i), 0).UTC(), MerkleRoot: "mr", PrevHash: prev, Transactions: []domain.Transaction{}}
		b.Hash = b.CalculateHash()
		prev = b.Hash
		headers = append(headers, b)
	}
	set := domain.UTXOSet{
		{TxID: "bb", Index: 0}: {Amount: 7, Address: "bob"},
		{TxID: "aa", Index: 1}: {Amount: 9, Address: "alice"},
		{TxID: "aa", Index: 0}: {Amount: 5, Address: "carol"},
	}
	last := headers[2].Hash
	return headers, storage.Snapshot{Height: 2, BlockHash: last, Hash: storage.SnapshotHash(last, set), UTXOs: set}
}

func writeFixture(t *testing.T) ([]*domain.Block, storage.Snapshot, []byte) {
	t.Helper()
	headers, snap := fileFixture()
	var buf bytes.Buffer
	if err := storage.WriteSnapshotFile(&buf, headers, snap); err != nil {
		t.Fatal(err)
	}
	return headers, snap, buf.Bytes()
}

func TestSnapshotFileRoundTrip(t *testing.T) {
	headers, snap, data := writeFixture(t)
	gotH, gotS, err := storage.ReadSnapshotFile(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(gotH) != len(headers) {
		t.Fatalf("headers = %d, want %d", len(gotH), len(headers))
	}
	for i := range headers {
		if gotH[i].Hash != headers[i].Hash || gotH[i].Index != i || !gotH[i].Timestamp.Equal(headers[i].Timestamp) || gotH[i].CalculateHash() != headers[i].Hash {
			t.Fatalf("header %d = %+v, want %+v", i, gotH[i], headers[i])
		}
	}
	if gotS.Height != snap.Height || gotS.BlockHash != snap.BlockHash || gotS.Hash != snap.Hash || len(gotS.UTXOs) != len(snap.UTXOs) {
		t.Fatalf("snapshot = %+v, want %+v", gotS, snap)
	}
	for op, o := range snap.UTXOs {
		if gotS.UTXOs[op] != o {
			t.Fatalf("utxo %v = %v, want %v", op, gotS.UTXOs[op], o)
		}
	}
}

func TestSnapshotFileShapeAndOrder(t *testing.T) {
	_, _, data := writeFixture(t)
	var raw struct {
		Height    int             `json:"height"`
		BlockHash string          `json:"blockHash"`
		Hash      string          `json:"hash"`
		Headers   json.RawMessage `json:"headers"`
		UTXOs     []struct {
			TxID    string `json:"txid"`
			Index   uint32 `json:"index"`
			Address string `json:"address"`
			Amount  uint64 `json:"amount"`
		} `json:"utxos"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if raw.Height != 2 || raw.BlockHash == "" || raw.Hash == "" || len(raw.Headers) == 0 {
		t.Fatalf("unexpected shape: %s", data)
	}
	want := []struct {
		id  string
		idx uint32
		a   string
		n   uint64
	}{{"aa", 0, "carol", 5}, {"aa", 1, "alice", 9}, {"bb", 0, "bob", 7}}
	if len(raw.UTXOs) != len(want) {
		t.Fatalf("utxos = %v", raw.UTXOs)
	}
	for i, w := range want {
		u := raw.UTXOs[i]
		if u.TxID != w.id || u.Index != w.idx || u.Address != w.a || u.Amount != w.n {
			t.Fatalf("utxos[%d] = %+v, want %+v", i, u, w)
		}
	}
}

func TestReadSnapshotFileRejects(t *testing.T) {
	_, _, data := writeFixture(t)
	mutate := func(f func(m map[string]any)) []byte {
		var m map[string]any
		if err := json.Unmarshal(data, &m); err != nil {
			t.Fatal(err)
		}
		f(m)
		out, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	cases := map[string][]byte{
		"invalid json": []byte("{not json"),
		"empty":        nil,
		"height":       mutate(func(m map[string]any) { m["height"] = 1 }),
		"blockHash":    mutate(func(m map[string]any) { m["blockHash"] = strings.Repeat("0", 64) }),
		"hash":         mutate(func(m map[string]any) { m["hash"] = strings.Repeat("0", 64) }),
		"amount": mutate(func(m map[string]any) {
			m["utxos"].([]any)[0].(map[string]any)["amount"] = 6
		}),
		"no headers": mutate(func(m map[string]any) { m["headers"] = []any{}; m["height"] = -1 }),
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			h, s, err := storage.ReadSnapshotFile(bytes.NewReader(in))
			if !errors.Is(err, storage.ErrInvalidSnapshot) {
				t.Fatalf("err = %v, want ErrInvalidSnapshot", err)
			}
			if h != nil || s.UTXOs != nil {
				t.Fatalf("returned data with an error: %v %v", h, s)
			}
		})
	}
}

func TestCheckHeaders(t *testing.T) {
	good, _ := fileFixture()
	if err := storage.CheckHeaders(good); err != nil {
		t.Fatalf("good headers: %v", err)
	}
	clone := func() []*domain.Block {
		out := make([]*domain.Block, len(good))
		for i, b := range good {
			c := *b
			out[i] = &c
		}
		return out
	}
	bad := map[string]func([]*domain.Block) []*domain.Block{
		"empty":    func([]*domain.Block) []*domain.Block { return nil },
		"nil":      func(h []*domain.Block) []*domain.Block { h[1] = nil; return h },
		"index":    func(h []*domain.Block) []*domain.Block { h[1].Index = 5; return h },
		"prevhash": func(h []*domain.Block) []*domain.Block { h[2].PrevHash = "x"; return h },
		"hash":     func(h []*domain.Block) []*domain.Block { h[1].Hash = "x"; return h },
		"nonce":    func(h []*domain.Block) []*domain.Block { h[2].Nonce++; return h },
	}
	for name, f := range bad {
		if err := storage.CheckHeaders(f(clone())); !errors.Is(err, storage.ErrConflict) {
			t.Errorf("%s: err = %v, want ErrConflict", name, err)
		}
	}
}
