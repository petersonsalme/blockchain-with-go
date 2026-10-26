package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/petersonsalme/blockchain-with-go/internal/domain"
	"github.com/petersonsalme/blockchain-with-go/internal/p2p"
)

var (
	hashX = strings.Repeat("a", 64)
	hashY = strings.Repeat("b", 64)
)

func invBody(from string, items ...p2p.InvItem) string {
	b, _ := json.Marshal(map[string]any{"node_id": from, "items": items})
	return string(b)
}

func blockGossipBody(from string, b domain.Block) string {
	raw, _ := json.Marshal(map[string]any{"node_id": from, "block": b})
	return string(raw)
}

func txGossipBody(from string, tx domain.Transaction) string {
	raw, _ := json.Marshal(map[string]any{"node_id": from, "tx": tx})
	return string(raw)
}

func sampleTx() domain.Transaction {
	return domain.Transaction{
		ID:        hashX,
		Inputs:    []domain.TxIn{{PrevTxID: hashY, OutIndex: 1}},
		Outputs:   []domain.TxOut{{Amount: 7, Address: "addr"}},
		Timestamp: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC),
	}
}

func TestPostInvReturnsWant(t *testing.T) {
	items := []p2p.InvItem{{Type: p2p.InvBlock, Hash: hashX}, {Type: p2p.InvTx, Hash: hashY}}
	f := &fakePeers{want: items[1:]}

	rec := do(p2pHandler(t, Config{P2P: f}), http.MethodPost, "/p2p/inv", invBody(idB, items...))
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("got %d %q %s", rec.Code, rec.Header().Get("Content-Type"), rec.Body)
	}

	var got struct {
		Want []p2p.InvItem `json:"want"`
	}

	err := json.Unmarshal(rec.Body.Bytes(), &got)
	if err != nil || len(got.Want) != 1 || got.Want[0] != items[1] {
		t.Fatalf("body = %s (%v)", rec.Body, err)
	}

	if f.gotFrom != idB || len(f.gotItems) != 2 || f.gotItems[0] != items[0] {
		t.Fatalf("Inv received %q %+v", f.gotFrom, f.gotItems)
	}
}

func TestPostInvEmptyWantIsAnArray(t *testing.T) {
	rec := do(p2pHandler(t, Config{P2P: &fakePeers{}}), http.MethodPost, "/p2p/inv", invBody(idB, p2p.InvItem{Type: p2p.InvTx, Hash: hashX}))
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}

	var m map[string]json.RawMessage

	_ = json.Unmarshal(rec.Body.Bytes(), &m)

	if string(m["want"]) != "[]" {
		t.Fatalf("body = %s", rec.Body)
	}
}

func TestGossipErrorMapping(t *testing.T) {
	notOnTip := fmt.Errorf("wrap: %w", p2p.ErrNotOnTip)
	invalid := fmt.Errorf("%w: boom", p2p.ErrInvalidObject)

	routes := []struct {
		name, path, body string
		cases            []struct {
			err    error
			status int
			code   string
		}
	}{
		{"inv", "/p2p/inv", invBody(idB, p2p.InvItem{Type: p2p.InvTx, Hash: hashX}), []struct {
			err    error
			status int
			code   string
		}{
			{p2p.ErrBadInv, http.StatusBadRequest, "invalid_inv"},
			{p2p.ErrUnknownPeer, http.StatusForbidden, "unknown_peer"},
			{p2p.ErrGossipDisabled, http.StatusNotFound, "p2p_disabled"},
			{errors.New("boom"), http.StatusInternalServerError, ""},
		}},
		{"block", "/p2p/block", blockGossipBody(idB, domain.Block{Hash: hashX}), []struct {
			err    error
			status int
			code   string
		}{
			{p2p.ErrUnknownPeer, http.StatusForbidden, "unknown_peer"},
			{notOnTip, http.StatusConflict, "not_on_tip"},
			{invalid, http.StatusUnprocessableEntity, "invalid_block"},
			{p2p.ErrGossipDisabled, http.StatusNotFound, "p2p_disabled"},
			{errors.New("boom"), http.StatusInternalServerError, ""},
		}},
		{"tx", "/p2p/tx", txGossipBody(idB, sampleTx()), []struct {
			err    error
			status int
			code   string
		}{
			{p2p.ErrUnknownPeer, http.StatusForbidden, "unknown_peer"},
			{invalid, http.StatusUnprocessableEntity, "invalid_tx"},
			{p2p.ErrGossipDisabled, http.StatusNotFound, "p2p_disabled"},
			{errors.New("boom"), http.StatusInternalServerError, ""},
		}},
	}
	for _, r := range routes {
		for _, c := range r.cases {
			t.Run(fmt.Sprintf("%s/%d", r.name, c.status), func(t *testing.T) {
				rec := do(p2pHandler(t, Config{P2P: &fakePeers{gossipErr: c.err}}), http.MethodPost, r.path, r.body)
				if rec.Code != c.status || errCode(t, rec.Body.String()) != c.code {
					t.Fatalf("got %d %s, want %d %q", rec.Code, rec.Body, c.status, c.code)
				}
			})
		}
	}
}

func TestPostBlockStatus(t *testing.T) {
	blk := domain.Block{Index: 4, Hash: hashX, PrevHash: hashY}

	for accepted, want := range map[bool]string{true: "accepted", false: "known"} {
		f := &fakePeers{accepted: accepted}

		rec := do(p2pHandler(t, Config{P2P: f}), http.MethodPost, "/p2p/block", blockGossipBody(idB, blk))
		if rec.Code != http.StatusOK || rec.Body.String() != "{\n  \"status\": \""+want+"\"\n}" {
			t.Fatalf("accepted=%v: %d %s", accepted, rec.Code, rec.Body)
		}

		if f.gotFrom != idB || f.gotBlock == nil || f.gotBlock.Hash != hashX || f.gotBlock.Index != 4 {
			t.Fatalf("ReceiveBlock got %q %+v", f.gotFrom, f.gotBlock)
		}
	}
}

func TestPostTxStatus(t *testing.T) {
	tx := sampleTx()

	for accepted, want := range map[bool]string{true: "accepted", false: "known"} {
		f := &fakePeers{accepted: accepted}

		rec := do(p2pHandler(t, Config{P2P: f}), http.MethodPost, "/p2p/tx", txGossipBody(idB, tx))
		if rec.Code != http.StatusOK || rec.Body.String() != "{\n  \"status\": \""+want+"\"\n}" {
			t.Fatalf("accepted=%v: %d %s", accepted, rec.Code, rec.Body)
		}

		if f.gotFrom != idB || f.gotTx.ID != hashX || len(f.gotTx.Inputs) != 1 || !f.gotTx.Timestamp.Equal(tx.Timestamp) {
			t.Fatalf("ReceiveTx got %q %+v", f.gotFrom, f.gotTx)
		}
	}
}

func TestGossipRejectsBadBodies(t *testing.T) {
	good := map[string]string{
		"/p2p/inv":   invBody(idB, p2p.InvItem{Type: p2p.InvTx, Hash: hashX}),
		"/p2p/block": blockGossipBody(idB, domain.Block{Hash: hashX}),
		"/p2p/tx":    txGossipBody(idB, sampleTx()),
	}
	codes := map[string]string{"/p2p/inv": "invalid_inv", "/p2p/block": "invalid_body", "/p2p/tx": "invalid_body"}

	for path, body := range good {
		cases := map[string]string{
			"not json":      "{",
			"empty":         "",
			"unknown field": strings.Replace(body, `{`, `{"extra":1,`, 1),
			"trailing data": body + body,
			"trailing junk": body + " x",
		}
		for name, bad := range cases {
			t.Run(path+"/"+name, func(t *testing.T) {
				f := &fakePeers{}

				rec := do(p2pHandler(t, Config{P2P: f}), http.MethodPost, path, bad)
				if rec.Code != http.StatusBadRequest || errCode(t, rec.Body.String()) != codes[path] || f.gotFrom != "" {
					t.Fatalf("got %d %s", rec.Code, rec.Body)
				}
			})
		}

		t.Run(path+"/trailing whitespace", func(t *testing.T) {
			rec := do(p2pHandler(t, Config{P2P: &fakePeers{}}), http.MethodPost, path, body+"\n \n")
			if rec.Code != http.StatusOK {
				t.Fatalf("got %d %s", rec.Code, rec.Body)
			}
		})

		t.Run(path+"/body limit", func(t *testing.T) {
			big := strings.TrimSuffix(body, "}") + `,"pad":"` + strings.Repeat("a", int(maxRequestBodyBytes())) + `"}`

			rec := do(p2pHandler(t, Config{P2P: &fakePeers{}}), http.MethodPost, path, big)
			if rec.Code != http.StatusRequestEntityTooLarge || errCode(t, rec.Body.String()) != requestTooLargeCode {
				t.Fatalf("got %d %s", rec.Code, rec.Body)
			}
		})
	}
}

func TestGossipDisabledWithoutPeers(t *testing.T) {
	h := p2pHandler(t, Config{})
	for path, body := range map[string]string{
		"/p2p/inv":   invBody(idB, p2p.InvItem{Type: p2p.InvTx, Hash: hashX}),
		"/p2p/block": blockGossipBody(idB, domain.Block{Hash: hashX}),
		"/p2p/tx":    txGossipBody(idB, sampleTx()),
	} {
		rec := do(h, http.MethodPost, path, body)
		if rec.Code != http.StatusNotFound || errCode(t, rec.Body.String()) != "p2p_disabled" {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body)
		}
	}
}

func TestGossipMethodNotAllowed(t *testing.T) {
	for _, cfg := range []Config{{}, {P2P: &fakePeers{}}} {
		h := p2pHandler(t, cfg)

		for _, path := range []string{"/p2p/inv", "/p2p/block", "/p2p/tx"} {
			for _, m := range []string{http.MethodGet, http.MethodPut} {
				rec := do(h, m, path, "")
				if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "POST" || rec.Body.Len() != 0 {
					t.Fatalf("%s %s: %d allow=%q", m, path, rec.Code, rec.Header().Get("Allow"))
				}
			}
		}
	}
}

func TestMineAnnouncesTheBlock(t *testing.T) {
	svc, funds := realChain(t)
	f := &fakePeers{}

	srv, err := NewServer(svc, Config{P2P: f})
	if err != nil {
		t.Fatal(err)
	}

	h := srv.Handler()

	if rec := do(h, http.MethodPost, "/tx", txBody(funds.tx(newWallet().Address(), 42))); rec.Code != http.StatusAccepted {
		t.Fatalf("tx = %d %s", rec.Code, rec.Body)
	}

	rec := do(h, http.MethodPost, "/mine", "")
	if rec.Code != http.StatusCreated {
		t.Fatalf("mine = %d %s", rec.Code, rec.Body)
	}

	var blk domain.Block

	_ = json.Unmarshal(rec.Body.Bytes(), &blk)

	if len(f.announcedBlk) != 1 || f.announcedBlk[0].Hash != blk.Hash || blk.Hash == "" {
		t.Fatalf("announced %+v, mined %s", f.announcedBlk, blk.Hash)
	}

	if len(f.announcedTx) != 1 {
		t.Fatalf("tx announced %d times", len(f.announcedTx))
	}
}

func TestMineFailureDoesNotAnnounce(t *testing.T) {
	f := &fakePeers{}

	srv, err := NewServer(brokenChain{}, Config{P2P: f})
	if err != nil {
		t.Fatal(err)
	}

	rec := do(srv.Handler(), http.MethodPost, "/mine", "")
	if rec.Code != http.StatusInternalServerError || len(f.announcedBlk) != 0 {
		t.Fatalf("mine = %d, announced %d", rec.Code, len(f.announcedBlk))
	}
}

func TestSubmitTxRejectedDoesNotAnnounce(t *testing.T) {
	svc, funds := realChain(t)
	f := &fakePeers{}

	srv, err := NewServer(svc, Config{P2P: f})
	if err != nil {
		t.Fatal(err)
	}

	h := srv.Handler()
	tx := funds.tx(newWallet().Address(), 42)

	do(h, http.MethodPost, "/tx", txBody(tx))

	if rec := do(h, http.MethodPost, "/tx", txBody(tx)); rec.Code != http.StatusConflict {
		t.Fatalf("duplicate = %d %s", rec.Code, rec.Body)
	}

	if len(f.announcedTx) != 1 || f.announcedTx[0].ID != tx.ID {
		t.Fatalf("announced %d txs", len(f.announcedTx))
	}
}

func TestMineAndSubmitWithoutPeersStillWork(t *testing.T) {
	svc, funds := realChain(t)

	srv, err := NewServer(svc, Config{})
	if err != nil {
		t.Fatal(err)
	}

	h := srv.Handler()
	if rec := do(h, http.MethodPost, "/tx", txBody(funds.tx(newWallet().Address(), 42))); rec.Code != http.StatusAccepted {
		t.Fatalf("tx = %d", rec.Code)
	}

	if rec := do(h, http.MethodPost, "/mine", ""); rec.Code != http.StatusCreated {
		t.Fatalf("mine = %d %s", rec.Code, rec.Body)
	}
}
