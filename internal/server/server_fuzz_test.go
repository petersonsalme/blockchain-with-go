package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/petersonsalme/blockchain-with-go/internal/domain"
)

// fuzzChain is a Chain whose Mine returns a fixed block and counts its calls;
// every other method would panic (nil embedded interface), proving POST /
// reaches only Mine.
type fuzzChain struct {
	Chain
	mined int
}

func (c *fuzzChain) Mine(_ context.Context, _ []domain.Transaction) (*domain.Block, error) {
	c.mined++
	return &domain.Block{Index: 1, Timestamp: time.Date(2026, 10, 16, 12, 5, 0, 0, time.UTC), Hash: "00ab", PrevHash: "00cd", Difficulty: 2}, nil
}

const fuzzReadmePost = `{"Transactions":[{"Inputs":[{"PrevTxID":"ab","OutIndex":0,"PublicKey":"cd","Signature":"ef"}],"Outputs":[{"Amount":60,"Address":"addr"}],"Timestamp":"2026-10-16T12:05:00Z"}]}`

func FuzzWriteBlockBody(f *testing.F) {
	f.Add([]byte(fuzzReadmePost))
	f.Add([]byte(`{}`))
	f.Add([]byte(fuzzReadmePost[:len(fuzzReadmePost)/2]))
	f.Add([]byte(`{"Transactions":[{"Timestamp":"2026-10-16T12:05:00Z"}]}`))
	f.Add([]byte(`{"Transactions":[{"ID":"wrong","Timestamp":"2026-10-16T12:05:00Z"}]}`))
	f.Add([]byte(`{"Transactions":[{}]}`))
	f.Add([]byte(strings.Repeat(" ", int(maxRequestBodyBytes())+1)))

	f.Fuzz(func(t *testing.T, body []byte) {
		fake := &fuzzChain{}

		srv, err := NewServer(fake, Config{})
		if err != nil {
			t.Fatal(err)
		}

		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body)))

		switch rec.Code {
		case http.StatusCreated, http.StatusBadRequest, http.StatusRequestEntityTooLarge:
		default:
			t.Fatalf("status %d outside {201, 400, 413}", rec.Code)
		}

		if !json.Valid(rec.Body.Bytes()) {
			t.Fatalf("response body is not valid JSON: %q", rec.Body.String())
		}

		if wantMined := rec.Code == http.StatusCreated; (fake.mined == 1) != wantMined || fake.mined > 1 {
			t.Fatalf("status %d but Mine called %d times", rec.Code, fake.mined)
		}
	})
}
