package server

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/petersonsalme/blockchain-with-go/internal/domain"
)

type tipChain struct {
	brokenChain

	tip *domain.Block
}

func (c tipChain) Tip() (*domain.Block, error) {
	return c.tip, nil
}

func TestSupplyHandler(t *testing.T) {
	tests := []struct {
		height int
		want   supplyResponse
	}{
		{
			height: 0,
			want: supplyResponse{
				Height:          0,
				Issued:          "0",
				InitialSubsidy:  "5000000000",
				HalvingInterval: 210000,
			},
		},
		{
			height: 1,
			want: supplyResponse{
				Height:          1,
				Issued:          "5000000000",
				InitialSubsidy:  "5000000000",
				HalvingInterval: 210000,
			},
		},
		{
			height: 210000,
			want: supplyResponse{
				Height:          210000,
				Issued:          "1049997500000000",
				InitialSubsidy:  "5000000000",
				HalvingInterval: 210000,
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

			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/supply", nil)
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("expected status 200, got %d", rec.Code)
			}

			var got supplyResponse

			err = json.Unmarshal(rec.Body.Bytes(), &got)
			if err != nil {
				t.Fatalf("unmarshal supplyResponse: %v", err)
			}

			if got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestSupplyUnhealthy(t *testing.T) {
	srv, err := NewServer(brokenChain{}, Config{})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/supply", nil)
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

func TestSupplyMethodNotAllowed(t *testing.T) {
	chain := tipChain{tip: &domain.Block{Index: 0, Hash: "h"}}

	srv, err := NewServer(chain, Config{})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/supply", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected status 405, got %d", rec.Code)
	}

	if allow := rec.Header().Get("Allow"); allow != "GET, HEAD" {
		t.Errorf("expected Allow header 'GET, HEAD', got %q", allow)
	}
}

func TestScheduledIssuance(t *testing.T) {
	tests := []struct {
		height int
		want   uint64
	}{
		{height: -1, want: 0},
		{height: 0, want: 0},
		{height: 1, want: 5_000_000_000},
		{height: 2, want: 10_000_000_000},
		{height: 209_999, want: 1_049_995_000_000_000},
		{height: 210_000, want: 1_049_997_500_000_000},
		{height: 420_000, want: 1_574_996_250_000_000},
	}

	for _, tc := range tests {
		t.Run(strconv.Itoa(tc.height), func(t *testing.T) {
			got := scheduledIssuance(tc.height)
			if got != tc.want {
				t.Fatalf("scheduledIssuance(%d) = %d, want %d", tc.height, got, tc.want)
			}
		})
	}
}

func TestScheduledIssuanceStep(t *testing.T) {
	steps := []int{1, 2, 209_998, 209_999, 210_000, 210_001, 419_999, 420_000, 420_001, 6_929_999, 6_930_000}

	for _, h := range steps {
		got := scheduledIssuance(h) - scheduledIssuance(h-1)
		want := domain.Subsidy(h)

		if got != want {
			t.Fatalf("step at %d: got %d, want %d", h, got, want)
		}
	}
}

func TestScheduledIssuanceCap(t *testing.T) {
	const wantCap uint64 = 2_099_994_997_690_000

	cases := []struct {
		name   string
		height int
	}{
		{name: "6929999", height: 6_929_999},
		{name: "6930000", height: 6_930_000},
		{name: "MaxInt", height: math.MaxInt},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := scheduledIssuance(tc.height)
			if got != wantCap {
				t.Fatalf("scheduledIssuance(%s) = %d, want %d", tc.name, got, wantCap)
			}
		})
	}
}
