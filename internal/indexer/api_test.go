package indexer_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/petersonsalme/blockchain-with-go/internal/indexer"
)

type fakeReader struct {
	healthFn    func(ctx context.Context) (indexer.Tip, error)
	supplyTipFn func(ctx context.Context) (indexer.SupplyRow, error)
	supplyAtFn  func(ctx context.Context, height int) (indexer.SupplyRow, error)
	richestFn   func(ctx context.Context, limit int) (indexer.RichestSnapshot, error)
	statsFn     func(ctx context.Context, from, to int) (indexer.RangeStats, error)
}

func (f *fakeReader) Health(ctx context.Context) (indexer.Tip, error) {
	if f.healthFn != nil {
		return f.healthFn(ctx)
	}

	return indexer.Tip{}, nil
}

func (f *fakeReader) SupplyTip(ctx context.Context) (indexer.SupplyRow, error) {
	if f.supplyTipFn != nil {
		return f.supplyTipFn(ctx)
	}

	return indexer.SupplyRow{}, nil
}

func (f *fakeReader) SupplyAt(ctx context.Context, height int) (indexer.SupplyRow, error) {
	if f.supplyAtFn != nil {
		return f.supplyAtFn(ctx, height)
	}

	return indexer.SupplyRow{}, nil
}

func (f *fakeReader) Richest(ctx context.Context, limit int) (indexer.RichestSnapshot, error) {
	if f.richestFn != nil {
		return f.richestFn(ctx, limit)
	}

	return indexer.RichestSnapshot{}, nil
}

func (f *fakeReader) Stats(ctx context.Context, from, to int) (indexer.RangeStats, error) {
	if f.statsFn != nil {
		return f.statsFn(ctx, from, to)
	}

	return indexer.RangeStats{}, nil
}

type apiErrorBody struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

func doAPIRequest(t *testing.T, handler http.Handler, method, target string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), method, target, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	return rec
}

func decodeAPIJSON[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()

	var got T

	err := json.Unmarshal(rec.Body.Bytes(), &got)
	if err != nil {
		t.Fatalf("decode JSON: %v (body: %s)", err, rec.Body.String())
	}

	return got
}

func assertAPIError(t *testing.T, rec *httptest.ResponseRecorder, wantStatus int, wantCode string) {
	t.Helper()

	if rec.Code != wantStatus {
		t.Fatalf("status = %d, want %d (body: %s)", rec.Code, wantStatus, rec.Body.String())
	}

	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q, want %q", ct, "application/json")
	}

	got := decodeAPIJSON[apiErrorBody](t, rec)

	if got.Code != wantCode {
		t.Fatalf("error code = %q, want %q", got.Code, wantCode)
	}

	if got.Error == "" {
		t.Fatalf("error message is empty, want non-empty")
	}
}

func assertMethodNotAllowed(t *testing.T, handler http.Handler, path string) {
	t.Helper()

	rec := doAPIRequest(t, handler, http.MethodPost, path)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST %s status = %d, want %d", path, rec.Code, http.StatusMethodNotAllowed)
	}

	if allow := rec.Header().Get("Allow"); allow != "GET, HEAD" {
		t.Fatalf("POST %s Allow = %q, want %q", path, allow, "GET, HEAD")
	}
}

func TestAPIHealth(t *testing.T) {
	t.Parallel()

	t.Run("healthy", func(t *testing.T) {
		t.Parallel()

		h := indexer.NewAPI(&fakeReader{
			healthFn: func(context.Context) (indexer.Tip, error) {
				return indexer.Tip{Height: 42, Hash: "abc123"}, nil
			},
		})

		rec := doAPIRequest(t, h, http.MethodGet, "/health")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}

		if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
			t.Fatalf("Content-Type = %q, want %q", ct, "application/json")
		}

		got := decodeAPIJSON[map[string]any](t, rec)

		if got["height"] != float64(42) || got["hash"] != "abc123" {
			t.Fatalf("body = %v, want height=42 hash=abc123", got)
		}
	})

	t.Run("unavailable", func(t *testing.T) {
		t.Parallel()

		h := indexer.NewAPI(&fakeReader{
			healthFn: func(context.Context) (indexer.Tip, error) {
				return indexer.Tip{}, errors.New("connection refused")
			},
		})

		rec := doAPIRequest(t, h, http.MethodGet, "/health")
		assertAPIError(t, rec, http.StatusServiceUnavailable, "unavailable")
	})

	t.Run("method_not_allowed", func(t *testing.T) {
		t.Parallel()

		assertMethodNotAllowed(t, indexer.NewAPI(&fakeReader{}), "/health")
	})
}

func TestAPISupply(t *testing.T) {
	t.Parallel()

	tipRow := indexer.SupplyRow{
		Height:   10,
		Hash:     "tiphash",
		Total:    "500",
		Created:  "550",
		Spent:    "50",
		Coinbase: "500",
	}

	heightZeroRow := indexer.SupplyRow{
		Height:   0,
		Hash:     "genesishash",
		Total:    "100",
		Created:  "100",
		Spent:    "0",
		Coinbase: "50",
	}

	t.Run("tip", func(t *testing.T) {
		t.Parallel()

		h := indexer.NewAPI(&fakeReader{
			supplyTipFn: func(context.Context) (indexer.SupplyRow, error) {
				return tipRow, nil
			},
		})

		rec := doAPIRequest(t, h, http.MethodGet, "/supply")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}

		got := decodeAPIJSON[indexer.SupplyRow](t, rec)

		if got != tipRow {
			t.Fatalf("got %+v, want %+v", got, tipRow)
		}
	})

	t.Run("explicit_height", func(t *testing.T) {
		t.Parallel()

		var gotHeight int

		h := indexer.NewAPI(&fakeReader{
			supplyAtFn: func(_ context.Context, height int) (indexer.SupplyRow, error) {
				gotHeight = height
				return heightZeroRow, nil
			},
		})

		rec := doAPIRequest(t, h, http.MethodGet, "/supply?height=0")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}

		if gotHeight != 0 {
			t.Fatalf("SupplyAt height = %d, want 0", gotHeight)
		}

		got := decodeAPIJSON[indexer.SupplyRow](t, rec)

		if got != heightZeroRow {
			t.Fatalf("got %+v, want %+v", got, heightZeroRow)
		}
	})

	t.Run("height_not_indexed", func(t *testing.T) {
		t.Parallel()

		h := indexer.NewAPI(&fakeReader{
			supplyAtFn: func(context.Context, int) (indexer.SupplyRow, error) {
				return indexer.SupplyRow{}, indexer.ErrHeightNotIndexed
			},
		})

		rec := doAPIRequest(t, h, http.MethodGet, "/supply?height=99")
		assertAPIError(t, rec, http.StatusNotFound, "height_not_indexed")
	})

	t.Run("empty_db_on_tip", func(t *testing.T) {
		t.Parallel()

		h := indexer.NewAPI(&fakeReader{
			supplyTipFn: func(context.Context) (indexer.SupplyRow, error) {
				return indexer.SupplyRow{}, indexer.ErrHeightNotIndexed
			},
		})

		rec := doAPIRequest(t, h, http.MethodGet, "/supply")
		assertAPIError(t, rec, http.StatusNotFound, "height_not_indexed")
	})

	for _, tc := range []struct {
		name  string
		query string
	}{
		{name: "negative_height", query: "?height=-1"},
		{name: "non_integer_height", query: "?height=abc"},
		{name: "empty_height", query: "?height="},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rec := doAPIRequest(t, indexer.NewAPI(&fakeReader{}), http.MethodGet, "/supply"+tc.query)
			assertAPIError(t, rec, http.StatusBadRequest, "invalid_query")
		})
	}

	t.Run("unavailable_tip", func(t *testing.T) {
		t.Parallel()

		h := indexer.NewAPI(&fakeReader{
			supplyTipFn: func(context.Context) (indexer.SupplyRow, error) {
				return indexer.SupplyRow{}, errors.New("db down")
			},
		})

		rec := doAPIRequest(t, h, http.MethodGet, "/supply")
		assertAPIError(t, rec, http.StatusServiceUnavailable, "unavailable")
	})

	t.Run("unavailable_height", func(t *testing.T) {
		t.Parallel()

		h := indexer.NewAPI(&fakeReader{
			supplyAtFn: func(context.Context, int) (indexer.SupplyRow, error) {
				return indexer.SupplyRow{}, errors.New("db down")
			},
		})

		rec := doAPIRequest(t, h, http.MethodGet, "/supply?height=1")
		assertAPIError(t, rec, http.StatusServiceUnavailable, "unavailable")
	})

	t.Run("method_not_allowed", func(t *testing.T) {
		t.Parallel()

		assertMethodNotAllowed(t, indexer.NewAPI(&fakeReader{}), "/supply")
	})
}

func TestAPIRichest(t *testing.T) {
	t.Parallel()

	sample := indexer.RichestSnapshot{
		Height: 5,
		Hash:   "h5",
		Total:  "1000",
		Addresses: []indexer.RichAddress{
			{Address: "addr1", Balance: "750", Share: "0.7500"},
			{Address: "addr2", Balance: "250", Share: "0.2500"},
		},
	}

	for _, tc := range []struct {
		name      string
		target    string
		wantLimit int
	}{
		{name: "default_limit", target: "/richest", wantLimit: 20},
		{name: "empty_limit_uses_default", target: "/richest?limit=", wantLimit: 20},
		{name: "explicit_limit", target: "/richest?limit=5", wantLimit: 5},
		{name: "clamped_limit", target: "/richest?limit=250", wantLimit: 100},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var gotLimit int

			h := indexer.NewAPI(&fakeReader{
				richestFn: func(_ context.Context, limit int) (indexer.RichestSnapshot, error) {
					gotLimit = limit
					return sample, nil
				},
			})

			rec := doAPIRequest(t, h, http.MethodGet, tc.target)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusOK, rec.Body.String())
			}

			if gotLimit != tc.wantLimit {
				t.Fatalf("Richest limit = %d, want %d", gotLimit, tc.wantLimit)
			}

			got := decodeAPIJSON[indexer.RichestSnapshot](t, rec)

			if !reflect.DeepEqual(got, sample) {
				t.Fatalf("got %+v, want %+v", got, sample)
			}
		})
	}

	t.Run("nil_addresses_encoded_as_empty_array", func(t *testing.T) {
		t.Parallel()

		h := indexer.NewAPI(&fakeReader{
			richestFn: func(context.Context, int) (indexer.RichestSnapshot, error) {
				return indexer.RichestSnapshot{Height: 0, Hash: "g", Total: "0"}, nil
			},
		})

		rec := doAPIRequest(t, h, http.MethodGet, "/richest")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}

		raw := decodeAPIJSON[map[string]any](t, rec)

		addrs, ok := raw["addresses"].([]any)
		if !ok || len(addrs) != 0 {
			t.Fatalf("addresses = %#v, want empty JSON array", raw["addresses"])
		}
	})

	for _, tc := range []struct {
		name  string
		query string
	}{
		{name: "zero_limit", query: "?limit=0"},
		{name: "negative_limit", query: "?limit=-5"},
		{name: "non_integer_limit", query: "?limit=abc"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rec := doAPIRequest(t, indexer.NewAPI(&fakeReader{}), http.MethodGet, "/richest"+tc.query)
			assertAPIError(t, rec, http.StatusBadRequest, "invalid_query")
		})
	}

	t.Run("unavailable_on_height_not_indexed", func(t *testing.T) {
		t.Parallel()

		h := indexer.NewAPI(&fakeReader{
			richestFn: func(context.Context, int) (indexer.RichestSnapshot, error) {
				return indexer.RichestSnapshot{}, indexer.ErrHeightNotIndexed
			},
		})

		rec := doAPIRequest(t, h, http.MethodGet, "/richest")
		assertAPIError(t, rec, http.StatusServiceUnavailable, "unavailable")
	})

	t.Run("unavailable_on_db_error", func(t *testing.T) {
		t.Parallel()

		h := indexer.NewAPI(&fakeReader{
			richestFn: func(context.Context, int) (indexer.RichestSnapshot, error) {
				return indexer.RichestSnapshot{}, errors.New("db error")
			},
		})

		rec := doAPIRequest(t, h, http.MethodGet, "/richest")
		assertAPIError(t, rec, http.StatusServiceUnavailable, "unavailable")
	})

	t.Run("method_not_allowed", func(t *testing.T) {
		t.Parallel()

		assertMethodNotAllowed(t, indexer.NewAPI(&fakeReader{}), "/richest")
	})
}

func TestAPIStats(t *testing.T) {
	t.Parallel()

	sample := indexer.RangeStats{
		From:     0,
		To:       3,
		Blocks:   4,
		Txs:      7,
		Created:  "200",
		Spent:    "50",
		Coinbase: "150",
	}

	for _, tc := range []struct {
		name     string
		target   string
		wantFrom int
		wantTo   int
	}{
		{name: "small_range", target: "/stats?from=0&to=3", wantFrom: 0, wantTo: 3},
		{name: "boundary_span_10000", target: "/stats?from=0&to=10000", wantFrom: 0, wantTo: 10000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var gotFrom, gotTo int

			h := indexer.NewAPI(&fakeReader{
				statsFn: func(_ context.Context, from, to int) (indexer.RangeStats, error) {
					gotFrom = from
					gotTo = to

					return sample, nil
				},
			})

			rec := doAPIRequest(t, h, http.MethodGet, tc.target)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusOK, rec.Body.String())
			}

			if gotFrom != tc.wantFrom || gotTo != tc.wantTo {
				t.Fatalf("Stats(%d, %d), want (%d, %d)", gotFrom, gotTo, tc.wantFrom, tc.wantTo)
			}

			got := decodeAPIJSON[indexer.RangeStats](t, rec)

			if got != sample {
				t.Fatalf("got %+v, want %+v", got, sample)
			}
		})
	}

	for _, tc := range []struct {
		name  string
		query string
	}{
		{name: "span_over_10000", query: "?from=0&to=10001"},
		{name: "to_less_than_from", query: "?from=3&to=2"},
		{name: "negative_from", query: "?from=-1&to=2"},
		{name: "missing_from", query: "?to=2"},
		{name: "missing_to", query: "?from=0"},
		{name: "missing_both", query: ""},
		{name: "empty_from", query: "?from=&to=2"},
		{name: "empty_to", query: "?from=0&to="},
		{name: "non_integer", query: "?from=a&to=b"},
		{name: "non_integer_to", query: "?from=0&to=b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rec := doAPIRequest(t, indexer.NewAPI(&fakeReader{}), http.MethodGet, "/stats"+tc.query)
			assertAPIError(t, rec, http.StatusBadRequest, "invalid_query")
		})
	}

	t.Run("unavailable", func(t *testing.T) {
		t.Parallel()

		h := indexer.NewAPI(&fakeReader{
			statsFn: func(context.Context, int, int) (indexer.RangeStats, error) {
				return indexer.RangeStats{}, errors.New("db error")
			},
		})

		rec := doAPIRequest(t, h, http.MethodGet, "/stats?from=0&to=3")
		assertAPIError(t, rec, http.StatusServiceUnavailable, "unavailable")
	})

	t.Run("method_not_allowed", func(t *testing.T) {
		t.Parallel()

		assertMethodNotAllowed(t, indexer.NewAPI(&fakeReader{}), "/stats")
	})
}
