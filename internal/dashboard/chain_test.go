package dashboard_test

import (
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/petersonsalme/blockchain-with-go/internal/dashboard"
)

const (
	txA     = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	txB     = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	blkHash = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	owner   = "00112233445566778899aabbccddeeff0011223344556677"
)

var (
	ownerAddr       = owner + "ab" // 50 lowercase hex
	badChecksumAddr = owner + "cd" // 50 lowercase hex the node answers 400 for
)

// reply is a canned node answer.
type reply struct {
	status int
	body   string
}

func ok(body string) reply { return reply{http.StatusOK, body} }

// routedNode answers by request URI; anything else is a 404.
func routedNode(t *testing.T, routes map[string]reply) *countingNode {
	t.Helper()

	return newCountingNode(t, func(w http.ResponseWriter, r *http.Request) {
		rep, found := routes[r.URL.RequestURI()]
		if !found {
			http.Error(w, `{"error":"nope"}`, http.StatusNotFound)

			return
		}

		w.WriteHeader(rep.status)
		_, _ = w.Write([]byte(rep.body))
	})
}

func chainServer(t *testing.T, nodes ...*countingNode) http.Handler {
	t.Helper()

	var list []dashboard.Node

	for i, n := range nodes {
		node := n.node(t)
		node.Name = "node" + string(rune('1'+i))
		list = append(list, node)
	}

	fetch := dashboard.NewFetcher(time.Second, 1<<20)

	h, err := dashboard.NewServer(list, fetch, dashboard.NewPoller(list, fetch, time.Hour, time.Now))
	if err != nil {
		t.Fatal(err)
	}

	return h
}

func txJSON(id, to string) string {
	return `{"ID":"` + id + `","Inputs":[{"PrevTxID":"` + txB + `","OutIndex":3,"PublicKey":"pk1","Signature":"sig1"}],` +
		`"Outputs":[{"Amount":4200,"Address":"` + to + `"}],"Timestamp":"2026-01-02T03:04:05Z"}`
}

func blockJSON(index int, hash string, txs ...string) string {
	return `{"Index":` + itoa(index) + `,"Timestamp":"2026-01-02T03:04:05Z","Transactions":[` + strings.Join(txs, ",") + `],` +
		`"Hash":"` + hash + `","PrevHash":"` + txB + `","Nonce":77,"Difficulty":5,"Validator":"val1","Signature":"sigv"}`
}

func itoa(i int) string { return strconv.Itoa(i) }

func fullChainNode(t *testing.T) *countingNode {
	t.Helper()

	routes := map[string]reply{}
	routes["/p2p/peers"] = ok(`{"peers":[{"node_id":"nid-1","addr":"http://peer1:8080","height":9,"tip_hash":"` + blkHash + `","inbound":true,"last_seen":"2026-01-02T03:04:05Z"}],"addrs":["http://known:8080"]}`)
	routes["/mempool"] = ok(`{"transactions":[` + txJSON(txA, ownerAddr) + `]}`)
	routes["/blocks?from=0&limit=20"] = ok(`{"blocks":[` + blockJSON(0, "", txJSON(txA, ownerAddr)) + `,` + blockJSON(1, blkHash) + `],"from":0,"limit":20,"next":2}`)
	routes["/blocks?from=2&limit=20"] = ok(`{"blocks":[` + blockJSON(2, "dd") + `],"from":2,"limit":20}`)
	routes["/block/1"] = ok(blockJSON(1, blkHash, txJSON(txA, ownerAddr)))
	routes["/block/"+blkHash] = ok(blockJSON(1, blkHash, txJSON(txA, ownerAddr)))
	routes["/tx/"+txA] = ok(`{"transaction":` + txJSON(txA, ownerAddr) + `,"block_hash":"` + blkHash + `","height":1}`)
	routes["/tx/"+txA+"/proof"] = ok(`{"tx_id":"` + txA + `","block_index":1,"block_hash":"` + blkHash + `","merkle_root":"rootrootroot","leaf_index":2,"leaf_hash":"leafleaf","proof":[{"hash":"sib0","side":"left"},{"hash":"sib1","side":"right"}]}`)
	routes["/balance/"+ownerAddr] = ok(`{"address":"` + ownerAddr + `","balance":4200,"utxos":[{"txid":"` + txA + `","index":0,"amount":4200}]}`)
	routes["/balance/"+badChecksumAddr] = reply{http.StatusBadRequest, `{"error":"checksum"}`}
	routes["/address/"+ownerAddr+"/txs?limit=25"] = ok(`{"address":"` + ownerAddr + `","txs":[{"transaction":` + txJSON(txA, ownerAddr) + `,"block_hash":"` + blkHash + `","height":1,"timestamp":"2026-01-02T03:04:05Z"}],"limit":25,"next":"` + txA + `","pruned_below":0}`)
	routes["/address/"+ownerAddr+"/txs?limit=25&after="+txA] = ok(`{"address":"` + ownerAddr + `","txs":[],"limit":25,"pruned_below":7}`)
	routes["/contracts/"+txA] = ok(`{"id":"` + txA + `","code":"0102","storage":{"10":7,"2":5,"11":8}}`)
	routes["/address/"+ownerAddr+"/txs?limit=25&after="+txB] = reply{http.StatusUnprocessableEntity, `{"error":"x","code":"unknown_cursor"}`}

	return routedNode(t, routes)
}

func TestChainRoutesShowTheNodeValues(t *testing.T) {
	t.Parallel()

	h := chainServer(t, fullChainNode(t))

	tests := map[string][]string{
		"/peers":                        {"nid-1", "http://peer1:8080", ">9<", blkHash, "yes", "2026-01-02T03:04:05Z", "http://known:8080"},
		"/n/node1/mempool":              {txA, ">0.000042<", `title="4,200 base units"`, ownerAddr, "pk1", "sig1", "2026-01-02T03:04:05Z", `href="/n/node1/address/` + ownerAddr + `"`},
		"/n/node1/blocks":               {`href="/n/node1/block/0"`, `href="/n/node1/block/1"`, blkHash, "val1", `href="/n/node1/blocks?from=2"`},
		"/n/node1/blocks?from=2":        {`href="/n/node1/block/2"`},
		"/n/node1/block/1":              {"Block 1", blkHash, txB, ">77<", ">5<", "val1", "sigv", `href="/n/node1/tx/` + txA + `"`, `href="/n/node1/address/` + ownerAddr + `"`},
		"/n/node1/block/" + blkHash:     {"Block 1"},
		"/n/node1/tx/" + txA:            {txA, blkHash, `href="/n/node1/block/1"`, "rootrootroot", ">2<", "leafleaf", "sib0", "left", "sib1", "right", ">0.000042<", `title="4,200 base units"`},
		"/n/node1/address/" + ownerAddr: {ownerAddr, ">0.000042<", `title="4,200 base units"`, "Amount (coins)", `href="/n/node1/tx/` + txA + `"`, `href="/n/node1/address/` + ownerAddr + `?after=` + txA + `"`},
		"/n/node1/address/" + ownerAddr + "?after=" + txA: {"Heights below 7 are pruned"},
		"/n/node1/contract/" + txA:                        {txA, "0102", ">10<", ">7<"},
	}

	for path, wants := range tests {
		rec := do(h, http.MethodGet, path)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d: %s", path, rec.Code, rec.Body)

			continue
		}

		for _, want := range wants {
			if !strings.Contains(rec.Body.String(), want) {
				t.Errorf("GET %s lacks %q", path, want)
			}
		}
	}
}

func TestContractStorageIsSortedByNumericKey(t *testing.T) {
	t.Parallel()

	body := do(chainServer(t, fullChainNode(t)), http.MethodGet, "/n/node1/contract/"+txA).Body.String()

	i2, i10, i11 := strings.Index(body, ">2<"), strings.Index(body, ">10<"), strings.Index(body, ">11<")
	if i2 >= i10 || i10 >= i11 {
		t.Error("keys should come in numeric order: 2, 10, 11")
	}
}

func TestAddressRestartsFromTheFirstPageOnUnknownCursor(t *testing.T) {
	t.Parallel()

	rec := do(chainServer(t, fullChainNode(t)), http.MethodGet, "/n/node1/address/"+ownerAddr+"?after="+txB)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}

	for _, want := range []string{"no longer valid", `href="/n/node1/tx/` + txA + `"`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("page lacks %q", want)
		}
	}
}

func TestInvalidParametersAre400WithoutOutboundRequest(t *testing.T) {
	t.Parallel()

	n := fullChainNode(t)
	h := chainServer(t, n)

	for _, path := range []string{
		"/n/node1/block/xyz", "/n/node1/block/", "/n/node1/block/1/2", "/n/node1/block/" + strings.ToUpper(blkHash),
		"/n/node1/tx/123", "/n/node1/tx/" + strings.ToUpper(txA), "/n/node1/tx/" + txA + "/proof",
		"/n/node1/address/" + strings.ToUpper(ownerAddr), "/n/node1/address/" + owner + "a/", "/n/node1/address/" + ownerAddr + "/x",
		"/n/node1/address/" + ownerAddr[:49] + "%2F", "/n/node1/address/short",
		"/n/node1/address/" + ownerAddr + "?after=zz", "/n/node1/address/" + ownerAddr + "?after=" + strings.ToUpper(txA),
		"/n/node1/contract/xyz", "/n/node1/contract/" + txA[:63],
		"/n/node1/blocks?from=-1", "/n/node1/blocks?from=abc", "/n/node1/blocks?from=1.5",
	} {
		if rec := do(h, http.MethodGet, path); rec.Code != http.StatusBadRequest {
			t.Errorf("GET %s = %d, want 400", path, rec.Code)
		}
	}

	if n.hits.Load() != 0 {
		t.Errorf("node got %d requests: %v", n.hits.Load(), n.seen)
	}
}

func TestUnknownNodeIs404ForEveryChainRoute(t *testing.T) {
	t.Parallel()

	n := fullChainNode(t)
	h := chainServer(t, n)

	for _, path := range []string{
		"/n/ghost/mempool", "/n/ghost/blocks", "/n/ghost/block/1", "/n/ghost/tx/" + txA, "/n/ghost/address/" + ownerAddr,
		"/n/ghost/contract/" + txA, "/fragments/n/ghost/mempool", "/fragments/n/ghost/blocks",
		"/search?node=ghost&q=1", "/search?q=1", "/n/ghost/block/xyz",
	} {
		if rec := do(h, http.MethodGet, path); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, rec.Code)
		}
	}

	if n.hits.Load() != 0 {
		t.Errorf("node got %d requests", n.hits.Load())
	}
}

func TestNodeFailuresBecomeDashboardPages(t *testing.T) {
	t.Parallel()

	down := fullChainNode(t)
	down.Close()

	bad := routedNode(t, map[string]reply{
		"/mempool":                {http.StatusInternalServerError, `{}`},
		"/blocks?from=0&limit=20": ok(`not json`),
		"/block/1":                {http.StatusGone, `{"code":"block_pruned"}`},
		"/block/2":                {http.StatusBadRequest, `{}`},
	})

	h := chainServer(t, fullChainNode(t), down, bad)

	tests := []struct {
		path   string
		status int
		want   string
	}{
		{"/n/node1/block/9", http.StatusNotFound, "node node1 does not have it"},
		{"/n/node1/tx/" + txB, http.StatusNotFound, "does not have it"},
		{"/n/node1/contract/" + txB, http.StatusNotFound, "does not have it"},
		{"/n/node2/mempool", http.StatusBadGateway, "node node2 is unreachable"},
		{"/n/node2/blocks", http.StatusBadGateway, "node node2"},
		{"/n/node2/block/1", http.StatusBadGateway, "node node2"},
		{"/n/node2/tx/" + txA, http.StatusBadGateway, "node node2"},
		{"/n/node2/address/" + ownerAddr, http.StatusBadGateway, "node node2"},
		{"/n/node2/contract/" + txA, http.StatusBadGateway, "node node2"},
		{"/n/node3/mempool", http.StatusBadGateway, "node node3 answered with status 500"},
		{"/n/node3/blocks", http.StatusBadGateway, "node node3 sent an answer the dashboard cannot read"},
		{"/n/node3/block/1", http.StatusGone, "pruned"},
		{"/n/node3/block/2", http.StatusBadRequest, "node node3 rejected the request"},
		{"/fragments/n/node2/mempool", http.StatusBadGateway, "node node2"},
	}

	for _, tc := range tests {
		rec := do(h, http.MethodGet, tc.path)
		if rec.Code != tc.status || !strings.Contains(rec.Body.String(), tc.want) {
			t.Errorf("GET %s = %d (want %d containing %q): %s", tc.path, rec.Code, tc.status, tc.want, rec.Body)
		}

		if rec.Header().Get("Content-Security-Policy") != wantCSP {
			t.Errorf("GET %s lacks the CSP", tc.path)
		}
	}
}

func TestPeersScreenKeepsNodesApart(t *testing.T) {
	t.Parallel()

	down := fullChainNode(t)
	down.Close()

	noP2P := routedNode(t, nil) // /p2p/peers answers 404

	rec := do(chainServer(t, fullChainNode(t), down, noP2P), http.MethodGet, "/peers")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}

	for _, want := range []string{"nid-1", "node node2 is unreachable", "No peer-to-peer layer"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("page lacks %q", want)
		}
	}
}

func TestSearch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		q    string
		want string // Location, empty for the search page
	}{
		{"1", "/n/node1/block/1"},
		{"  1 ", "/n/node1/block/1"},
		{txA, "/n/node1/tx/" + txA},
		{strings.ToUpper(txA), "/n/node1/tx/" + txA},
		{blkHash, "/n/node1/block/" + blkHash},
		{ownerAddr, "/n/node1/address/" + ownerAddr},
		{strings.ToUpper(ownerAddr), "/n/node1/address/" + ownerAddr},
		{"7", ""},
		{"  7 ", ""},
		{txB, ""},
		{badChecksumAddr, ""},
		{"hello", ""},
		{"", ""},
		{"../etc", ""},
		{"http://evil.example/", ""},
		{strings.Repeat("9", 200), ""},
		{"1/2", ""},
	}

	h := chainServer(t, fullChainNode(t))

	for _, tc := range tests {
		rec := do(h, http.MethodGet, "/search?node=node1&q="+url.QueryEscape(tc.q))

		if tc.want == "" {
			// An empty query, or one too long to look at, gets the plain page; a miss is a 404.
			wantStatus, wantMiss := http.StatusNotFound, true
			if q := strings.TrimSpace(tc.q); q == "" || len(q) > 128 {
				wantStatus, wantMiss = http.StatusOK, false
			}

			if rec.Code != wantStatus || rec.Header().Get("Location") != "" {
				t.Errorf("q=%q: %d Location %q, want %d without a Location", tc.q, rec.Code, rec.Header().Get("Location"), wantStatus)
			}

			if strings.Contains(rec.Body.String(), "Not found") != wantMiss {
				t.Errorf("q=%q: Not found shown = %t, want %t", tc.q, !wantMiss, wantMiss)
			}

			continue
		}

		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != tc.want {
			t.Errorf("q=%q: %d Location %q, want 303 %s", tc.q, rec.Code, rec.Header().Get("Location"), tc.want)
		}
	}
}

func TestSearchQueryLengthLimit(t *testing.T) {
	t.Parallel()

	h := chainServer(t, fullChainNode(t))

	long := do(h, http.MethodGet, "/search?node=node1&q="+strings.Repeat("x", 128)).Body.String()
	if !strings.Contains(long, "Not found: "+strings.Repeat("x", 128)) {
		t.Error("a 128 character query should be looked up and reported")
	}

	over := do(h, http.MethodGet, "/search?node=node1&q="+strings.Repeat("x", 129)).Body.String()
	if strings.Contains(over, "Not found") || strings.Contains(over, "xxxx") {
		t.Error("a 129 character query should be ignored, not echoed")
	}
}

func TestSearchUsesOnlyAllowlistedReads(t *testing.T) {
	t.Parallel()

	n := fullChainNode(t)
	h := chainServer(t, n)

	for _, q := range []string{"nope", "../x"} {
		do(h, http.MethodGet, "/search?node=node1&q="+url.QueryEscape(q))
	}

	if n.hits.Load() != 0 {
		t.Errorf("search without a known shape read the node: %v", n.seen)
	}

	for _, q := range []string{"7", ownerAddr, strings.ToUpper(txA)} {
		do(h, http.MethodGet, "/search?node=node1&q="+url.QueryEscape(q))
	}

	// One read per query: the height and the address stop at their first answer, the id at the transaction.
	want := []string{"GET /block/7", "GET /balance/" + ownerAddr, "GET /tx/" + txA}
	if !slices.Equal(n.seen, want) {
		t.Errorf("node saw %v, want %v", n.seen, want)
	}
}

func TestSearchPrefersTransactionOverBlockHash(t *testing.T) {
	t.Parallel()

	n := routedNode(t, map[string]reply{
		"/tx/" + txA:    ok(`{}`),
		"/block/" + txA: ok(`{}`),
	})

	rec := do(chainServer(t, n), http.MethodGet, "/search?node=node1&q="+txA)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/n/node1/tx/"+txA {
		t.Errorf("= %d Location %q, want 303 to the transaction", rec.Code, rec.Header().Get("Location"))
	}
}

func TestSearchNodeErrorIsAPageNotARedirect(t *testing.T) {
	t.Parallel()

	n := routedNode(t, map[string]reply{
		"/tx/" + txA:    {http.StatusInternalServerError, `{}`},
		"/block/" + txA: ok(`{}`),
	})

	rec := do(chainServer(t, n), http.MethodGet, "/search?node=node1&q="+txA)
	if rec.Code != http.StatusBadGateway || rec.Header().Get("Location") != "" || !strings.Contains(rec.Body.String(), "node node1 answered with status 500") {
		t.Errorf("= %d Location %q: %s", rec.Code, rec.Header().Get("Location"), rec.Body)
	}
}

func TestSearchWhenTheNodeIsDown(t *testing.T) {
	t.Parallel()

	n := fullChainNode(t)
	n.Close()

	rec := do(chainServer(t, n), http.MethodGet, "/search?node=node1&q="+txA)
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "node node1") {
		t.Errorf("= %d: %s", rec.Code, rec.Body)
	}
}

func TestPollingOnListScreensOnly(t *testing.T) {
	t.Parallel()

	h := chainServer(t, fullChainNode(t))

	for path, want := range map[string]bool{
		"/peers":                        true,
		"/n/node1/mempool":              true,
		"/n/node1/blocks":               true,
		"/n/node1/blocks?from=0":        true,
		"/n/node1/blocks?from=2":        false,
		"/n/node1/block/1":              false,
		"/n/node1/tx/" + txA:            false,
		"/n/node1/address/" + ownerAddr: false,
		"/n/node1/contract/" + txA:      false,
	} {
		body := strings.ReplaceAll(do(h, http.MethodGet, path).Body.String(), sidebarPoll, "")
		if got := strings.Contains(body, `hx-trigger="every 2s"`); got != want {
			t.Errorf("GET %s polls = %t, want %t", path, got, want)
		}
	}

	for path, hx := range map[string]string{
		"/fragments/peers":           `hx-get="/fragments/peers"`,
		"/fragments/n/node1/mempool": `hx-get="/fragments/n/node1/mempool"`,
		"/fragments/n/node1/blocks":  `hx-get="/fragments/n/node1/blocks"`,
	} {
		rec := do(h, http.MethodGet, path)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), hx) || strings.Contains(rec.Body.String(), "<html") {
			t.Errorf("GET %s = %d, want a bare fragment with %s", path, rec.Code, hx)
		}
	}
}

// hostile is a node value that breaks out of HTML text, attributes and URLs when not escaped.
const hostile = `"><script>alert(1)</script>'&/..`

func hostileNode(t *testing.T) *countingNode {
	t.Helper()

	hostileTx := `{"ID":"` + hostileJSON() + `","Inputs":[{"PrevTxID":"` + hostileJSON() + `","OutIndex":0,"PublicKey":"` + hostileJSON() + `","Signature":"` + hostileJSON() + `"}],` +
		`"Outputs":[{"Amount":1,"Address":"` + hostileJSON() + `"}],"Timestamp":"2026-01-02T03:04:05Z",` +
		`"Contract":{"Kind":"` + hostileJSON() + `","Code":"` + hostileJSON() + `","Target":"` + hostileJSON() + `","Args":[1],"GasLimit":5}}`
	hostileBlock := `{"Index":1,"Timestamp":"2026-01-02T03:04:05Z","Transactions":[` + hostileTx + `],"Hash":"` + hostileJSON() + `","PrevHash":"` + hostileJSON() +
		`","Nonce":1,"Difficulty":1,"Validator":"` + hostileJSON() + `","Signature":"` + hostileJSON() + `"}`

	routes := map[string]reply{}
	routes["/p2p/peers"] = ok(`{"peers":[{"node_id":"` + hostileJSON() + `","addr":"` + hostileJSON() + `","height":1,"tip_hash":"` + hostileJSON() + `","inbound":false,"last_seen":"2026-01-02T03:04:05Z"}],"addrs":["` + hostileJSON() + `"]}`)
	routes["/mempool"] = ok(`{"transactions":[` + hostileTx + `]}`)
	routes["/blocks?from=0&limit=20"] = ok(`{"blocks":[` + hostileBlock + `],"from":0,"limit":20}`)
	routes["/block/1"] = ok(hostileBlock)
	routes["/tx/"+txA] = ok(`{"transaction":` + hostileTx + `,"block_hash":"` + hostileJSON() + `","height":1}`)
	routes["/tx/"+txA+"/proof"] = ok(`{"merkle_root":"` + hostileJSON() + `","leaf_index":0,"leaf_hash":"` + hostileJSON() + `","proof":[{"hash":"` + hostileJSON() + `","side":"` + hostileJSON() + `"}]}`)
	routes["/balance/"+ownerAddr] = ok(`{"address":"x","balance":1,"utxos":[{"txid":"` + hostileJSON() + `","index":0,"amount":1}]}`)
	routes["/address/"+ownerAddr+"/txs?limit=25"] = ok(`{"txs":[{"transaction":` + hostileTx + `,"block_hash":"` + hostileJSON() + `","height":1,"timestamp":"2026-01-02T03:04:05Z"}],"limit":25,"next":"` + hostileJSON() + `","pruned_below":0}`)
	routes["/contracts/"+txA] = ok(`{"id":"` + hostileJSON() + `","code":"` + hostileJSON() + `","storage":{"` + hostileJSON() + `":1}}`)

	return routedNode(t, routes)
}

// hostileJSON is hostile as the inside of a JSON string.
func hostileJSON() string {
	return strings.NewReplacer(`"`, `\"`).Replace(hostile)
}

func TestDetailTemplatesEscapeHostileNodeValues(t *testing.T) {
	t.Parallel()

	h := chainServer(t, hostileNode(t))

	for _, path := range []string{
		"/peers", "/fragments/peers",
		"/n/node1/mempool", "/fragments/n/node1/mempool",
		"/n/node1/blocks", "/fragments/n/node1/blocks",
		"/n/node1/block/1", "/n/node1/tx/" + txA,
		"/n/node1/address/" + ownerAddr, "/n/node1/contract/" + txA,
	} {
		rec := do(h, http.MethodGet, path)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d: %s", path, rec.Code, rec.Body)

			continue
		}

		body := rec.Body.String()

		if strings.Contains(body, "<script>alert") || strings.Contains(body, `"><script`) {
			t.Errorf("GET %s renders a node value unescaped", path)
		}

		if strings.Count(body, "<script") != strings.Count(body, `<script src="/static/htmx.min.js">`) {
			t.Errorf("GET %s has a script element from the node", path)
		}

		if !strings.Contains(body, "&lt;script&gt;") && !strings.Contains(body, "%3Cscript%3E") {
			t.Errorf("GET %s does not show the hostile value escaped", path)
		}
	}
}

func TestLinksEscapeNodeValuesAsPathSegments(t *testing.T) {
	t.Parallel()

	body := do(chainServer(t, hostileNode(t)), http.MethodGet, "/n/node1/tx/"+txA).Body.String()
	if !strings.Contains(body, "/n/node1/address/%22%3E%3Cscript%3Ealert%281%29%3C%2Fscript%3E%27&amp;%2F..") {
		t.Errorf("address link is not path-escaped:\n%s", body)
	}

	if strings.Contains(body, `href="/n/node1/address/"`) {
		t.Error("address link lost its parameter")
	}
}

func TestChainRoutesServeOnlyGetAndHead(t *testing.T) {
	t.Parallel()

	n := fullChainNode(t)
	h := chainServer(t, n)

	for _, path := range []string{"/peers", "/n/node1/mempool", "/n/node1/blocks", "/n/node1/block/1", "/n/node1/tx/" + txA, "/n/node1/address/" + ownerAddr, "/n/node1/contract/" + txA, "/search?node=node1&q=1"} {
		if rec := do(h, http.MethodPost, path); rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("POST %s = %d, want 405", path, rec.Code)
		}
	}

	if n.hits.Load() != 0 {
		t.Error("a rejected method reached the node")
	}
}

func TestNavigationCarriesTheSearchForm(t *testing.T) {
	t.Parallel()

	body := do(chainServer(t, fullChainNode(t), fullChainNode(t)), http.MethodGet, "/n/node2/blocks").Body.String()
	for _, want := range []string{
		`<form method="get" action="/search"`, `<option value="node2" selected>`, `href="/peers"`,
		`href="/n/node2/mempool"`, `href="/n/node2/blocks"`, `href="/n/node1/"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %s", want)
		}
	}
}

func TestEveryChainScreenExplainsItselfOutsideTheRefreshedFragment(t *testing.T) {
	t.Parallel()

	h := chainServer(t, fullChainNode(t))

	for page, id := range map[string]string{
		"/peers":                        "peers-help",
		"/n/node1/mempool":              "mempool-help",
		"/n/node1/blocks":               "blocks-help",
		"/n/node1/block/1":              "block-help",
		"/n/node1/tx/" + txA:            "tx-help",
		"/n/node1/address/" + ownerAddr: "address-help",
	} {
		body := do(h, http.MethodGet, page).Body.String()
		for _, want := range []string{`aria-describedby="` + id + `"`, `id="` + id + `" role="tooltip"`} {
			if !strings.Contains(body, want) {
				t.Errorf("GET %s lacks %q", page, want)
			}
		}
	}

	// htmx swaps these fragments every 2 s: a tooltip inside would close under the pointer.
	for _, frag := range []string{"/fragments/peers", "/fragments/n/node1/mempool", "/fragments/n/node1/blocks"} {
		if body := do(h, http.MethodGet, frag).Body.String(); strings.Contains(body, `role="tooltip"`) {
			t.Errorf("GET %s carries the help tooltip", frag)
		}
	}
}

func TestPendingTxShowsItsPosition(t *testing.T) {
	t.Parallel()

	n := routedNode(t, map[string]reply{
		"/tx/" + txB: {http.StatusNotFound, `{"error":"nope"}`},
		"/mempool":   ok(`{"transactions":[` + txJSON(txA, ownerAddr) + `,` + txJSON(txB, ownerAddr) + `]}`),
	})

	rec := do(chainServer(t, n), http.MethodGet, "/n/node1/tx/"+txB)
	body := rec.Body.String()

	// The proof section's anchor, not its text: the help tooltip always names the Merkle proof.
	if rec.Code != http.StatusOK || !strings.Contains(body, "Pending: position 2 of 2") || strings.Contains(body, `id="merkle"`) {
		t.Errorf("= %d: %s", rec.Code, body)
	}

	if slices.Contains(n.seen, "GET /tx/"+txB+"/proof") {
		t.Errorf("pending transaction read its proof: %v", n.seen)
	}
}

func TestTxPageNodeErrorIsNotPending(t *testing.T) {
	t.Parallel()

	n := routedNode(t, map[string]reply{
		"/tx/" + txA: {http.StatusInternalServerError, `{}`},
		"/mempool":   ok(`{"transactions":[` + txJSON(txA, ownerAddr) + `]}`),
	})

	rec := do(chainServer(t, n), http.MethodGet, "/n/node1/tx/"+txA)
	if rec.Code != http.StatusBadGateway {
		t.Errorf("= %d, want 502: %s", rec.Code, rec.Body)
	}

	if slices.Contains(n.seen, "GET /mempool") {
		t.Errorf("node error read the mempool: %v", n.seen)
	}
}

func TestTxNeitherInChainNorInPoolIs404(t *testing.T) {
	t.Parallel()

	n := routedNode(t, map[string]reply{
		"/mempool": ok(`{"transactions":[` + txJSON(txA, ownerAddr) + `]}`),
	})

	if rec := do(chainServer(t, n), http.MethodGet, "/n/node1/tx/"+txB); rec.Code != http.StatusNotFound {
		t.Errorf("= %d, want 404: %s", rec.Code, rec.Body)
	}
}

func TestMempoolRowsShowPosition(t *testing.T) {
	t.Parallel()

	n := routedNode(t, map[string]reply{
		"/mempool": ok(`{"transactions":[` + txJSON(txA, ownerAddr) + `,` + txJSON(txB, ownerAddr) + `]}`),
	})

	body := do(chainServer(t, n), http.MethodGet, "/n/node1/mempool").Body.String()

	first, second := strings.Index(body, "#1 "), strings.Index(body, "#2 ")
	if first < 0 || second < first {
		t.Errorf("rows lack #1 before #2: %s", body)
	}
}
