package server

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/petersonsalme/blockchain-with-go/internal/domain"
	"github.com/petersonsalme/blockchain-with-go/internal/p2p"
)

func FuzzP2PInvBody(f *testing.F) {
	fuzzP2PSeeds(f, invBody(idB, p2p.InvItem{Type: p2p.InvBlock, Hash: hashX}, p2p.InvItem{Type: p2p.InvTx, Hash: hashY}))

	f.Fuzz(func(t *testing.T, body []byte) {
		rec, peers := fuzzP2PPost(t, "/p2p/inv", body)

		checkP2PFuzz(t, rec, peers.calls, "invalid_inv")

		if rec.Code == http.StatusOK {
			var got struct {
				Want json.RawMessage `json:"want"`
			}

			err := json.Unmarshal(rec.Body.Bytes(), &got)
			if err != nil {
				t.Fatalf("200 body does not unmarshal: %v", err)
			}

			if string(got.Want) != "[]" {
				t.Fatalf("200 body want field %q, want exactly []", string(got.Want))
			}
		}
	})
}

func FuzzP2PBlockBody(f *testing.F) {
	fuzzP2PSeeds(f, blockGossipBody(idB, domain.Block{Index: 1, Timestamp: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC), Hash: hashX, PrevHash: hashY, Difficulty: 2}))

	f.Fuzz(func(t *testing.T, body []byte) {
		rec, peers := fuzzP2PPost(t, "/p2p/block", body)

		checkP2PFuzz(t, rec, peers.calls, "invalid_body")

		if rec.Code == http.StatusOK {
			var got map[string]string

			err := json.Unmarshal(rec.Body.Bytes(), &got)
			if err != nil {
				t.Fatalf("200 body does not unmarshal: %v", err)
			}

			if len(got) != 1 || got["status"] != "accepted" {
				t.Fatalf("200 body %v, want {\"status\":\"accepted\"}", got)
			}
		}
	})
}

func FuzzP2PTxBody(f *testing.F) {
	fuzzP2PSeeds(f, txGossipBody(idB, sampleTx()))

	f.Fuzz(func(t *testing.T, body []byte) {
		rec, peers := fuzzP2PPost(t, "/p2p/tx", body)

		checkP2PFuzz(t, rec, peers.calls, "invalid_body")

		if rec.Code == http.StatusOK {
			var got map[string]string

			err := json.Unmarshal(rec.Body.Bytes(), &got)
			if err != nil {
				t.Fatalf("200 body does not unmarshal: %v", err)
			}

			if len(got) != 1 || got["status"] != "accepted" {
				t.Fatalf("200 body %v, want {\"status\":\"accepted\"}", got)
			}
		}
	})
}
