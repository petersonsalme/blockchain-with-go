package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"
)

const (
	// eventBufferSize is how many events one subscriber may leave unread before it is dropped.
	eventBufferSize = 16
	// eventWriteTimeout renews the write deadline before each write to an event stream, so the
	// http.Server WriteTimeout does not end a long-lived stream.
	eventWriteTimeout = 10 * time.Second
)

// Event names of GET /events.
const (
	eventBlock = "block"
	eventTx    = "tx"
)

// event is one server-sent event; data is compact JSON on a single line.
type event struct {
	name string
	data []byte
}

// hub fans events out to the subscribers of GET /events. publish never blocks.
type hub struct {
	mu   sync.Mutex
	subs map[chan event]struct{}
}

func newHub() *hub { return &hub{subs: make(map[chan event]struct{})} }

// subscribe registers a subscriber with a buffer of eventBufferSize.
func (h *hub) subscribe() chan event {
	ch := make(chan event, eventBufferSize)

	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()

	return ch
}

// unsubscribe removes ch and closes it; it is a no-op when publish already dropped ch.
func (h *hub) unsubscribe(ch chan event) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if _, ok := h.subs[ch]; ok {
		delete(h.subs, ch)
		close(ch)
	}
}

// count is the number of active subscribers.
func (h *hub) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()

	return len(h.subs)
}

// publish sends v as an event called name to every subscriber without blocking.
// A subscriber whose buffer is full is removed and its channel closed.
func (h *hub) publish(name string, v any) {
	data, err := json.Marshal(v)
	if err != nil { // domain types always marshal; nothing useful to send otherwise
		return
	}

	ev := event{name: name, data: data}

	h.mu.Lock()
	defer h.mu.Unlock()

	for ch := range h.subs {
		select {
		case ch <- ev:
		default:
			delete(h.subs, ch)
			close(ch)
		}
	}
}

// handleEvents streams "block" and "tx" events as text/event-stream until the client
// disconnects, falls behind, a write fails or the request context ends.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	header := w.Header()
	header.Set("Content-Type", "text/event-stream")
	header.Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)

	if r.Method == http.MethodHead {
		return
	}

	// Subscribe before flushing the headers: a client that has them is already registered.
	ch := s.events.subscribe()
	defer s.events.unsubscribe(ch)

	if flushEvent(rc, nil) != nil {
		return
	}

	for {
		select {
		case <-r.Context().Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}

			err := flushEvent(rc, func() error {
				_, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.name, ev.data)
				return err
			})
			if err != nil {
				return
			}
		}
	}
}

// flushEvent renews the write deadline, runs write (if any) and flushes.
// Writers without deadline support, such as httptest.ResponseRecorder, are served without one.
func flushEvent(rc *http.ResponseController, write func() error) error {
	if err := rc.SetWriteDeadline(time.Now().Add(eventWriteTimeout)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return err
	}

	if write != nil {
		if err := write(); err != nil {
			return err
		}
	}

	return rc.Flush()
}
