package dashboard_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/petersonsalme/blockchain-with-go/internal/dashboard"
)

const wantCSP = "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self'; base-uri 'none'; frame-ancestors 'none'"

func newTestServer(t *testing.T, nodes ...*countingNode) http.Handler {
	t.Helper()

	var list []dashboard.Node

	for i, n := range nodes {
		node := n.node(t)
		node.Name = "node" + string(rune('1'+i))
		list = append(list, node)
	}

	fetch := dashboard.NewFetcher(time.Second, 1<<10)

	h, err := dashboard.NewServer(list, fetch, dashboard.NewPoller(list, fetch, time.Hour, time.Now))
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	return h
}

// newTestServerWithPoller serves the nodes of p, whose names are node1, node2, ...
func newTestServerWithPoller(t *testing.T, p *dashboard.Poller, nodes ...*chainNode) http.Handler {
	t.Helper()

	var list []dashboard.Node

	for i, n := range nodes {
		node := n.node(t)
		node.Name = fmt.Sprintf("node%d", i+1)
		list = append(list, node)
	}

	h, err := dashboard.NewServer(list, dashboard.NewFetcher(time.Second, 1<<10), p)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	return h
}

func do(h http.Handler, method, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), method, path, nil))

	return rec
}

func TestPageShell(t *testing.T) {
	t.Parallel()

	n1, n2 := okNode(t), okNode(t)
	rec := do(newTestServer(t, n1, n2), http.MethodGet, "/")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}

	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q", ct)
	}

	body := rec.Body.String()
	for _, want := range []string{
		`<script src="/static/htmx.min.js"></script>`,
		`<link rel="stylesheet" href="/static/app.css">`,
		`<meta name="htmx-config" content='{"allowEval":false,"allowScriptTags":false,"includeIndicatorStyles":false}'>`,
		`href="/n/node1/"`,
		`href="/n/node2/"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %s", want)
		}
	}

	if n1.hits.Load()+n2.hits.Load() != 0 {
		t.Error("the page shell made outbound requests")
	}
}

func TestPageEscapesNodeNames(t *testing.T) {
	t.Parallel()

	n := okNode(t)
	node := n.node(t)
	node.Name = `<b>"x"`

	fetch := dashboard.NewFetcher(time.Second, 1<<10)

	h, err := dashboard.NewServer([]dashboard.Node{node}, fetch, dashboard.NewPoller([]dashboard.Node{node}, fetch, time.Hour, time.Now))
	if err != nil {
		t.Fatal(err)
	}

	if body := do(h, http.MethodGet, "/").Body.String(); strings.Contains(body, "<b>") {
		t.Error("node name is not escaped")
	}
}

func TestSecurityHeadersOnEveryResponse(t *testing.T) {
	t.Parallel()

	h := newTestServer(t, okNode(t))
	tests := []struct {
		method, path string
		status       int
	}{
		{http.MethodGet, "/", 200},
		{http.MethodHead, "/", 200},
		{http.MethodGet, "/static/htmx.min.js", 200},
		{http.MethodGet, "/static/app.css", 200},
		{http.MethodGet, "/static/missing.js", 404},
		{http.MethodGet, "/static/", 404},
		{http.MethodGet, "/nope", 404},
		{http.MethodPost, "/", 405},
		{http.MethodPost, "/static/app.css", 405},
		{http.MethodGet, "/n/node1/", 200},
		{http.MethodGet, "/n/other/", 404},
		{http.MethodGet, "/n/node1", 307},
	}

	for _, tc := range tests {
		rec := do(h, tc.method, tc.path)
		if rec.Code != tc.status {
			t.Errorf("%s %s = %d, want %d", tc.method, tc.path, rec.Code, tc.status)
		}

		if got := rec.Header().Get("Content-Security-Policy"); got != wantCSP {
			t.Errorf("%s %s CSP = %q", tc.method, tc.path, got)
		}

		if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("%s %s nosniff = %q", tc.method, tc.path, got)
		}
	}
}

func TestStaticFiles(t *testing.T) {
	t.Parallel()

	h := newTestServer(t, okNode(t))

	js := do(h, http.MethodGet, "/static/htmx.min.js")
	if !strings.Contains(js.Header().Get("Content-Type"), "javascript") || js.Body.Len() < 10_000 {
		t.Errorf("htmx: type %q, %d bytes", js.Header().Get("Content-Type"), js.Body.Len())
	}

	css := do(h, http.MethodGet, "/static/app.css")
	if !strings.HasPrefix(css.Header().Get("Content-Type"), "text/css") || !strings.Contains(css.Body.String(), "prefers-color-scheme") {
		t.Errorf("css: type %q", css.Header().Get("Content-Type"))
	}
}

func TestMethodsOtherThanGetAndHeadAreRejected(t *testing.T) {
	t.Parallel()

	h := newTestServer(t, okNode(t))

	for _, path := range []string{"/", "/static/app.css", "/n/node1/", "/n/other/"} {
		for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions} {
			rec := do(h, method, path)
			if rec.Code != http.StatusMethodNotAllowed {
				t.Errorf("%s %s = %d, want 405", method, path, rec.Code)
			}

			allow := rec.Header().Get("Allow")
			if !strings.Contains(allow, "GET") || !strings.Contains(allow, "HEAD") || strings.Contains(allow, "POST") {
				t.Errorf("%s %s Allow = %q", method, path, allow)
			}
		}
	}
}

func TestUnknownNodeIs404WithoutOutboundRequest(t *testing.T) {
	t.Parallel()

	n := okNode(t)
	h := newTestServer(t, n)

	for _, path := range []string{"/n/ghost/", "/n/ghost/health", "/n/NODE1/", "/n/node1/health"} {
		if rec := do(h, http.MethodGet, path); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, rec.Code)
		}
	}

	if n.hits.Load() != 0 {
		t.Errorf("node got %d requests, want 0", n.hits.Load())
	}
}

func TestHostHeaderNeverReachesNodes(t *testing.T) {
	t.Parallel()

	n := okNode(t)
	h := newTestServer(t, n)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "http://evil.example:9/n/node1/", nil)
	req.Host = "evil.example:9"
	req.Header.Set("X-Forwarded-Host", "evil.example")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if n.hits.Load() != 0 {
		t.Error("shell made an outbound request")
	}

	if body, _ := io.ReadAll(rec.Body); strings.Contains(string(body), "evil.example") {
		t.Error("page echoes the request host")
	}
}
