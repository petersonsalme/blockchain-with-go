package dashboard

import (
	"bytes"
	"embed"
	"html/template"
	"io/fs"
	"net/http"
	"net/url"
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
	tmpl, err := template.New("").Funcs(template.FuncMap{"pe": url.PathEscape, "qe": url.QueryEscape, "hstart": hashStart, "hend": hashEnd, "coins": coins, "units": baseUnits}).ParseFS(templateFiles, "templates/*.html")
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
	mux.HandleFunc("GET /fragments/sidebar", s.sidebarFragment)
	mux.HandleFunc("GET /n/{node}/{$}", s.nodePage)
	s.routes(mux)
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
	s.show(w, place{Page: "network"}, "network", &view, false)
}

func (s *server) networkFragment(w http.ResponseWriter, _ *http.Request) {
	view := s.poll.View()
	s.show(w, place{}, "network", &view, true)
}

// sidebarFragment is the node list of the sidebar, polled on every page.
func (s *server) sidebarFragment(w http.ResponseWriter, _ *http.Request) {
	view := s.poll.View()
	s.show(w, place{}, "sidebar", view.Cards, true)
}

// hashLastChars is how many characters of a hash the pages always show at the end.
const hashLastChars = 8

// hashStart and hashEnd split a hash so the page can cut the middle with CSS when it does not fit.
func hashStart(h string) string {
	if len(h) <= hashLastChars {
		return h
	}

	return h[:len(h)-hashLastChars]
}

func hashEnd(h string) string {
	if len(h) <= hashLastChars {
		return ""
	}

	return h[len(h)-hashLastChars:]
}

// nodePage answers only for a configured node: the name comes from the config, never the URL.
func (s *server) nodePage(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("node")
	if !s.known[name] {
		http.NotFound(w, r)

		return
	}

	data := struct {
		Name   string
		Card   *Card
		Latest homeView
	}{Name: name}

	for _, c := range s.poll.View().Cards {
		if c.Name == name {
			data.Card = &c

			break
		}
	}

	if data.Card != nil && data.Card.Seen {
		node, _ := s.lookup(name)
		data.Latest = s.loadHome(r.Context(), node, data.Card.Height)
	} else {
		data.Latest.Msg = "Latest activity appears after the first poll of this node."
	}

	s.show(w, place{Node: name, Page: "node"}, "node", data, false)
}

// place is where a page sits in the shell: Node is the node of the page (blank on the network
// pages), Page the key of the sidebar link or node tab that is this page (blank when none is) and
// Crumb the last breadcrumb label (blank: the node name, or Network).
type place struct {
	Node, Page, Crumb string
}

// layout wraps an already rendered body (html/template output) in the page shell.
func (s *server) layout(w http.ResponseWriter, status int, at place, body string) {
	var buf bytes.Buffer

	view := s.poll.View()

	err := s.tmpl.ExecuteTemplate(&buf, "index.html", struct {
		Nodes   []Node
		Cards   []Card
		Current string
		Page    string
		Crumb   string
		Updated string
		Body    template.HTML
	}{s.nodes, view.Cards, at.Node, at.Page, at.Crumb, s.poll.now().UTC().Format("15:04:05"), template.HTML(body)}) //nolint:gosec // body is the output of html/template
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

// routes registers the chain screens. Detail routes take the rest of the path ({x...}) so a value
// with a slash reaches validation and gets 400 instead of falling through to 404.
func (s *server) routes(mux *http.ServeMux) {
	mempool := screen{body: "mempool", page: "mempool", crumb: label("Mempool"), load: s.loadMempool}
	blocks := screen{body: "blocks", page: "blocks", crumb: label("Blocks"), load: s.loadBlocks}
	block := screen{body: "block", crumb: func(r *http.Request) string { return "Block " + r.PathValue("ref") }, load: s.loadBlock}
	tx := screen{body: "tx", crumb: label("Transaction"), load: s.loadTx}
	address := screen{body: "address", crumb: label("Address"), load: s.loadAddress}
	contract := screen{body: "contract", crumb: label("Contract"), load: s.loadContract}

	mux.HandleFunc("GET /peers", s.peersScreen(false))
	mux.HandleFunc("GET /fragments/peers", s.peersScreen(true))
	mux.HandleFunc("GET /n/{node}/mempool", s.nodeScreen(mempool, false))
	mux.HandleFunc("GET /fragments/n/{node}/mempool", s.nodeScreen(mempool, true))
	mux.HandleFunc("GET /n/{node}/blocks", s.nodeScreen(blocks, false))
	mux.HandleFunc("GET /fragments/n/{node}/blocks", s.nodeScreen(blocks, true))
	mux.HandleFunc("GET /n/{node}/block/{ref...}", s.nodeScreen(block, false))
	mux.HandleFunc("GET /n/{node}/tx/{id...}", s.nodeScreen(tx, false))
	mux.HandleFunc("GET /n/{node}/address/{addr...}", s.nodeScreen(address, false))
	mux.HandleFunc("GET /n/{node}/contract/{id...}", s.nodeScreen(contract, false))
	mux.HandleFunc("GET /search", s.search)
	mux.HandleFunc("GET /metrics", s.metricsScreen)
	mux.HandleFunc("GET /fragments/metrics", s.metricsFragment)
	mux.HandleFunc("GET /fragments/charts", s.chartsFragment)
}
