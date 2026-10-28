package dashboard

import (
	"bytes"
	"embed"
	"html/template"
	"io/fs"
	"net/http"
	"strings"
)

// dashboardCSP is the same policy as explorerCSP in internal/server/explorer.go (not imported:
// this package stays stdlib only). It forbids everything but same-origin files.
const dashboardCSP = "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self'; base-uri 'none'; frame-ancestors 'none'"

//go:embed templates/*.html
var templateFiles embed.FS

//go:embed static
var staticFiles embed.FS

type server struct {
	nodes []Node
	known map[string]bool
	fetch *Fetcher
	poll  *Poller
	tmpl  *template.Template
}

// NewServer returns the dashboard handler: a page shell and static files, serving only GET and
// HEAD. Node reads go through fetch, to the nodes listed here and nowhere else; the Network screen
// shows what poll last learned and never reaches a node itself.
func NewServer(nodes []Node, fetch *Fetcher, poll *Poller) (http.Handler, error) {
	tmpl, err := template.ParseFS(templateFiles, "templates/*.html")
	if err != nil {
		return nil, err
	}

	static, err := fs.Sub(staticFiles, "static")
	if err != nil {
		return nil, err
	}

	s := &server{nodes: nodes, known: map[string]bool{}, fetch: fetch, poll: poll, tmpl: tmpl}
	for _, n := range nodes {
		s.known[n.Name] = true
	}

	files := http.StripPrefix("/static/", http.FileServerFS(static))

	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.page)
	mux.HandleFunc("GET /fragments/network", s.networkFragment)
	mux.HandleFunc("GET /n/{node}/{$}", s.nodePage)
	mux.HandleFunc("GET /static/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/") { // no directory listing
			http.NotFound(w, r)

			return
		}

		files.ServeHTTP(w, r)
	})

	return secure(mux), nil
}

// secure sets the CSP and nosniff headers before anything else can answer, errors included.
func secure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", dashboardCSP)
		h.Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}

func (s *server) page(w http.ResponseWriter, _ *http.Request) {
	view := s.poll.View()
	s.render(w, "", &view)
}

func (s *server) networkFragment(w http.ResponseWriter, _ *http.Request) {
	var buf bytes.Buffer

	err := s.tmpl.ExecuteTemplate(&buf, "network", s.poll.View())
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(buf.Bytes())
}

// nodePage answers only for a configured node: the name comes from the config, never the URL.
func (s *server) nodePage(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("node")
	if !s.known[name] {
		http.NotFound(w, r)

		return
	}

	s.render(w, name, nil)
}

func (s *server) render(w http.ResponseWriter, current string, network *View) {
	var buf bytes.Buffer

	err := s.tmpl.ExecuteTemplate(&buf, "index.html", struct {
		Nodes   []Node
		Current string
		Network *View
	}{s.nodes, current, network})
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(buf.Bytes())
}
