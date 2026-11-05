package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/petersonsalme/blockchain-with-go/internal/domain"
)

func TestNextHalvingHeight(t *testing.T) {
	tests := []struct {
		height int
		want   int
	}{
		{height: 0, want: 210000},
		{height: 1, want: 210000},
		{height: 209999, want: 210000},
		{height: 210000, want: 420000},
		{height: 420001, want: 630000},
		{height: 6929999, want: 6930000},
		{height: 6930000, want: 7140000},
	}

	for _, tc := range tests {
		t.Run(strconv.Itoa(tc.height), func(t *testing.T) {
			got := nextHalvingHeight(tc.height)
			if got != tc.want {
				t.Fatalf("nextHalvingHeight(%d) = %d, want %d", tc.height, got, tc.want)
			}
		})
	}
}

func TestHalvingHandler(t *testing.T) {
	tests := []struct {
		height int
		want   halvingResponse
	}{
		{
			height: 0,
			want: halvingResponse{
				Height:             0,
				CurrentSubsidy:     "5000000000",
				NextHalvingHeight:  210000,
				BlocksUntilHalving: 210000,
			},
		},
		{
			height: 1,
			want: halvingResponse{
				Height:             1,
				CurrentSubsidy:     "5000000000",
				NextHalvingHeight:  210000,
				BlocksUntilHalving: 209999,
			},
		},
		{
			height: 209999,
			want: halvingResponse{
				Height:             209999,
				CurrentSubsidy:     "5000000000",
				NextHalvingHeight:  210000,
				BlocksUntilHalving: 1,
			},
		},
		{
			height: 210000,
			want: halvingResponse{
				Height:             210000,
				CurrentSubsidy:     "2500000000",
				NextHalvingHeight:  420000,
				BlocksUntilHalving: 210000,
			},
		},
		{
			height: 420001,
			want: halvingResponse{
				Height:             420001,
				CurrentSubsidy:     "1250000000",
				NextHalvingHeight:  630000,
				BlocksUntilHalving: 209999,
			},
		},
		{
			height: 6929999,
			want: halvingResponse{
				Height:             6929999,
				CurrentSubsidy:     "1",
				NextHalvingHeight:  6930000,
				BlocksUntilHalving: 1,
			},
		},
		{
			height: 6930000,
			want: halvingResponse{
				Height:             6930000,
				CurrentSubsidy:     "0",
				NextHalvingHeight:  7140000,
				BlocksUntilHalving: 210000,
			},
		},
	}

	for _, tc := range tests {
		t.Run(strconv.Itoa(tc.height), func(t *testing.T) {
			chain := tipChain{tip: &domain.Block{Index: tc.height, Hash: "h"}}

			srv, err := NewServer(chain, Config{})
			if err != nil {
				t.Fatalf("NewServer: %v", err)
			}

			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/halving", nil)
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("expected status 200, got %d", rec.Code)
			}

			var got halvingResponse

			err = json.Unmarshal(rec.Body.Bytes(), &got)
			if err != nil {
				t.Fatalf("unmarshal halvingResponse: %v", err)
			}

			if got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}

	t.Run("HEAD", func(t *testing.T) {
		chain := tipChain{tip: &domain.Block{Index: 0, Hash: "h"}}

		srv, err := NewServer(chain, Config{})
		if err != nil {
			t.Fatalf("NewServer: %v", err)
		}

		req := httptest.NewRequestWithContext(t.Context(), http.MethodHead, "/halving", nil)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d", rec.Code)
		}
	})
}

func TestHalvingUnhealthy(t *testing.T) {
	srv, err := NewServer(brokenChain{}, Config{})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/halving", nil)
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

func TestHalvingMethodNotAllowed(t *testing.T) {
	chain := tipChain{tip: &domain.Block{Index: 0, Hash: "h"}}

	srv, err := NewServer(chain, Config{})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/halving", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected status 405, got %d", rec.Code)
	}

	if allow := rec.Header().Get("Allow"); allow != "GET, HEAD" {
		t.Errorf("expected Allow header 'GET, HEAD', got %q", allow)
	}
}
