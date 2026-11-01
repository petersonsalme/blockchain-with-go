package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/petersonsalme/blockchain-with-go/internal/domain"
)

type fakeConsensusChain struct {
	brokenChain

	blocks []*domain.Block
	tip    *domain.Block
}

func newFakeConsensusChain(n int, difficulty int) *fakeConsensusChain {
	blocks := make([]*domain.Block, n)
	prev := ""

	for i := range n {
		hash := fmt.Sprintf("h%d", i)
		blocks[i] = &domain.Block{
			Index:      i,
			Hash:       hash,
			PrevHash:   prev,
			Difficulty: difficulty,
		}
		prev = hash
	}

	var tip *domain.Block
	if n > 0 {
		tip = blocks[n-1]
	}

	return &fakeConsensusChain{
		blocks: blocks,
		tip:    tip,
	}
}

func (c *fakeConsensusChain) Tip() (*domain.Block, error) {
	if c.tip != nil {
		return c.tip, nil
	}

	return nil, errors.New("empty chain")
}

func (c *fakeConsensusChain) BlocksRange(from, limit int) ([]*domain.Block, error) {
	if from < 0 || from >= len(c.blocks) {
		return nil, nil
	}

	end := min(from+limit, len(c.blocks))

	return c.blocks[from:end], nil
}

func TestConsensusChainWorkPoW(t *testing.T) {
	fc := newFakeConsensusChain(1501, 3)
	info := &ConsensusInfo{
		Engine:           "pow",
		Difficulty:       3,
		RetargetInterval: 10,
		TargetBlockTime:  1500 * time.Millisecond,
	}

	srv, err := NewServer(fc, Config{Consensus: info})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/consensus", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var raw map[string]json.RawMessage

	err = json.Unmarshal(rec.Body.Bytes(), &raw)
	if err != nil {
		t.Fatalf("json unmarshal: %v", err)
	}

	if _, ok := raw["poa"]; ok {
		t.Fatalf("expected no poa key in body: %s", rec.Body.String())
	}

	var resp consensusResponse

	err = json.Unmarshal(rec.Body.Bytes(), &resp)
	if err != nil {
		t.Fatalf("unmarshal consensusResponse: %v", err)
	}

	if resp.Engine != "pow" {
		t.Errorf("expected engine pow, got %q", resp.Engine)
	}

	if resp.Height != 1500 {
		t.Errorf("expected height 1500, got %d", resp.Height)
	}

	if resp.Tip != "h1500" {
		t.Errorf("expected tip h1500, got %q", resp.Tip)
	}

	if resp.ChainWork != "12008" {
		t.Errorf("expected chain_work 12008, got %q", resp.ChainWork)
	}

	if resp.PoW == nil {
		t.Fatalf("expected pow not nil")
	}

	if resp.PoW.InitialDifficulty != 3 || resp.PoW.RetargetInterval != 10 || resp.PoW.TargetBlockTimeMS != 1500 {
		t.Errorf("unexpected pow params: %+v", resp.PoW)
	}
}

func TestConsensusPoA(t *testing.T) {
	fc := newFakeConsensusChain(4, 0)
	info := &ConsensusInfo{
		Engine:     "poa",
		Validators: []string{"aa", "bb"},
	}

	srv, err := NewServer(fc, Config{Consensus: info})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/consensus", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var raw map[string]json.RawMessage

	err = json.Unmarshal(rec.Body.Bytes(), &raw)
	if err != nil {
		t.Fatalf("json unmarshal: %v", err)
	}

	if _, ok := raw["pow"]; ok {
		t.Fatalf("expected no pow key in body: %s", rec.Body.String())
	}

	var resp consensusResponse

	err = json.Unmarshal(rec.Body.Bytes(), &resp)
	if err != nil {
		t.Fatalf("unmarshal consensusResponse: %v", err)
	}

	if resp.Engine != "poa" {
		t.Errorf("expected engine poa, got %q", resp.Engine)
	}

	if resp.ChainWork != "4" {
		t.Errorf("expected chain_work 4, got %q", resp.ChainWork)
	}

	if resp.PoA == nil {
		t.Fatalf("expected poa not nil")
	}

	expectedValidators := []string{"aa", "bb"}
	if !reflect.DeepEqual(resp.PoA.Validators, expectedValidators) {
		t.Errorf("expected validators %v, got %v", expectedValidators, resp.PoA.Validators)
	}
}

func TestConsensusChainChanged(t *testing.T) {
	t.Run("hash mismatch", func(t *testing.T) {
		fc := newFakeConsensusChain(10, 1)
		fc.blocks[7].PrevHash = "mismatch"

		srv, err := NewServer(fc, Config{Consensus: &ConsensusInfo{Engine: "pow"}})
		if err != nil {
			t.Fatalf("NewServer: %v", err)
		}

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/consensus", nil)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)

		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("expected status 503, got %d", rec.Code)
		}

		var errResp errorResponse

		err = json.Unmarshal(rec.Body.Bytes(), &errResp)
		if err != nil {
			t.Fatalf("unmarshal errorResponse: %v", err)
		}

		if errResp.Code != chainChangedCode {
			t.Errorf("expected code %q, got %q", chainChangedCode, errResp.Code)
		}
	})

	t.Run("tip mismatch", func(t *testing.T) {
		fc := newFakeConsensusChain(10, 1)
		fc.tip = &domain.Block{
			Index:      9,
			Hash:       "different_tip_hash",
			PrevHash:   fc.blocks[8].Hash,
			Difficulty: 1,
		}

		srv, err := NewServer(fc, Config{Consensus: &ConsensusInfo{Engine: "pow"}})
		if err != nil {
			t.Fatalf("NewServer: %v", err)
		}

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/consensus", nil)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)

		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("expected status 503, got %d", rec.Code)
		}

		var errResp errorResponse

		err = json.Unmarshal(rec.Body.Bytes(), &errResp)
		if err != nil {
			t.Fatalf("unmarshal errorResponse: %v", err)
		}

		if errResp.Code != chainChangedCode {
			t.Errorf("expected code %q, got %q", chainChangedCode, errResp.Code)
		}
	})
}

func TestConsensusUnknown(t *testing.T) {
	fc := newFakeConsensusChain(1, 0)

	srv, err := NewServer(fc, Config{})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/consensus", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected status 404, got %d", rec.Code)
	}

	var errResp errorResponse

	err = json.Unmarshal(rec.Body.Bytes(), &errResp)
	if err != nil {
		t.Fatalf("unmarshal errorResponse: %v", err)
	}

	if errResp.Code != consensusUnknownCode {
		t.Errorf("expected code %q, got %q", consensusUnknownCode, errResp.Code)
	}
}

func TestConsensusUnhealthy(t *testing.T) {
	srv, err := NewServer(brokenChain{}, Config{Consensus: &ConsensusInfo{Engine: "pow"}})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/consensus", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected status 503, got %d", rec.Code)
	}

	var errResp errorResponse

	err = json.Unmarshal(rec.Body.Bytes(), &errResp)
	if err != nil {
		t.Fatalf("unmarshal errorResponse: %v", err)
	}

	if errResp.Code != unhealthyCode {
		t.Errorf("expected code %q, got %q", unhealthyCode, errResp.Code)
	}
}

func TestConsensusMethodNotAllowed(t *testing.T) {
	fc := newFakeConsensusChain(1, 0)

	srv, err := NewServer(fc, Config{Consensus: &ConsensusInfo{Engine: "pow"}})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/consensus", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected status 405, got %d", rec.Code)
	}

	if allow := rec.Header().Get("Allow"); allow != "GET, HEAD" {
		t.Errorf("expected Allow header 'GET, HEAD', got %q", allow)
	}
}
