package indexer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

const (
	codeUnavailable      = "unavailable"
	codeInvalidQuery     = "invalid_query"
	codeHeightNotIndexed = "height_not_indexed"

	defaultRichestLimit = 20
	maxRichestLimit     = 100
	maxStatsSpan        = 10000

	allowReadOnly = "GET, HEAD"
)

type apiError struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

type healthResponse struct {
	Height int    `json:"height"`
	Hash   string `json:"hash"`
}

// NewAPI returns an HTTP handler serving the read-only indexer endpoints
// (/health, /supply, /richest and /stats) over r.
func NewAPI(r Reader) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", handleHealth(r))
	mux.HandleFunc("GET /supply", handleSupply(r))
	mux.HandleFunc("GET /richest", handleRichest(r))
	mux.HandleFunc("GET /stats", handleStats(r))

	mux.Handle("/health", methodNotAllowed())
	mux.Handle("/supply", methodNotAllowed())
	mux.Handle("/richest", methodNotAllowed())
	mux.Handle("/stats", methodNotAllowed())

	return mux
}

func handleHealth(r Reader) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		tip, err := r.Health(req.Context())
		if err != nil {
			respondError(w, http.StatusServiceUnavailable, codeUnavailable, err.Error())
			return
		}

		respondJSON(w, http.StatusOK, healthResponse(tip))
	}
}

func handleSupply(r Reader) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		height, hasHeight, err := parseSupplyHeight(req.URL.Query())
		if err != nil {
			respondError(w, http.StatusBadRequest, codeInvalidQuery, err.Error())
			return
		}

		row, err := fetchSupply(req.Context(), r, height, hasHeight)
		if errors.Is(err, ErrHeightNotIndexed) {
			respondError(w, http.StatusNotFound, codeHeightNotIndexed, err.Error())
			return
		}

		if err != nil {
			respondError(w, http.StatusServiceUnavailable, codeUnavailable, err.Error())
			return
		}

		respondJSON(w, http.StatusOK, row)
	}
}

func fetchSupply(ctx context.Context, r Reader, height int, hasHeight bool) (SupplyRow, error) {
	if hasHeight {
		return r.SupplyAt(ctx, height)
	}

	return r.SupplyTip(ctx)
}

func handleRichest(r Reader) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		limit, err := parseRichestLimit(req.URL.Query())
		if err != nil {
			respondError(w, http.StatusBadRequest, codeInvalidQuery, err.Error())
			return
		}

		snap, err := r.Richest(req.Context(), limit)
		if err != nil {
			respondError(w, http.StatusServiceUnavailable, codeUnavailable, err.Error())
			return
		}

		if snap.Addresses == nil {
			snap.Addresses = []RichAddress{}
		}

		respondJSON(w, http.StatusOK, snap)
	}
}

func handleStats(r Reader) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		from, to, err := parseStatsRange(req.URL.Query())
		if err != nil {
			respondError(w, http.StatusBadRequest, codeInvalidQuery, err.Error())
			return
		}

		stats, err := r.Stats(req.Context(), from, to)
		if err != nil {
			respondError(w, http.StatusServiceUnavailable, codeUnavailable, err.Error())
			return
		}

		respondJSON(w, http.StatusOK, stats)
	}
}

func parseSupplyHeight(q url.Values) (int, bool, error) {
	if !q.Has("height") {
		return 0, false, nil
	}

	height, err := strconv.Atoi(q.Get("height"))
	if err != nil || height < 0 {
		return 0, true, errors.New("height must be a non-negative integer")
	}

	return height, true, nil
}

func parseRichestLimit(q url.Values) (int, error) {
	raw := q.Get("limit")
	if raw == "" {
		return defaultRichestLimit, nil
	}

	limit, err := strconv.Atoi(raw)
	if err != nil || limit <= 0 {
		return 0, errors.New("limit must be a positive integer")
	}

	return min(limit, maxRichestLimit), nil
}

func parseStatsRange(q url.Values) (int, int, error) {
	fromRaw := q.Get("from")
	toRaw := q.Get("to")

	if !q.Has("from") || fromRaw == "" || !q.Has("to") || toRaw == "" {
		return 0, 0, errors.New("from and to query parameters are required")
	}

	from, err := strconv.Atoi(fromRaw)
	if err != nil || from < 0 {
		return 0, 0, errors.New("from must be a non-negative integer")
	}

	to, err := strconv.Atoi(toRaw)
	if err != nil || to < from {
		return 0, 0, errors.New("to must be an integer greater than or equal to from")
	}

	if to-from > maxStatsSpan {
		return 0, 0, fmt.Errorf("range exceeds maximum span of %d heights", maxStatsSpan)
	}

	return from, to, nil
}

func methodNotAllowed() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Allow", allowReadOnly)
		w.WriteHeader(http.StatusMethodNotAllowed)
	})
}

func respondError(w http.ResponseWriter, code int, errCode, msg string) {
	respondJSON(w, code, apiError{Error: msg, Code: errCode})
}

func respondJSON(w http.ResponseWriter, code int, payload any) {
	response, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("HTTP 500: Internal Server Error"))

		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = w.Write(response)
}
