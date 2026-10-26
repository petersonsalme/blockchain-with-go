package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/petersonsalme/blockchain-with-go/internal/chain"
	"github.com/petersonsalme/blockchain-with-go/internal/consensus"
	"github.com/petersonsalme/blockchain-with-go/internal/consensus/pow"
	"github.com/petersonsalme/blockchain-with-go/internal/domain"
	"github.com/petersonsalme/blockchain-with-go/internal/storage/memory"
)

// sseEvent is one parsed server-sent event.
type sseEvent struct{ name, data string }

// sseStream reads the events of an open GET /events response.
type sseStream struct {
	resp   *http.Response
	events chan sseEvent
	// raw collects every line read, to check the framing.
	mu  sync.Mutex
	raw []string
}

func openStream(ctx context.Context, url string) (*sseStream, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url+"/events", nil)
	if err != nil {
		return nil, err
	}

	resp, err := http.DefaultClient.Do(req) //nolint:bodyclose // the body is closed by sseStream.close
	if err != nil {
		return nil, err
	}

	st := &sseStream{resp: resp, events: make(chan sseEvent, 64)}
	go func() {
		defer close(st.events)

		sc := bufio.NewScanner(resp.Body)

		var cur sseEvent

		for sc.Scan() {
			line := sc.Text()

			st.mu.Lock()
			st.raw = append(st.raw, line)
			st.mu.Unlock()

			switch {
			case strings.HasPrefix(line, "event: "):
				cur.name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				cur.data = strings.TrimPrefix(line, "data: ")
			case line == "":
				st.events <- cur

				cur = sseEvent{}
			}
		}
	}()

	return st, nil
}

func (s *sseStream) close() { _ = s.resp.Body.Close() }

// next waits up to d for one event; ok is false on timeout or end of stream.
func (s *sseStream) next(d time.Duration) (sseEvent, bool) {
	select {
	case ev, ok := <-s.events:
		return ev, ok
	case <-time.After(d):
		return sseEvent{}, false
	}
}

const eventWait = 5 * time.Second

func newEventsServer(t testing.TB) *Server {
	t.Helper()

	srv, err := NewServer(brokenChain{}, Config{})
	if err != nil {
		t.Fatal(err)
	}

	return srv
}

func waitSubscribers(t testing.TB, h *hub, want int) {
	t.Helper()

	deadline := time.Now().Add(eventWait)
	for h.count() != want {
		if time.Now().After(deadline) {
			t.Fatalf("subscribers = %d, want %d", h.count(), want)
		}

		time.Sleep(5 * time.Millisecond)
	}
}

func TestHubDropsSlowSubscriberWithoutBlocking(t *testing.T) {
	h := newHub()
	slow := h.subscribe()
	fast := h.subscribe()

	got := make(chan int, 1)

	go func() {
		n := 0
		for range fast {
			n++
			if n == eventBufferSize+1 {
				got <- n
			}
		}
	}()

	for i := range eventBufferSize {
		h.publish(eventBlock, i)
	}

	if h.count() != 2 {
		t.Fatalf("subscribers = %d after filling the buffer, want 2", h.count())
	}

	if len(slow) != eventBufferSize {
		t.Fatalf("slow buffer = %d, want %d", len(slow), eventBufferSize)
	}

	for len(fast) > 0 { // let the reader catch up so only the slow subscriber overflows
		time.Sleep(time.Millisecond)
	}

	done := make(chan struct{})

	go func() { h.publish(eventTx, "last"); close(done) }()

	select {
	case <-done:
	case <-time.After(eventWait):
		t.Fatal("publish blocked on a full subscriber")
	}

	if h.count() != 1 {
		t.Fatalf("subscribers = %d after overflow, want 1", h.count())
	}

	// The slow channel is closed: its buffered events drain, then it reports closed.
	for range eventBufferSize {
		if _, ok := <-slow; !ok {
			t.Fatal("slow channel closed before its buffered events")
		}
	}

	if _, ok := <-slow; ok {
		t.Fatal("slow channel still open after being dropped")
	}

	select {
	case n := <-got:
		if n != eventBufferSize+1 {
			t.Fatalf("fast subscriber got %d events, want %d", n, eventBufferSize+1)
		}
	case <-time.After(eventWait):
		t.Fatal("fast subscriber did not receive every event")
	}

	h.unsubscribe(fast)
}

func TestHubUnsubscribeIsIdempotent(t *testing.T) {
	h := newHub()
	ch := h.subscribe()
	h.unsubscribe(ch)
	h.unsubscribe(ch) // must not panic on a double close

	if h.count() != 0 {
		t.Fatalf("subscribers = %d, want 0", h.count())
	}

	h.publish(eventBlock, 1) // no subscribers: nothing to do
}

func TestHubPublishSkipsUnmarshalableValues(t *testing.T) {
	h := newHub()
	ch := h.subscribe()
	h.publish(eventBlock, make(chan int))

	if len(ch) != 0 {
		t.Fatalf("an unmarshalable value was published: %d events", len(ch))
	}

	h.publish(eventBlock, map[string]int{"a": 1})

	ev := <-ch
	if ev.name != eventBlock || string(ev.data) != `{"a":1}` {
		t.Fatalf("event = %+v", ev)
	}
}

// spyWriter is a ResponseWriter whose write, flush and deadline behaviour tests control.
type spyWriter struct {
	*httptest.ResponseRecorder

	mu          sync.Mutex
	deadlines   []time.Time
	flushes     int
	writeErr    error
	flushErr    error
	deadlineErr error
}

func (w *spyWriter) Write(b []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.writeErr != nil {
		return 0, w.writeErr
	}

	return w.ResponseRecorder.Write(b)
}

func (w *spyWriter) FlushError() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.flushes++

	return w.flushErr
}

func (w *spyWriter) SetWriteDeadline(t time.Time) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.deadlines = append(w.deadlines, t)

	return w.deadlineErr
}

func (w *spyWriter) snapshot() (deadlines []time.Time, flushes int, body string) {
	w.mu.Lock()
	defer w.mu.Unlock()

	return append([]time.Time(nil), w.deadlines...), w.flushes, w.Body.String()
}

// serveEvents runs handleEvents on w in the background and returns a channel closed when it returns.
func serveEvents(srv *Server, w http.ResponseWriter, ctx context.Context) <-chan struct{} {
	done := make(chan struct{})
	req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/events", nil)

	go func() { defer close(done); srv.handleEvents(w, req) }()

	return done
}

func waitDone(t *testing.T, done <-chan struct{}, what string) {
	t.Helper()

	select {
	case <-done:
	case <-time.After(eventWait):
		t.Fatalf("handler did not return: %s", what)
	}
}

func TestHandleEventsRenewsWriteDeadlineBeforeEveryWrite(t *testing.T) {
	srv := newEventsServer(t)
	w := &spyWriter{ResponseRecorder: httptest.NewRecorder()}
	ctx, cancel := context.WithCancel(context.Background())
	done := serveEvents(srv, w, ctx)
	waitSubscribers(t, srv.events, 1)

	srv.events.publish(eventBlock, 1)
	srv.events.publish(eventTx, 2)

	deadline := time.Now().Add(eventWait)

	for {
		d, _, _ := w.snapshot()
		if len(d) == 3 { // headers flush + two events
			break
		}

		if time.Now().After(deadline) {
			t.Fatalf("deadlines set = %d, want 3", len(d))
		}

		time.Sleep(5 * time.Millisecond)
	}

	cancel()
	waitDone(t, done, "context cancelled")

	deadlines, flushes, body := w.snapshot()
	for i, d := range deadlines {
		if remaining := time.Until(d); remaining < eventWriteTimeout-2*time.Second || remaining > eventWriteTimeout {
			t.Errorf("deadline %d is %v away, want about %v", i, remaining, eventWriteTimeout)
		}
	}

	if flushes != 3 {
		t.Errorf("flushes = %d, want 3", flushes)
	}

	if want := "event: block\ndata: 1\n\nevent: tx\ndata: 2\n\n"; body != want {
		t.Errorf("body = %q, want %q", body, want)
	}

	if got := w.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Errorf("Content-Type = %q", got)
	}

	if got := w.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control = %q", got)
	}

	waitSubscribers(t, srv.events, 0)
}

func TestHandleEventsEndsWhenItsChannelIsClosed(t *testing.T) {
	srv := newEventsServer(t)
	w := &spyWriter{ResponseRecorder: httptest.NewRecorder()}
	done := serveEvents(srv, w, context.Background())
	waitSubscribers(t, srv.events, 1)

	srv.events.mu.Lock()

	var ch chan event
	for c := range srv.events.subs {
		ch = c
	}
	srv.events.mu.Unlock()
	srv.events.unsubscribe(ch)

	waitDone(t, done, "channel closed")
	waitSubscribers(t, srv.events, 0)
}

func TestHandleEventsEndsOnWriteFailures(t *testing.T) {
	boom := errors.New("boom")

	cases := []struct {
		name string
		w    *spyWriter
		// startsFailing means the failure hits before any event is published.
		startsFailing bool
	}{
		{"write error", &spyWriter{ResponseRecorder: httptest.NewRecorder(), writeErr: boom}, false},
		{"flush error", &spyWriter{ResponseRecorder: httptest.NewRecorder(), flushErr: boom}, true},
		{"deadline error", &spyWriter{ResponseRecorder: httptest.NewRecorder(), deadlineErr: boom}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := newEventsServer(t)

			done := serveEvents(srv, c.w, context.Background())
			if !c.startsFailing {
				waitSubscribers(t, srv.events, 1)
				srv.events.publish(eventBlock, 1)
			}

			waitDone(t, done, c.name)
			waitSubscribers(t, srv.events, 0)
		})
	}
}

func TestEventStreamOutlivesServerWriteTimeout(t *testing.T) {
	srv := newEventsServer(t)
	ts := httptest.NewUnstartedServer(srv.Handler())
	ts.Config.WriteTimeout = 200 * time.Millisecond

	ts.Start()
	defer ts.Close()

	st, err := openStream(context.Background(), ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer st.close()

	waitSubscribers(t, srv.events, 1)

	time.Sleep(500 * time.Millisecond)
	srv.events.publish(eventBlock, map[string]int{"n": 1})

	ev, ok := st.next(eventWait)
	if !ok || ev.name != eventBlock || ev.data != `{"n":1}` {
		t.Fatalf("event after the server WriteTimeout = %+v, ok %v", ev, ok)
	}
}

func TestShutdownEndsEventStreams(t *testing.T) {
	srv := newEventsServer(t)

	l, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	nodeCtx, cancelNode := context.WithCancel(context.Background())
	defer cancelNode()

	hs := srv.HTTPServer(nodeCtx, l.Addr().String())

	serveErr := make(chan error, 1)
	go func() { serveErr <- hs.Serve(l) }()

	st, err := openStream(context.Background(), "http://"+l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer st.close()

	waitSubscribers(t, srv.events, 1)

	cancelNode()

	if ev, ok := st.next(eventWait); ok {
		t.Fatalf("unexpected event %+v; want the stream to end", ev)
	}

	if _, err := io.ReadAll(st.resp.Body); err != nil {
		t.Fatalf("stream did not end with a clean EOF: %v", err)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := hs.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown = %v, want nil", err)
	}

	if err := <-serveErr; !errors.Is(err, http.ErrServerClosed) {
		t.Fatalf("Serve = %v, want ErrServerClosed", err)
	}

	waitSubscribers(t, srv.events, 0)
}

var _ = Describe("GET /events", func() {
	var (
		funds *bank
		srv   *Server
		ts    *httptest.Server
	)

	build := func(opts ...chain.Option) {
		svc, err := funds.service(memory.New(), pow.NewProofOfWork(1), opts...)
		Expect(err).NotTo(HaveOccurred())
		srv, err = NewServer(svc, Config{})
		Expect(err).NotTo(HaveOccurred())

		ts = httptest.NewServer(srv.Handler())
		DeferCleanup(ts.Close)
	}
	connect := func() *sseStream {
		st, err := openStream(context.Background(), ts.URL)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(st.close)
		Expect(st.resp.StatusCode).To(Equal(http.StatusOK))
		Expect(st.resp.Header.Get("Content-Type")).To(Equal("text/event-stream"))
		Expect(st.resp.Header.Get("Cache-Control")).To(Equal("no-cache"))

		return st
	}
	post := func(path, body string) (int, []byte) {
		resp, err := postURL(ts.URL+path, "application/json", strings.NewReader(body))
		Expect(err).NotTo(HaveOccurred())

		defer func() { _ = resp.Body.Close() }()

		b, err := io.ReadAll(resp.Body)
		Expect(err).NotTo(HaveOccurred())

		return resp.StatusCode, b
	}
	expectBlockEvent := func(st *sseStream, body []byte) {
		ev, ok := st.next(eventWait)
		Expect(ok).To(BeTrue(), "no event received")
		Expect(ev.name).To(Equal("block"))

		var got, want domain.Block
		Expect(json.Unmarshal([]byte(ev.data), &got)).To(Succeed())
		Expect(json.Unmarshal(body, &want)).To(Succeed())
		Expect(got.Hash).NotTo(BeEmpty())
		Expect(got.Hash).To(Equal(want.Hash))
		Expect(ev.data).NotTo(ContainSubstring("\n"))
	}

	BeforeEach(func() {
		funds = newBank()

		build()
	})

	It("flushes the headers before any event", func() {
		connect() // returns only once the headers arrived; no event was published
		Expect(srv.events.count()).To(Equal(1))
	})

	It("publishes a block event for POST /mine and POST /", func() {
		st := connect()
		code, _ := post("/tx", txBody(funds.tx(newWallet().Address(), 42)))
		Expect(code).To(Equal(http.StatusAccepted))

		ev, ok := st.next(eventWait)
		Expect(ok).To(BeTrue())
		Expect(ev.name).To(Equal("tx"))

		code, body := post("/mine", "")
		Expect(code).To(Equal(http.StatusCreated))
		expectBlockEvent(st, body)

		code, body = post("/", funds.payload())
		Expect(code).To(Equal(http.StatusCreated))
		expectBlockEvent(st, body)
	})

	It("publishes a tx event with the admitted transaction and nothing for rejected ones", func() {
		st := connect()
		tx := funds.tx(newWallet().Address(), 42)
		code, _ := post("/tx", txBody(tx))
		Expect(code).To(Equal(http.StatusAccepted))

		ev, ok := st.next(eventWait)
		Expect(ok).To(BeTrue())
		Expect(ev.name).To(Equal("tx"))

		var got domain.Transaction
		Expect(json.Unmarshal([]byte(ev.data), &got)).To(Succeed())
		Expect(got.ID).To(Equal(tx.ID))
		Expect(ev.data).NotTo(ContainSubstring("\n"))

		code, _ = post("/tx", txBody(tx)) // duplicate: 409
		Expect(code).To(Equal(http.StatusConflict))
		code, _ = post("/tx", "{}") // invalid: 400
		Expect(code).To(Equal(http.StatusBadRequest))

		_, ok = st.next(150 * time.Millisecond)
		Expect(ok).To(BeFalse(), "a rejected submission published an event")
	})

	It("publishes nothing when mining fails", func() {
		build(chain.WithGenerator(func(context.Context, consensus.Consensus, []*domain.Block, []domain.Transaction) (*domain.Block, error) {
			return nil, errors.New("boom")
		}))

		st := connect()
		code, _ := post("/mine", "")
		Expect(code).To(Equal(http.StatusInternalServerError))

		_, ok := st.next(150 * time.Millisecond)
		Expect(ok).To(BeFalse(), "a failed mining published an event")
	})

	It("fans out to every subscriber and removes the ones that disconnect", func() {
		a, b := connect(), connect()

		Eventually(srv.events.count).Should(Equal(2))

		code, body := post("/mine", "")
		Expect(code).To(Equal(http.StatusCreated))
		expectBlockEvent(a, body)
		expectBlockEvent(b, body)

		a.close()
		Eventually(srv.events.count).Should(Equal(1))
		b.close()
		Eventually(srv.events.count).Should(Equal(0))
	})

	It("answers HEAD with the headers and ends", func() {
		resp, err := http.Head(ts.URL + "/events")
		Expect(err).NotTo(HaveOccurred())

		defer func() { _ = resp.Body.Close() }()

		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		Expect(resp.Header.Get("Content-Type")).To(Equal("text/event-stream"))
		Expect(srv.events.count()).To(Equal(0))
	})

	It("answers other methods with 405 and Allow", func() {
		resp, err := postURL(ts.URL+"/events", "application/json", nil)
		Expect(err).NotTo(HaveOccurred())

		defer func() { _ = resp.Body.Close() }()

		Expect(resp.StatusCode).To(Equal(http.StatusMethodNotAllowed))
		Expect(resp.Header.Get("Allow")).To(Equal("GET, HEAD"))
	})
})

func postURL(url, contentType string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, body)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", contentType)

	return http.DefaultClient.Do(req)
}
