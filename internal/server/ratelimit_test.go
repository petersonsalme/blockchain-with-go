package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/petersonsalme/blockchain-with-go/internal/domain"
)

type dummyTipChain struct{ brokenChain }

func (dummyTipChain) Tip() (*domain.Block, error) {
	return &domain.Block{Index: 0, Hash: "0000000000000000000000000000000000000000000000000000000000000000"}, nil
}

var _ = Describe("Rate Limiting", func() {
	Describe("ParseRateLimit", func() {
		It("parses valid positive integers", func() {
			Expect(ParseRateLimit("30")).To(Equal(30))
			Expect(ParseRateLimit("1")).To(Equal(1))
			Expect(ParseRateLimit("60")).To(Equal(60))
		})

		It("returns 0 for empty, invalid, or non-positive values", func() {
			Expect(ParseRateLimit("")).To(Equal(0))
			Expect(ParseRateLimit("abc")).To(Equal(0))
			Expect(ParseRateLimit("0")).To(Equal(0))
			Expect(ParseRateLimit("-5")).To(Equal(0))
			Expect(ParseRateLimit("-1")).To(Equal(0))
		})
	})

	Describe("Token bucket middleware", func() {
		var (
			currentTime time.Time
			nowFunc     func() time.Time
		)

		BeforeEach(func() {
			currentTime = time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
			nowFunc = func() time.Time {
				return currentTime
			}
		})

		It("allows up to N requests and returns 429 on N+1 within the same instant", func() {
			const n = 5
			var handlerCalls int64
			dummyHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				atomic.AddInt64(&handlerCalls, 1)
				w.WriteHeader(http.StatusOK)
			})

			limiter := newRateLimiter(n, nowFunc)
			wrapped := limiter.middleware(dummyHandler)

			for i := 0; i < n; i++ {
				req := httptest.NewRequest("GET", "/", nil)
				req.RemoteAddr = "192.168.1.100:1234"
				rec := httptest.NewRecorder()
				wrapped.ServeHTTP(rec, req)

				Expect(rec.Code).To(Equal(http.StatusOK))
			}
			Expect(atomic.LoadInt64(&handlerCalls)).To(Equal(int64(n)))

			// Request N+1 from same IP at same instant
			req := httptest.NewRequest("GET", "/", nil)
			req.RemoteAddr = "192.168.1.100:1234"
			rec := httptest.NewRecorder()
			wrapped.ServeHTTP(rec, req)

			Expect(rec.Code).To(Equal(http.StatusTooManyRequests))
			Expect(rec.Header().Get("Content-Type")).To(Equal("application/json"))

			retryAfter := rec.Header().Get("Retry-After")
			retrySec, err := strconv.Atoi(retryAfter)
			Expect(err).NotTo(HaveOccurred())
			Expect(retrySec).To(BeNumerically(">=", 1))

			var body map[string]any
			Expect(json.Unmarshal(rec.Body.Bytes(), &body)).To(Succeed())
			Expect(body["code"]).To(Equal("rate_limited"))
			Expect(body["error"]).To(Equal("rate limit exceeded"))

			// Handler must not be called when rejected
			Expect(atomic.LoadInt64(&handlerCalls)).To(Equal(int64(n)))
		})

		It("recovers tokens after advancing clock by 60s/N", func() {
			const n = 30 // refill rate: 30 / 60s = 1 token every 2s
			limiter := newRateLimiter(n, nowFunc)
			wrapped := limiter.middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))

			// Exhaust all tokens
			for i := 0; i < n; i++ {
				req := httptest.NewRequest("GET", "/", nil)
				req.RemoteAddr = "10.0.0.1:1234"
				rec := httptest.NewRecorder()
				wrapped.ServeHTTP(rec, req)
				Expect(rec.Code).To(Equal(http.StatusOK))
			}

			// 31st request is rate limited
			req := httptest.NewRequest("GET", "/", nil)
			req.RemoteAddr = "10.0.0.1:1234"
			rec := httptest.NewRecorder()
			wrapped.ServeHTTP(rec, req)
			Expect(rec.Code).To(Equal(http.StatusTooManyRequests))
			Expect(rec.Header().Get("Retry-After")).To(Equal("2"))

			// Advance clock by 1s (half token recovered, missing 0.5 tokens => 1s)
			currentTime = currentTime.Add(1 * time.Second)

			// Still rate limited, but Retry-After is 1s
			req = httptest.NewRequest("GET", "/", nil)
			req.RemoteAddr = "10.0.0.1:1234"
			rec = httptest.NewRecorder()
			wrapped.ServeHTTP(rec, req)
			Expect(rec.Code).To(Equal(http.StatusTooManyRequests))
			Expect(rec.Header().Get("Retry-After")).To(Equal("1"))

			// Advance clock by 1 more second (total 2s = 60s / N)
			currentTime = currentTime.Add(1 * time.Second)

			// Same IP receives normal response again
			req = httptest.NewRequest("GET", "/", nil)
			req.RemoteAddr = "10.0.0.1:1234"
			rec = httptest.NewRecorder()
			wrapped.ServeHTTP(rec, req)
			Expect(rec.Code).To(Equal(http.StatusOK))

			// Next immediate request is rate limited again
			req = httptest.NewRequest("GET", "/", nil)
			req.RemoteAddr = "10.0.0.1:1234"
			rec = httptest.NewRecorder()
			wrapped.ServeHTTP(rec, req)
			Expect(rec.Code).To(Equal(http.StatusTooManyRequests))
		})

		It("rounds Retry-After up to the next full second for non-integer wait times", func() {
			const n = 7 // refill rate: 7 / 60s
			limiter := newRateLimiter(n, nowFunc)
			wrapped := limiter.middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))

			// Exhaust all 7 tokens at t=0
			for i := 0; i < n; i++ {
				req := httptest.NewRequest("GET", "/", nil)
				req.RemoteAddr = "10.0.0.7:1234"
				rec := httptest.NewRecorder()
				wrapped.ServeHTTP(rec, req)
				Expect(rec.Code).To(Equal(http.StatusOK))
			}

			// 8th request is rate limited
			// missing = 1.0 token, refillRate = 7/60 tokens/s
			// wait time = 1.0 / (7/60) = 60/7 ≈ 8.5714s
			// Ceil must round up to 9s (Floor would give 8s)
			req := httptest.NewRequest("GET", "/", nil)
			req.RemoteAddr = "10.0.0.7:1234"
			rec := httptest.NewRecorder()
			wrapped.ServeHTTP(rec, req)
			Expect(rec.Code).To(Equal(http.StatusTooManyRequests))
			Expect(rec.Header().Get("Retry-After")).To(Equal("9"))

			// Advance clock by 8 seconds:
			// tokens refilled = 8 * (7/60) = 56/60 ≈ 0.9333 tokens
			// missing = 1.0 - 56/60 = 4/60 tokens
			// wait time = (4/60) / (7/60) = 4/7 ≈ 0.5714s (< 1s)
			// Ceil must round up to 1s (Floor would give 0s, violating >= 1)
			currentTime = currentTime.Add(8 * time.Second)

			req = httptest.NewRequest("GET", "/", nil)
			req.RemoteAddr = "10.0.0.7:1234"
			rec = httptest.NewRecorder()
			wrapped.ServeHTTP(rec, req)
			Expect(rec.Code).To(Equal(http.StatusTooManyRequests))
			Expect(rec.Header().Get("Retry-After")).To(Equal("1"))
		})

		It("caps accumulated tokens at maxTokens after prolonged idle periods", func() {
			const n = 10
			limiter := newRateLimiter(n, nowFunc)
			wrapped := limiter.middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))

			// Send 2 requests spaced by 1s to ensure an existing bucket with lastCheck is active
			for i := 0; i < 2; i++ {
				req := httptest.NewRequest("GET", "/", nil)
				req.RemoteAddr = "10.0.0.5:1234"
				rec := httptest.NewRecorder()
				wrapped.ServeHTTP(rec, req)
				Expect(rec.Code).To(Equal(http.StatusOK))
				currentTime = currentTime.Add(1 * time.Second)
			}

			// Advance clock by 10 minutes (600s), far exceeding the time to refill to maxTokens.
			// No requests from other IPs occur, so the bucket is not lazily evicted.
			currentTime = currentTime.Add(10 * time.Minute)

			// Fire 30 requests at the same instant from the same IP
			okCount, limitedCount := 0, 0
			for i := 0; i < 30; i++ {
				req := httptest.NewRequest("GET", "/", nil)
				req.RemoteAddr = "10.0.0.5:1234"
				rec := httptest.NewRecorder()
				wrapped.ServeHTTP(rec, req)
				switch rec.Code {
				case http.StatusOK:
					okCount++
				case http.StatusTooManyRequests:
					limitedCount++
				}
			}

			// With token cap at maxTokens, exactly N requests succeed; excess requests must be rejected.
			Expect(okCount).To(Equal(n))
			Expect(limitedCount).To(Equal(20))
		})

		It("does not affect another IP when one IP is exhausted", func() {
			const n = 2
			limiter := newRateLimiter(n, nowFunc)
			wrapped := limiter.middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))

			// Exhaust IP A
			for i := 0; i < n; i++ {
				req := httptest.NewRequest("GET", "/", nil)
				req.RemoteAddr = "192.168.1.1:1000"
				rec := httptest.NewRecorder()
				wrapped.ServeHTTP(rec, req)
				Expect(rec.Code).To(Equal(http.StatusOK))
			}
			reqA := httptest.NewRequest("GET", "/", nil)
			reqA.RemoteAddr = "192.168.1.1:1000"
			recA := httptest.NewRecorder()
			wrapped.ServeHTTP(recA, reqA)
			Expect(recA.Code).To(Equal(http.StatusTooManyRequests))

			// IP B is still allowed
			reqB := httptest.NewRequest("GET", "/", nil)
			reqB.RemoteAddr = "192.168.1.2:1000"
			recB := httptest.NewRecorder()
			wrapped.ServeHTTP(recB, reqB)
			Expect(recB.Code).To(Equal(http.StatusOK))
		})

		It("never rate limits when RATE_LIMIT is disabled (0)", func() {
			srv, err := NewServer(dummyTipChain{}, Config{RateLimit: 0})
			Expect(err).NotTo(HaveOccurred())
			Expect(srv.limiter).To(BeNil())

			sActive, err := NewServer(dummyTipChain{}, Config{RateLimit: 10})
			Expect(err).NotTo(HaveOccurred())
			Expect(sActive.limiter).NotTo(BeNil())

			handler := srv.Handler()
			for i := 0; i < 150; i++ {
				req := httptest.NewRequest("GET", "/health", nil)
				req.RemoteAddr = "127.0.0.1:5000"
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, req)
				Expect(rec.Code).To(Equal(http.StatusOK))
			}
		})

		It("ignores X-Forwarded-For and relies only on RemoteAddr", func() {
			const n = 1
			limiter := newRateLimiter(n, nowFunc)
			wrapped := limiter.middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))

			req1 := httptest.NewRequest("GET", "/", nil)
			req1.RemoteAddr = "1.1.1.1:1234"
			req1.Header.Set("X-Forwarded-For", "2.2.2.2")
			rec1 := httptest.NewRecorder()
			wrapped.ServeHTTP(rec1, req1)
			Expect(rec1.Code).To(Equal(http.StatusOK))

			// Same RemoteAddr but different X-Forwarded-For must be blocked
			req2 := httptest.NewRequest("GET", "/", nil)
			req2.RemoteAddr = "1.1.1.1:1234"
			req2.Header.Set("X-Forwarded-For", "3.3.3.3")
			rec2 := httptest.NewRecorder()
			wrapped.ServeHTTP(rec2, req2)
			Expect(rec2.Code).To(Equal(http.StatusTooManyRequests))
		})

		It("handles RemoteAddr without a port correctly", func() {
			const n = 1
			limiter := newRateLimiter(n, nowFunc)
			wrapped := limiter.middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))

			req1 := httptest.NewRequest("GET", "/", nil)
			req1.RemoteAddr = "10.20.30.40"
			rec1 := httptest.NewRecorder()
			wrapped.ServeHTTP(rec1, req1)
			Expect(rec1.Code).To(Equal(http.StatusOK))

			req2 := httptest.NewRequest("GET", "/", nil)
			req2.RemoteAddr = "10.20.30.40"
			rec2 := httptest.NewRecorder()
			wrapped.ServeHTTP(rec2, req2)
			Expect(rec2.Code).To(Equal(http.StatusTooManyRequests))
		})

		It("handles 50 concurrent requests from same IP with N=10 and frozen clock", func() {
			const n = 10
			const concurrent = 50
			limiter := newRateLimiter(n, nowFunc)
			wrapped := limiter.middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))

			codes := make(chan int, concurrent)
			var wg sync.WaitGroup
			for i := 0; i < concurrent; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					req := httptest.NewRequest("GET", "/", nil)
					req.RemoteAddr = "172.16.0.1:4000"
					rec := httptest.NewRecorder()
					wrapped.ServeHTTP(rec, req)
					codes <- rec.Code
				}()
			}
			wg.Wait()
			close(codes)

			okCount, limitedCount := 0, 0
			for code := range codes {
				switch code {
				case http.StatusOK:
					okCount++
				case http.StatusTooManyRequests:
					limitedCount++
				}
			}

			Expect(okCount).To(Equal(n))
			Expect(limitedCount).To(Equal(concurrent - n))
		})

		It("respects invariant: non-429 responses <= burst + floor(T * N / 60s)", func() {
			const n = 12 // 12 req/min => 1 token every 5 seconds
			limiter := newRateLimiter(n, nowFunc)
			wrapped := limiter.middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))

			allowedTotal := 0
			// Advance in small steps over 60 seconds
			for step := 0; step < 60; step++ {
				for r := 0; r < 2; r++ {
					req := httptest.NewRequest("GET", "/", nil)
					req.RemoteAddr = "192.168.50.1:1234"
					rec := httptest.NewRecorder()
					wrapped.ServeHTTP(rec, req)
					if rec.Code == http.StatusOK {
						allowedTotal++
					}
				}
				currentTime = currentTime.Add(1 * time.Second)
			}

			// In duration T = 60s: max allowed is burst (12) + floor(60 * 12 / 60) = 12 + 12 = 24
			Expect(allowedTotal).To(BeNumerically("<=", 24))
			Expect(allowedTotal).To(BeNumerically(">=", 23)) // allow slight discrete timing difference
		})

		It("lazily evicts idle buckets that are full, but keeps active buckets", func() {
			const n = 10
			limiter := newRateLimiter(n, nowFunc)
			wrapped := limiter.middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))

			// Request from IP A
			reqA := httptest.NewRequest("GET", "/", nil)
			reqA.RemoteAddr = "1.2.3.4:1234"
			recA := httptest.NewRecorder()
			wrapped.ServeHTTP(recA, reqA)

			limiter.mu.Lock()
			Expect(limiter.buckets).To(HaveKey("1.2.3.4"))
			limiter.mu.Unlock()

			// Advance clock by 30s (not enough to fully refill, bucket has 9 tokens + 5 = 14 > 10? wait, 10 tokens max:
			// Burst 10, consumed 1 => 9 tokens left. 10 req/min = 1 token per 6s.
			// After 1 second: 9 + 1/6 tokens < 10 tokens. IP A is NOT yet full!
			currentTime = currentTime.Add(1 * time.Second)

			// Request from IP B
			reqB := httptest.NewRequest("GET", "/", nil)
			reqB.RemoteAddr = "5.6.7.8:1234"
			recB := httptest.NewRecorder()
			wrapped.ServeHTTP(recB, reqB)

			limiter.mu.Lock()
			// IP A is not full yet, so it must still exist
			Expect(limiter.buckets).To(HaveKey("1.2.3.4"))
			Expect(limiter.buckets).To(HaveKey("5.6.7.8"))
			limiter.mu.Unlock()

			// Now advance clock by 60s: IP A is definitely full now!
			currentTime = currentTime.Add(60 * time.Second)

			// Request from IP C: triggers lazy eviction under mutex
			reqC := httptest.NewRequest("GET", "/", nil)
			reqC.RemoteAddr = "9.9.9.9:1234"
			recC := httptest.NewRecorder()
			wrapped.ServeHTTP(recC, reqC)

			limiter.mu.Lock()
			// IP A was idle long enough to be full, so it must be evicted
			Expect(limiter.buckets).NotTo(HaveKey("1.2.3.4"))
			// IP B was also idle for 60s, so it must be evicted
			Expect(limiter.buckets).NotTo(HaveKey("5.6.7.8"))
			// IP C is recent
			Expect(limiter.buckets).To(HaveKey("9.9.9.9"))
			limiter.mu.Unlock()
		})

		It("evicts a bucket at the exact boundary when refilled tokens reach max tokens", func() {
			const n = 60 // refill rate: 1 token/sec
			limiter := newRateLimiter(n, nowFunc)
			wrapped := limiter.middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))

			// Request 1 from IP X: consumes 1 token, remaining 59 tokens
			reqX := httptest.NewRequest("GET", "/", nil)
			reqX.RemoteAddr = "11.22.33.44:1234"
			recX := httptest.NewRecorder()
			wrapped.ServeHTTP(recX, reqX)

			limiter.mu.Lock()
			Expect(limiter.buckets).To(HaveKey("11.22.33.44"))
			limiter.mu.Unlock()

			// Advance by exactly 1 second: 59 + 1.0 * 1.0 = 60.0 (maxTokens)
			currentTime = currentTime.Add(1 * time.Second)

			// Request from IP Y triggers sweep
			reqY := httptest.NewRequest("GET", "/", nil)
			reqY.RemoteAddr = "55.66.77.88:1234"
			recY := httptest.NewRecorder()
			wrapped.ServeHTTP(recY, reqY)

			limiter.mu.Lock()
			// IP X refilled to exactly 60.0 >= 60.0, so it must be evicted
			Expect(limiter.buckets).NotTo(HaveKey("11.22.33.44"))
			Expect(limiter.buckets).To(HaveKey("55.66.77.88"))
			limiter.mu.Unlock()
		})
	})
})
