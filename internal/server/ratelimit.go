package server

import (
	"math"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// RateLimitEnvVar overrides the default rate limit when set.
const RateLimitEnvVar = "RATE_LIMIT"

// sweepInterval bounds how often allow scans the buckets for eviction: every
// bucket refills from empty to full in one minute, whatever the rate (ADR-0013).
const sweepInterval = time.Minute

// ParseRateLimit reads a RATE_LIMIT value: positive integer requests per minute.
// Empty, invalid and non-positive values yield 0 (disabled).
func ParseRateLimit(raw string) int {
	v, err := strconv.Atoi(raw)
	if err != nil || v < 1 {
		return 0
	}

	return v
}

type ipBucket struct {
	tokens    float64
	lastCheck time.Time
}

type rateLimiter struct {
	mu        sync.Mutex
	rateLimit int
	now       func() time.Time
	buckets   map[string]*ipBucket
	lastSweep time.Time
}

func newRateLimiter(rateLimit int, now func() time.Time) *rateLimiter {
	if now == nil {
		now = time.Now
	}

	return &rateLimiter{
		rateLimit: rateLimit,
		now:       now,
		buckets:   make(map[string]*ipBucket),
	}
}

func (l *rateLimiter) allow(remoteAddr string) (bool, int) {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	refillRate := float64(l.rateLimit) / 60.0
	maxTokens := float64(l.rateLimit)

	if now.Sub(l.lastSweep) >= sweepInterval || now.Before(l.lastSweep) {
		for k, b := range l.buckets {
			if k != host && b.tokens+now.Sub(b.lastCheck).Seconds()*refillRate >= maxTokens {
				delete(l.buckets, k)
			}
		}

		l.lastSweep = now
	}

	b, ok := l.buckets[host]
	if !ok {
		l.buckets[host] = &ipBucket{
			tokens:    maxTokens - 1.0,
			lastCheck: now,
		}

		return true, 0
	}

	if now.After(b.lastCheck) {
		elapsed := now.Sub(b.lastCheck).Seconds()
		b.tokens = min(maxTokens, b.tokens+elapsed*refillRate)
		b.lastCheck = now
	}

	if b.tokens < 1.0 {
		missing := 1.0 - b.tokens
		retryAfter := int(math.Ceil(missing / refillRate))

		return false, retryAfter
	}

	b.tokens -= 1.0

	return true, 0
}

func (l *rateLimiter) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		allowed, retryAfter := l.allow(r.RemoteAddr)
		if !allowed {
			respondRateLimited(w, retryAfter)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func respondRateLimited(w http.ResponseWriter, retryAfter int) {
	w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
	respondWithJSON(w, http.StatusTooManyRequests, errorResponse{
		Error: "rate limit exceeded",
		Code:  rateLimitedCode,
	})
}
