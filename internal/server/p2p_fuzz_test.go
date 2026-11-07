package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/petersonsalme/blockchain-with-go/internal/domain"
	"github.com/petersonsalme/blockchain-with-go/internal/p2p"
)

// fuzzPeers is a Peers that accepts everything and counts the calls of the
// four POST routes; any other method panics (nil embedded interface).
type fuzzPeers struct {
	// fuzzPort is Peers under another field name: embedding Peers itself would
	// name the field Peers, which clashes with the port method Peers().
	fuzzPort

	calls int
}

type fuzzPort = Peers

func (p *fuzzPeers) Accept(p2p.Hello) (p2p.Hello, error) { p.calls++; return fuzzLocalHello, nil }
func (p *fuzzPeers) Inv(string, []p2p.InvItem) ([]p2p.InvItem, error) {
	p.calls++
	return nil, nil
}

func (p *fuzzPeers) ReceiveBlock(context.Context, string, *domain.Block) (bool, error) {
	p.calls++
	return true, nil
}

func (p *fuzzPeers) ReceiveTx(string, domain.Transaction) (bool, error) { p.calls++; return true, nil }

var fuzzLocalHello = p2p.Hello{Version: 1, NodeID: idA, GenesisHash: strings.Repeat("0", 64), Height: 3, TipHash: strings.Repeat("2", 64), ListenAddr: "http://10.0.0.1:8080"}

// fuzzP2PPost posts body to path on a fresh Server whose peer layer is a fresh fuzzPeers.
func fuzzP2PPost(t *testing.T, path string, body []byte) (*httptest.ResponseRecorder, *fuzzPeers) {
	t.Helper()

	peers := &fuzzPeers{}

	srv, err := NewServer(&fuzzChain{}, Config{P2P: peers})
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodPost, path, bytes.NewReader(body)))

	return rec, peers
}

// checkP2PFuzz asserts what every /p2p POST guarantees for any body: JSON body and
// Content-Type application/json; 200 with exactly one port call; 400 with badCode or
// 413 with requestTooLargeCode, both with no port call; no other status.
func checkP2PFuzz(t *testing.T, rec *httptest.ResponseRecorder, calls int, badCode string) {
	t.Helper()

	if !json.Valid(rec.Body.Bytes()) {
		t.Fatalf("response body is not valid JSON: %q", rec.Body.String())
	}

	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("Content-Type %q, want application/json", ct)
	}

	switch rec.Code {
	case http.StatusOK:
		if calls != 1 {
			t.Fatalf("status 200 but the port was called %d times", calls)
		}
	case http.StatusBadRequest:
		if calls != 0 {
			t.Fatalf("status 400 but the port was called %d times", calls)
		}

		if code := errCode(t, rec.Body.String()); code != badCode {
			t.Fatalf("status 400 with code %q, want %q", code, badCode)
		}
	case http.StatusRequestEntityTooLarge:
		if calls != 0 {
			t.Fatalf("status 413 but the port was called %d times", calls)
		}

		if code := errCode(t, rec.Body.String()); code != requestTooLargeCode {
			t.Fatalf("status 413 with code %q, want %q", code, requestTooLargeCode)
		}
	default:
		t.Fatalf("status %d outside {200, 400, 413}", rec.Code)
	}
}

// fuzzP2PSeeds adds the seeds every /p2p target shares: valid, {}, valid cut in half,
// an unknown field, trailing data and a body one byte over maxRequestBodyBytes().
func fuzzP2PSeeds(f *testing.F, valid string) {
	f.Helper()

	f.Add([]byte(valid))
	f.Add([]byte(`{}`))
	f.Add([]byte(valid[:len(valid)/2]))
	f.Add([]byte(`{"unknown_field":1}`))
	f.Add([]byte(valid + "{}"))
	f.Add([]byte(strings.Repeat(" ", int(maxRequestBodyBytes())+1)))
}

func FuzzP2PHelloBody(f *testing.F) {
	fuzzP2PSeeds(f, helloBody(idB, 7))

	f.Fuzz(func(t *testing.T, body []byte) {
		rec, peers := fuzzP2PPost(t, "/p2p/hello", body)

		checkP2PFuzz(t, rec, peers.calls, "invalid_hello")

		if rec.Code == http.StatusOK {
			var got p2p.Hello

			err := json.Unmarshal(rec.Body.Bytes(), &got)
			if err != nil {
				t.Fatalf("200 body does not unmarshal to p2p.Hello: %v", err)
			}

			if got != fuzzLocalHello {
				t.Fatalf("200 body %+v, want %+v", got, fuzzLocalHello)
			}
		}
	})
}
