package dashboard

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/bits"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// historyLimit is the page size of the address history, the node default.
const historyLimit = 25

// blocksLimit is the page size of the Blocks screen, the node default.
const blocksLimit = 20

// homeBlocks and homeTxs bound the latest activity on a node page.
const (
	homeBlocks = 10
	homeTxs    = 10
)

// maxQueryLen bounds what /search will look at.
const maxQueryLen = 128

// maxFeeReads bounds how many spent transactions the fee of one page reads.
const maxFeeReads = 16

// The node's JSON, as far as the screens show it. Field names without tags match the node's Go
// field names the way encoding/json always does.
type (
	nodeTxIn struct {
		PrevTxID  string
		OutIndex  uint32
		PublicKey string
		Signature string
	}

	nodeTxOut struct {
		Amount  uint64
		Address string
	}

	nodeContractOp struct {
		Kind     string
		Code     string
		Target   string
		Args     []uint64
		GasLimit uint64
	}

	nodeTx struct {
		ID        string
		Inputs    []nodeTxIn
		Outputs   []nodeTxOut
		Timestamp time.Time
		Contract  *nodeContractOp
	}

	nodeBlock struct {
		Index        int
		Timestamp    time.Time
		Transactions []nodeTx
		Hash         string
		PrevHash     string
		Nonce        uint64
		Difficulty   uint64
		Validator    string
		Signature    string
	}

	nodePeer struct {
		NodeID   string    `json:"node_id"`
		Addr     string    `json:"addr"`
		Height   int       `json:"height"`
		TipHash  string    `json:"tip_hash"`
		Inbound  bool      `json:"inbound"`
		LastSeen time.Time `json:"last_seen"`
	}

	proofStep struct {
		Hash string `json:"hash"`
		Side string `json:"side"`
	}

	nodeProof struct {
		MerkleRoot string      `json:"merkle_root"`
		LeafIndex  int         `json:"leaf_index"`
		LeafHash   string      `json:"leaf_hash"`
		Steps      []proofStep `json:"proof"`
	}

	nodeUTXO struct {
		TxID   string `json:"txid"`
		Index  uint32 `json:"index"`
		Amount uint64 `json:"amount"`
	}

	nodeAddressTx struct {
		Transaction nodeTx    `json:"transaction"`
		BlockHash   string    `json:"block_hash"`
		Height      int       `json:"height"`
		Timestamp   time.Time `json:"timestamp"`
	}

	// nodeEntry is one item of GET /mempool entries: the fee and size of the transaction at the same index.
	nodeEntry struct {
		ID   string `json:"id"`
		Fee  uint64 `json:"fee"`
		Size int    `json:"size"`
	}
)

// Screen data. Node is the name from the configuration, never from the URL or a node.
type (
	peersNode struct {
		Name     string
		Err      string
		Disabled bool
		Peers    []nodePeer
		Addrs    []string
	}

	peersView struct{ Nodes []peersNode }

	// poolEntry is the fee and fee rate a mempool row shows.
	poolEntry struct {
		Fee  uint64
		Rate string
	}

	// txItem is a transaction with the node name its links need. Pos is its 1-based place in the
	// mempool selection order; 0 means none is shown.
	txItem struct {
		Node  string
		Tx    nodeTx
		Pos   int
		Entry *poolEntry // fee and fee rate from the node's pool entry; nil shows neither
	}

	mempoolView struct {
		Node string
		Txs  []txItem
	}

	blocksView struct {
		Node   string
		From   int
		Blocks []nodeBlock
		Next   *int
		Poll   bool
	}

	// homeTx is one transaction of the node page, with the height of its block.
	homeTx struct {
		Node   string
		Height int
		Tx     nodeTx
	}

	// homeView is the latest activity on a node page. Msg replaces both lists when they were not read.
	homeView struct {
		Blocks []nodeBlock // newest first
		Txs    []homeTx    // newest block first, block order inside a block, at most homeTxs
		Msg    string
	}

	blockView struct {
		Node  string
		Block nodeBlock
		Txs   []txItem
	}

	// A pending view is a mempool transaction: Position of PoolSize in the selection order, no block.
	txView struct {
		Node      string
		Item      txItem
		BlockHash string
		Height    int
		Proof     *nodeProof
		Pending   bool
		Position  int
		PoolSize  int
		Fee       uint64
		FeeMsg    string // why Fee is not shown; empty when it is
	}

	addressView struct {
		Node        string
		Addr        string
		Balance     uint64
		UTXOs       []nodeUTXO
		Txs         []nodeAddressTx
		Next        string
		After       string
		Restarted   bool
		PrunedBelow int
	}

	storageCell struct {
		Key   string
		Value uint64
	}

	contractView struct {
		Node    string
		ID      string
		Code    string
		Storage []storageCell
	}

	searchView struct {
		Node  string
		Query string
		Miss  bool
	}
)

// pageError is a failure shown as a dashboard page; Message never repeats user input or node text.
type pageError struct {
	Status   int
	Message  string
	Upstream int // status the node answered, when it answered
}

func noSuchNode() *pageError {
	return &pageError{Status: http.StatusNotFound, Message: "no such node"}
}

func badRequest(what string) *pageError {
	return &pageError{Status: http.StatusBadRequest, Message: "invalid " + what}
}

// read asks node for req and decodes the JSON answer into into. A node 404 is a dashboard 404; any
// other failure is a 502 naming the node.
func (s *server) read(ctx context.Context, node Node, req Request, into any) *pageError {
	body, err := s.fetch.Get(ctx, node, req)
	if err != nil {
		return nodeFailure(node, err)
	}

	err = json.Unmarshal(body, into)
	if err != nil {
		return &pageError{Status: http.StatusBadGateway, Message: fmt.Sprintf("node %s sent an answer the dashboard cannot read", node.Name)}
	}

	return nil
}

func nodeFailure(node Node, err error) *pageError {
	var st *StatusError

	if !errors.As(err, &st) {
		return &pageError{Status: http.StatusBadGateway, Message: fmt.Sprintf("node %s is unreachable", node.Name)}
	}

	perr := &pageError{Status: http.StatusBadGateway, Upstream: st.Code}

	switch st.Code {
	case http.StatusNotFound:
		perr.Status, perr.Message = http.StatusNotFound, fmt.Sprintf("node %s does not have it", node.Name)
	case http.StatusGone:
		perr.Status, perr.Message = http.StatusGone, fmt.Sprintf("node %s pruned that block; only its header is kept", node.Name)
	case http.StatusBadRequest:
		perr.Status, perr.Message = http.StatusBadRequest, fmt.Sprintf("node %s rejected the request", node.Name)
	default:
		perr.Message = fmt.Sprintf("node %s answered with status %d", node.Name, st.Code)
	}

	return perr
}

// notFoundCrumb is the breadcrumb of a page for a node that is not in the configuration.
const notFoundCrumb = "Not found"

// screen is one page: how to load its data from one node (or all, for Peers) and the template
// that renders it. The same template is the polled fragment.
type screen struct {
	body  string
	page  string                       // key of the node tab that is this screen; blank for none
	crumb func(r *http.Request) string // last breadcrumb label
	load  func(r *http.Request, node Node) (any, *pageError)
}

// label is a breadcrumb that does not depend on the request.
func label(text string) func(*http.Request) string {
	return func(*http.Request) string { return text }
}

// nodeScreen serves a screen of the node in the path, as a full page or, when fragment, as the
// bare template for htmx. The node is looked up by name before anything else happens.
func (s *server) nodeScreen(sc screen, fragment bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		node, ok := s.lookup(r.PathValue("node"))
		if !ok {
			s.fail(w, place{Crumb: notFoundCrumb}, noSuchNode())

			return
		}

		at := place{Node: node.Name, Page: sc.page, Crumb: sc.crumb(r)}

		data, perr := sc.load(r, node)
		if perr != nil {
			s.fail(w, at, perr)

			return
		}

		s.show(w, at, sc.body, data, fragment)
	}
}

func (s *server) lookup(name string) (Node, bool) {
	for _, n := range s.nodes {
		if n.Name == name {
			return n, true
		}
	}

	return Node{}, false
}

func (s *server) show(w http.ResponseWriter, at place, body string, data any, fragment bool) {
	var buf bytes.Buffer

	// A full page may wrap the fragment with a header that htmx must not replace (the help tooltip).
	if !fragment && s.tmpl.Lookup(body+"page") != nil {
		body += "page"
	}

	err := s.tmpl.ExecuteTemplate(&buf, body, data)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if fragment {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(buf.Bytes())

		return
	}

	s.layout(w, http.StatusOK, at, buf.String())
}

func (s *server) fail(w http.ResponseWriter, at place, perr *pageError) {
	var buf bytes.Buffer

	err := s.tmpl.ExecuteTemplate(&buf, "error", struct {
		Status  int
		Title   string
		Message string
	}{perr.Status, http.StatusText(perr.Status), perr.Message})
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	s.layout(w, perr.Status, at, buf.String())
}

func (s *server) peersScreen(fragment bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		view := peersView{Nodes: make([]peersNode, len(s.nodes))}

		var wg sync.WaitGroup

		ctx := r.Context()

		for i, node := range s.nodes {
			wg.Go(func() { view.Nodes[i] = s.peersOf(ctx, node) })
		}

		wg.Wait()

		s.show(w, place{Page: "peers", Crumb: "Peers"}, "peers", view, fragment)
	}
}

func (s *server) peersOf(ctx context.Context, node Node) peersNode {
	out := peersNode{Name: node.Name}

	var body struct {
		Peers []nodePeer `json:"peers"`
		Addrs []string   `json:"addrs"`
	}

	perr := s.read(ctx, node, Peers(), &body)
	if perr != nil {
		if perr.Status == http.StatusNotFound {
			out.Disabled = true
		} else {
			out.Err = perr.Message
		}

		return out
	}

	out.Peers, out.Addrs = body.Peers, body.Addrs

	return out
}

func items(node string, txs []nodeTx) []txItem {
	out := make([]txItem, len(txs))
	for i, tx := range txs {
		out[i] = txItem{Node: node, Tx: tx}
	}

	return out
}

// poolEntries pairs each pool transaction with its entry: same index, same id, positive size.
// A length mismatch (an older node without entries) pairs none.
func poolEntries(txs []nodeTx, entries []nodeEntry) []*poolEntry {
	out := make([]*poolEntry, len(txs))
	if len(entries) != len(txs) {
		return out
	}

	for i, tx := range txs {
		if entries[i].ID != tx.ID {
			continue
		}

		rate, ok := feeRate(entries[i].Fee, entries[i].Size)
		if !ok {
			continue
		}

		out[i] = &poolEntry{Fee: entries[i].Fee, Rate: rate}
	}

	return out
}

func (s *server) loadMempool(r *http.Request, node Node) (any, *pageError) {
	var body struct {
		Transactions []nodeTx    `json:"transactions"`
		Entries      []nodeEntry `json:"entries"`
	}

	if perr := s.read(r.Context(), node, Mempool(), &body); perr != nil {
		return nil, perr
	}

	pairs := poolEntries(body.Transactions, body.Entries)

	txs := items(node.Name, body.Transactions)
	for i := range txs {
		txs[i].Pos = i + 1
		txs[i].Entry = pairs[i]
	}

	return mempoolView{Node: node.Name, Txs: txs}, nil
}

func (s *server) loadBlocks(r *http.Request, node Node) (any, *pageError) {
	from := 0

	if raw := r.URL.Query().Get("from"); raw != "" {
		if !isDecimal(raw) {
			return nil, badRequest("page start")
		}

		from, _ = strconv.Atoi(raw)
	}

	var body struct {
		Blocks []nodeBlock `json:"blocks"`
		Next   *int        `json:"next"`
	}

	if perr := s.read(r.Context(), node, Blocks(from, blocksLimit), &body); perr != nil {
		return nil, perr
	}

	return blocksView{Node: node.Name, From: from, Blocks: body.Blocks, Next: body.Next, Poll: from == 0}, nil
}

// loadHome reads the homeBlocks blocks ending at height, the height the poller last saw. A failed
// read is shown in place of the lists; the rest of the node page does not depend on it.
func (s *server) loadHome(ctx context.Context, node Node, height int) homeView {
	var body struct {
		Blocks []nodeBlock `json:"blocks"`
	}

	if perr := s.read(ctx, node, Blocks(max(height-homeBlocks+1, 0), homeBlocks), &body); perr != nil {
		return homeView{Msg: "Latest activity not available: " + perr.Message + "."}
	}

	slices.Reverse(body.Blocks)

	var txs []homeTx

outer:
	for _, block := range body.Blocks {
		for _, tx := range block.Transactions {
			if len(txs) == homeTxs {
				break outer
			}

			txs = append(txs, homeTx{Node: node.Name, Height: block.Index, Tx: tx})
		}
	}

	return homeView{Blocks: body.Blocks, Txs: txs}
}

func (s *server) loadBlock(r *http.Request, node Node) (any, *pageError) {
	ref := r.PathValue("ref")
	if !isDecimal(ref) && !isLowerHex(ref, hexIDLen) {
		return nil, badRequest("block reference")
	}

	var block nodeBlock

	if perr := s.read(r.Context(), node, Block(ref), &block); perr != nil {
		return nil, perr
	}

	return blockView{Node: node.Name, Block: block, Txs: items(node.Name, block.Transactions)}, nil
}

func (s *server) loadTx(r *http.Request, node Node) (any, *pageError) {
	id := r.PathValue("id")
	if !isLowerHex(id, hexIDLen) {
		return nil, badRequest("transaction id")
	}

	var body struct {
		Transaction nodeTx `json:"transaction"`
		BlockHash   string `json:"block_hash"`
		Height      int    `json:"height"`
	}

	if perr := s.read(r.Context(), node, Tx(id), &body); perr != nil {
		if perr.Upstream != http.StatusNotFound {
			return nil, perr
		}

		// Not in a block: a transaction the node's pool holds is pending, at its selection position.
		var pool struct {
			Transactions []nodeTx `json:"transactions"`
		}

		if p := s.read(r.Context(), node, Mempool(), &pool); p != nil {
			return nil, p
		}

		for i, tx := range pool.Transactions {
			if tx.ID == id {
				fee, msg := s.txFee(r.Context(), node, tx, pool.Transactions)

				return txView{
					Node:     node.Name,
					Item:     txItem{Node: node.Name, Tx: tx},
					Pending:  true,
					Position: i + 1,
					PoolSize: len(pool.Transactions),
					Fee:      fee,
					FeeMsg:   msg,
				}, nil
			}
		}

		return nil, perr
	}

	view := txView{Node: node.Name, Item: txItem{Node: node.Name, Tx: body.Transaction}, BlockHash: body.BlockHash, Height: body.Height}
	view.Fee, view.FeeMsg = s.txFee(r.Context(), node, body.Transaction, nil)

	var proof nodeProof

	if perr := s.read(r.Context(), node, TxProof(id), &proof); perr == nil {
		view.Proof = &proof
	}

	return view, nil
}

// txFee returns the fee of tx, or a message saying why there is none. The fee is the outputs tx
// spends minus its own outputs. pool is the decoded mempool (nil for a confirmed tx): a spent
// transaction the node does not have in a block may be pending there.
func (s *server) txFee(ctx context.Context, node Node, tx nodeTx, pool []nodeTx) (uint64, string) {
	const unknown = "Fee unknown: a spent output could not be read."

	if len(tx.Inputs) == 0 {
		return 0, "No fee (coinbase or genesis)."
	}

	var ids []string

	for _, in := range tx.Inputs {
		if !slices.Contains(ids, in.PrevTxID) {
			ids = append(ids, in.PrevTxID)
		}
	}

	if len(ids) > maxFeeReads {
		return 0, fmt.Sprintf("Fee not computed: it spends more than %d transactions.", maxFeeReads)
	}

	spent := make(map[string]nodeTx, len(ids))

	for _, id := range ids {
		var body struct {
			Transaction nodeTx `json:"transaction"`
		}

		perr := s.read(ctx, node, Tx(id), &body)
		if perr != nil {
			if perr.Upstream != http.StatusNotFound || pool == nil {
				return 0, unknown
			}

			i := slices.IndexFunc(pool, func(p nodeTx) bool { return p.ID == id })
			if i < 0 {
				return 0, unknown
			}

			body.Transaction = pool[i]
		}

		spent[id] = body.Transaction
	}

	var in, out, carry uint64

	for _, input := range tx.Inputs {
		prev := spent[input.PrevTxID]
		if uint64(input.OutIndex) >= uint64(len(prev.Outputs)) {
			return 0, unknown
		}

		in, carry = bits.Add64(in, prev.Outputs[input.OutIndex].Amount, 0)
		if carry != 0 {
			return 0, unknown
		}
	}

	for _, o := range tx.Outputs {
		out, carry = bits.Add64(out, o.Amount, 0)
		if carry != 0 {
			return 0, unknown
		}
	}

	if in < out {
		return 0, unknown
	}

	return in - out, ""
}

func (s *server) loadAddress(r *http.Request, node Node) (any, *pageError) {
	addr := r.PathValue("addr")
	if !isLowerHex(addr, addressLen) {
		return nil, badRequest("address")
	}

	after := r.URL.Query().Get("after")
	if after != "" && !isLowerHex(after, hexIDLen) {
		return nil, badRequest("page cursor")
	}

	var balance struct {
		Balance uint64     `json:"balance"`
		UTXOs   []nodeUTXO `json:"utxos"`
	}

	if perr := s.read(r.Context(), node, Balance(addr), &balance); perr != nil {
		return nil, perr
	}

	view := addressView{Node: node.Name, Addr: addr, Balance: balance.Balance, UTXOs: balance.UTXOs, After: after}

	var history struct {
		Txs         []nodeAddressTx `json:"txs"`
		Next        string          `json:"next"`
		PrunedBelow int             `json:"pruned_below"`
	}

	perr := s.read(r.Context(), node, AddressTxs(addr, historyLimit, after), &history)
	if perr != nil && after != "" && perr.Upstream == http.StatusUnprocessableEntity {
		view.After, view.Restarted = "", true
		history.Txs, history.Next = nil, ""
		perr = s.read(r.Context(), node, AddressTxs(addr, historyLimit, ""), &history)
	}

	if perr != nil {
		return nil, perr
	}

	view.Txs, view.Next, view.PrunedBelow = history.Txs, history.Next, history.PrunedBelow

	return view, nil
}

func (s *server) loadContract(r *http.Request, node Node) (any, *pageError) {
	id := r.PathValue("id")
	if !isLowerHex(id, hexIDLen) {
		return nil, badRequest("contract id")
	}

	var body struct {
		ID      string            `json:"id"`
		Code    string            `json:"code"`
		Storage map[string]uint64 `json:"storage"`
	}

	if perr := s.read(r.Context(), node, Contract(id), &body); perr != nil {
		return nil, perr
	}

	cells := make([]storageCell, 0, len(body.Storage))
	for k, v := range body.Storage {
		cells = append(cells, storageCell{Key: k, Value: v})
	}

	// Keys are decimal strings: shorter is smaller, then text order.
	slices.SortFunc(cells, func(a, b storageCell) int {
		return cmp.Or(cmp.Compare(len(a.Key), len(b.Key)), strings.Compare(a.Key, b.Key))
	})

	return contractView{Node: node.Name, ID: body.ID, Code: body.Code, Storage: cells}, nil
}

// searchProbe is one candidate of /search: the read that confirms it and the path it links to.
type searchProbe struct {
	req  Request
	path string
	// badRequestMiss: the node answers 400 for an address with a bad checksum, which is a miss too.
	badRequestMiss bool
}

// searchProbes lists the candidates for q, already lowercase and trimmed, in the order /search
// tries them. A query that matches no shape has none.
func searchProbes(q string) []searchProbe {
	switch {
	case isDecimal(q):
		return []searchProbe{{req: Block(q), path: "/block/" + q}}
	case isLowerHex(q, hexIDLen):
		return []searchProbe{{req: Tx(q), path: "/tx/" + q}, {req: Block(q), path: "/block/" + q}}
	case isLowerHex(q, addressLen):
		return []searchProbe{{req: Balance(q), path: "/address/" + q, badRequestMiss: true}}
	}

	return nil
}

// search redirects to the first candidate of the query's shape that the node answers 200 for: a
// decimal height to its block, 64 hex to the transaction and then the block, 50 hex to the address.
// A 404 from the node, or a 400 for an address, is a miss and the next candidate is tried. When none
// answers, or the query matches no shape, the search page says Not found (404); an empty query, or
// one over maxQueryLen, gets the plain page. Any other node failure is a page. The Location is built
// here from the node name in the configuration and the text the allowlist just accepted.
func (s *server) search(w http.ResponseWriter, r *http.Request) {
	node, ok := s.lookup(r.URL.Query().Get("node"))
	if !ok {
		s.fail(w, place{Crumb: notFoundCrumb}, noSuchNode())

		return
	}

	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	if len(q) > maxQueryLen {
		q = ""
	}

	base := "/n/" + url.PathEscape(node.Name)

	for _, p := range searchProbes(q) {
		_, err := s.fetch.Get(r.Context(), node, p.req)
		if err == nil {
			http.Redirect(w, r, base+p.path, http.StatusSeeOther)

			return
		}

		perr := nodeFailure(node, err)
		if perr.Status != http.StatusNotFound && (!p.badRequestMiss || perr.Status != http.StatusBadRequest) {
			s.fail(w, place{Node: node.Name, Crumb: "Search"}, perr)

			return
		}
	}

	status := http.StatusOK
	if q != "" {
		status = http.StatusNotFound
	}

	var buf bytes.Buffer

	err := s.tmpl.ExecuteTemplate(&buf, "search", searchView{Node: node.Name, Query: q, Miss: q != ""})
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	s.layout(w, status, place{Node: node.Name, Crumb: "Search"}, buf.String())
}
