package dashboard_test

import (
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

var (
	olderLink  = regexp.MustCompile(`href="([^"]+)">Older transactions</a>`)
	txLink     = regexp.MustCompile(`href="/n/node1/tx/([0-9a-f]{64})"`)
	storageKey = regexp.MustCompile(`<tr><td class="mono">(\d+)</td>`)
)

// numberedTxs returns n transactions whose ids count up from first.
func numberedTxs(first, n int) []string {
	txs := make([]string, 0, n)
	for i := range n {
		txs = append(txs, txJSON(fmt.Sprintf("%064x", first+i), ownerAddr))
	}

	return txs
}

func TestAddressHistoryWalksToTheEnd(t *testing.T) {
	t.Parallel()

	const total = 60

	ids := make([]string, 0, total)
	for i := 1; i <= total; i++ {
		ids = append(ids, fmt.Sprintf("%064x", i))
	}

	// Pages of 25, 25 and 10. The last id of a page is the cursor of the next one.
	routes := map[string]reply{
		"/balance/" + ownerAddr: ok(`{"address":"` + ownerAddr + `","balance":0,"utxos":[]}`),
	}
	start, cursor := 0, ""

	for _, size := range []int{25, 25, 10} {
		page := ids[start : start+size]

		entries := make([]string, 0, size)
		for _, id := range page {
			entries = append(entries, `{"transaction":`+txJSON(id, ownerAddr)+`,"block_hash":"`+blkHash+`","height":1,"timestamp":"2026-01-02T03:04:05Z"}`)
		}

		uri := "/address/" + ownerAddr + "/txs?limit=25"
		if cursor != "" {
			uri += "&after=" + cursor
		}

		body := `{"address":"` + ownerAddr + `","txs":[` + strings.Join(entries, ",") + `],"limit":25`
		if start+size < total {
			body += `,"next":"` + page[len(page)-1] + `"`
		}

		routes[uri] = ok(body + `}`)
		cursor = page[len(page)-1]
		start += size
	}

	h := chainServer(t, routedNode(t, routes))

	path := "/n/node1/address/" + ownerAddr
	pages := 0

	var seen []string

	for {
		rec := do(h, http.MethodGet, path)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d: %s", path, rec.Code, rec.Body)
		}

		pages++
		if pages > 3 {
			t.Fatalf("walked %d pages: the Older link never ends", pages)
		}

		body := rec.Body.String()
		for _, m := range txLink.FindAllStringSubmatch(body, -1) {
			seen = append(seen, m[1])
		}

		link := olderLink.FindStringSubmatch(body)
		if link == nil {
			break // the last page has no Older link
		}

		path = link[1]
	}

	if pages != 3 {
		t.Errorf("pages = %d, want 3", pages)
	}

	if !slices.Equal(seen, ids) {
		t.Errorf("walked %d transactions, want the 60 distinct ids in order", len(seen))
	}
}

func TestPagesRenderEmptyInputs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		build   func(t *testing.T) http.Handler
		path    string
		want    []string
		notWant []string
	}{
		{
			name: "empty mempool",
			build: func(t *testing.T) http.Handler {
				t.Helper()

				return chainServer(t, routedNode(t, map[string]reply{"/mempool": ok(`{"transactions":[]}`)}))
			},
			path: "/n/node1/mempool",
			want: []string{`<p class="empty-title">No transactions</p>`},
		},
		{
			name: "address with no balance, utxos or history",
			build: func(t *testing.T) http.Handler {
				t.Helper()

				return chainServer(t, routedNode(t, map[string]reply{
					"/balance/" + ownerAddr:                   ok(`{"address":"` + ownerAddr + `","balance":0,"utxos":[]}`),
					"/address/" + ownerAddr + "/txs?limit=25": ok(`{"address":"` + ownerAddr + `","txs":[],"limit":25,"pruned_below":0}`),
				}))
			},
			path: "/n/node1/address/" + ownerAddr,
			want: []string{"No unspent outputs.", "No transactions."},
		},
		{
			name: "node home at height 0 with an empty genesis block",
			build: func(t *testing.T) http.Handler {
				t.Helper()

				h, _ := homeServer(t, 0, `{"blocks":[`+blockJSON(0, blkHash)+`]}`)

				return h
			},
			path: "/n/node1/",
			want: []string{"Latest blocks", "No transactions."},
		},
		{
			name: "contract with empty storage",
			build: func(t *testing.T) http.Handler {
				t.Helper()

				return chainServer(t, routedNode(t, map[string]reply{
					"/contracts/" + txA: ok(`{"id":"` + txA + `","code":"0102","storage":{}}`),
				}))
			},
			path: "/n/node1/contract/" + txA,
			want: []string{"No cell written yet."},
		},
		{
			name: "blocks list with no blocks",
			build: func(t *testing.T) http.Handler {
				t.Helper()

				return chainServer(t, routedNode(t, map[string]reply{
					"/blocks?from=0&limit=20": ok(`{"blocks":[],"from":0,"limit":20}`),
				}))
			},
			path:    "/n/node1/blocks",
			notWant: []string{"Next page"},
		},
		{
			name: "peers screen with no peers",
			build: func(t *testing.T) http.Handler {
				t.Helper()

				return chainServer(t, routedNode(t, map[string]reply{
					"/p2p/peers": ok(`{"peers":[],"addrs":[]}`),
				}))
			},
			path: "/peers",
			want: []string{"No connected peers."},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec := do(tt.build(t), http.MethodGet, tt.path)
			if rec.Code != http.StatusOK {
				t.Fatalf("GET %s = %d: %s", tt.path, rec.Code, rec.Body)
			}

			page := rec.Body.String()

			for _, want := range tt.want {
				if !strings.Contains(page, want) {
					t.Errorf("GET %s lacks %q", tt.path, want)
				}
			}

			for _, notWant := range tt.notWant {
				if strings.Contains(page, notWant) {
					t.Errorf("GET %s shows %q", tt.path, notWant)
				}
			}
		})
	}
}

func TestPagesRenderLargeInputs(t *testing.T) {
	t.Parallel()

	storage := make([]string, 0, 500)
	for i := range 500 {
		storage = append(storage, fmt.Sprintf(`"%d":%d`, i, i+1))
	}

	tests := []struct {
		name  string
		build func(t *testing.T) http.Handler
		path  string
		check func(t *testing.T, page string)
	}{
		{
			name: "block with 1000 transactions",
			build: func(t *testing.T) http.Handler {
				t.Helper()

				return chainServer(t, routedNode(t, map[string]reply{
					"/block/1": ok(blockJSON(1, blkHash, numberedTxs(1, 1000)...)),
				}))
			},
			path: "/n/node1/block/1",
			check: func(t *testing.T, page string) {
				t.Helper()

				if n := strings.Count(page, `<article class="tx">`); n != 1000 {
					t.Errorf("transactions shown = %d, want 1000", n)
				}
			},
		},
		{
			name: "mempool with 1000 transactions",
			build: func(t *testing.T) http.Handler {
				t.Helper()

				return chainServer(t, routedNode(t, map[string]reply{
					"/mempool": ok(`{"transactions":[` + strings.Join(numberedTxs(1, 1000), ",") + `]}`),
				}))
			},
			path: "/n/node1/mempool",
			check: func(t *testing.T, page string) {
				t.Helper()

				if n := strings.Count(page, `<article class="tx">`); n != 1000 {
					t.Errorf("transactions shown = %d, want 1000", n)
				}

				for _, pos := range []string{"#1 ", "#1000 "} {
					if !strings.Contains(page, pos) {
						t.Errorf("the page lacks position %q", pos)
					}
				}
			},
		},
		{
			name: "contract with 500 storage cells",
			build: func(t *testing.T) http.Handler {
				t.Helper()

				return chainServer(t, routedNode(t, map[string]reply{
					"/contracts/" + txA: ok(`{"id":"` + txA + `","code":"0102","storage":{` + strings.Join(storage, ",") + `}}`),
				}))
			},
			path: "/n/node1/contract/" + txA,
			check: func(t *testing.T, page string) {
				t.Helper()

				rows := storageKey.FindAllStringSubmatch(page, -1)
				if len(rows) != 500 {
					t.Fatalf("storage rows = %d, want 500", len(rows))
				}

				for i, row := range rows {
					if row[1] != strconv.Itoa(i) {
						t.Fatalf("storage row %d has key %s, want keys in numeric order", i, row[1])
					}
				}
			},
		},
		{
			name: "node home with 10 blocks of 100 transactions",
			build: func(t *testing.T) http.Handler {
				t.Helper()

				var blocks []string
				for i := range 10 {
					blocks = append(blocks, blockJSON(i, fmt.Sprintf("%064x", 0x1000+i), numberedTxs(i*100+1, 100)...))
				}

				h, _ := homeServer(t, 9, `{"blocks":[`+strings.Join(blocks, ",")+`]}`)

				return h
			},
			path: "/n/node1/",
			check: func(t *testing.T, page string) {
				t.Helper()

				if n := strings.Count(page, `href="/n/node1/tx/`); n != 10 {
					t.Errorf("transaction links = %d, want 10", n)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec := do(tt.build(t), http.MethodGet, tt.path)
			if rec.Code != http.StatusOK {
				t.Fatalf("GET %s = %d: %s", tt.path, rec.Code, rec.Body)
			}

			tt.check(t, rec.Body.String())
		})
	}
}

// consensusNodeRoutes answers a node whose /consensus is consensus and whose recent headers are
// headers. recentFrom(12) with ConsensusBlocks is 3, so the headers are read from 3 with limit 10.
func consensusNodeRoutes(consensus, headers string) map[string]reply {
	return map[string]reply{
		"/consensus":               ok(consensus),
		"/headers?from=3&limit=10": ok(headers),
	}
}

// powConsensus is a PoW node's /consensus body at height 12.
func powConsensus(tip, work string, targetMS int64) string {
	return fmt.Sprintf(`{"engine":"pow","height":12,"tip":"%s","chain_work":"%s","pow":{"initial_difficulty":3,"retarget_interval":0,"target_block_time_ms":%d}}`, tip, work, targetMS)
}

// powHeaders are the headers at heights 11 and 12, each of difficulty 3 (block work 8).
func powHeaders() string {
	return fmt.Sprintf(`{"blocks":[{"Index":11,"Hash":"%064x","Difficulty":3,"Validator":""},{"Index":12,"Hash":"%064x","Difficulty":3,"Validator":""}]}`, 11, 12)
}

// consensusGet serves path from h and fails the test unless it answers 200.
func consensusGet(t *testing.T, h http.Handler, path string) string {
	t.Helper()

	rec := do(h, http.MethodGet, path)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d: %s", path, rec.Code, rec.Body)
	}

	return rec.Body.String()
}

func TestConsensusPage(t *testing.T) {
	t.Parallel()

	h := chainServer(t,
		routedNode(t, consensusNodeRoutes(powConsensus(blkHash, "4096", 1000), powHeaders())),
		routedNode(t, consensusNodeRoutes(powConsensus(blkHash, "4096", 1000), powHeaders())),
	)
	page := consensusGet(t, h, "/consensus")

	for _, want := range []string{
		"All reachable nodes use the same consensus configuration",
		`>node1<`, `>node2<`,
		"4096", "Initial difficulty",
		`<td class="col-num">8</td>`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("GET /consensus lacks %q", want)
		}
	}

	for _, node := range []string{"node1", "node2"} {
		if n := strings.Count(page, `href="/n/`+node+`/block/`); n != 2 {
			t.Errorf("%s: header rows = %d, want 2", node, n)
		}
	}
}

func TestConsensusPageDiffers(t *testing.T) {
	t.Parallel()

	h := chainServer(t,
		routedNode(t, consensusNodeRoutes(powConsensus(blkHash, "4096", 1000), powHeaders())),
		routedNode(t, consensusNodeRoutes(powConsensus(blkHash, "4096", 2000), powHeaders())),
	)
	page := consensusGet(t, h, "/consensus")

	for _, want := range []string{"Configuration differs", "configuration differs"} {
		if !strings.Contains(page, want) {
			t.Errorf("GET /consensus lacks %q", want)
		}
	}

	if strings.Contains(page, "All reachable nodes use the same consensus configuration") {
		t.Error("GET /consensus claims the nodes agree")
	}
}

func TestConsensusPageFork(t *testing.T) {
	t.Parallel()

	h := chainServer(t,
		routedNode(t, consensusNodeRoutes(powConsensus(blkHash, "9", 1000), powHeaders())),
		routedNode(t, consensusNodeRoutes(powConsensus(txB, "10", 1000), powHeaders())),
	)
	page := consensusGet(t, h, "/consensus")

	if !strings.Contains(page, "Nodes report different tips") {
		t.Fatal("GET /consensus lacks the fork line")
	}

	heaviest := regexp.MustCompile(`most work:([^<]*)</span>`).FindStringSubmatch(page)
	if heaviest == nil {
		t.Fatal("the fork line names no node")
	}

	if got := strings.TrimSpace(heaviest[1]); got != "node2" {
		t.Errorf("the fork line names %q, want only node2", got)
	}
}

func TestConsensusPageNodeErrors(t *testing.T) {
	t.Parallel()

	h := chainServer(t,
		routedNode(t, map[string]reply{}), // no /consensus: answers 404
		routedNode(t, map[string]reply{
			"/consensus":               ok(powConsensus(blkHash, "4096", 1000)),
			"/headers?from=3&limit=10": {http.StatusInternalServerError, `{"error":"boom"}`},
		}),
	)
	page := consensusGet(t, h, "/consensus")

	for _, want := range []string{"older than ADR-0026", "Initial difficulty", "answered with status 500"} {
		if !strings.Contains(page, want) {
			t.Errorf("GET /consensus lacks %q", want)
		}
	}
}

func TestConsensusPagePoA(t *testing.T) {
	t.Parallel()

	poa := `{"engine":"poa","height":12,"tip":"` + blkHash + `","chain_work":"2","poa":{"validators":["validatorkeyone","validatorkeytwo"]}}`
	headers := fmt.Sprintf(`{"blocks":[{"Index":12,"Hash":"%064x","Difficulty":0,"Validator":"validatorkeyone"}]}`, 12)

	h := chainServer(t, routedNode(t, consensusNodeRoutes(poa, headers)))
	page := consensusGet(t, h, "/consensus")

	for _, want := range []string{"<code>validatorkeyone</code>", "<code>validatorkeytwo</code>"} {
		if !strings.Contains(page, want) {
			t.Errorf("GET /consensus lacks %q", want)
		}
	}

	if strings.Contains(page, "Initial difficulty") {
		t.Error("GET /consensus shows PoW parameters for a PoA node")
	}
}

func TestConsensusFragment(t *testing.T) {
	t.Parallel()

	h := chainServer(t, routedNode(t, consensusNodeRoutes(powConsensus(blkHash, "4096", 1000), powHeaders())))
	body := consensusGet(t, h, "/fragments/consensus")

	if !strings.HasPrefix(body, `<section id="consensus"`) {
		t.Errorf("fragment starts with %.40q, want the consensus section", body)
	}

	if strings.Contains(body, "<html") {
		t.Error("fragment is a full page")
	}
}
