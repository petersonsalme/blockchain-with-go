package dashboard

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// maxQueryLen bounds what /search will look at.
const maxQueryLen = 128

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

	// txItem is a transaction with the node name its links need.
	txItem struct {
		Node string
		Tx   nodeTx
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

	blockView struct {
		Node  string
		Block nodeBlock
		Txs   []txItem
	}

	txView struct {
		Node      string
		Item      txItem
		BlockHash string
		Height    int
		Proof     *nodeProof
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

// screen is one page: how to load its data from one node (or all, for Peers) and the template
// that renders it. The same template is the polled fragment.
type screen struct {
	body string
	load func(r *http.Request, node Node) (any, *pageError)
}

// nodeScreen serves a screen of the node in the path, as a full page or, when fragment, as the
// bare template for htmx. The node is looked up by name before anything else happens.
func (s *server) nodeScreen(sc screen, fragment bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		node, ok := s.lookup(r.PathValue("node"))
		if !ok {
			s.fail(w, "", noSuchNode())

			return
		}

		data, perr := sc.load(r, node)
		if perr != nil {
			s.fail(w, node.Name, perr)

			return
		}

		s.show(w, node.Name, sc.body, data, fragment)
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

func (s *server) show(w http.ResponseWriter, current, body string, data any, fragment bool) {
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

	s.layout(w, http.StatusOK, current, buf.String())
}

func (s *server) fail(w http.ResponseWriter, current string, perr *pageError) {
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

	s.layout(w, perr.Status, current, buf.String())
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

		s.show(w, "", "peers", view, fragment)
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

func (s *server) loadMempool(r *http.Request, node Node) (any, *pageError) {
	var body struct {
		Transactions []nodeTx `json:"transactions"`
	}

	if perr := s.read(r.Context(), node, Mempool(), &body); perr != nil {
		return nil, perr
	}

	return mempoolView{Node: node.Name, Txs: items(node.Name, body.Transactions)}, nil
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
		return nil, perr
	}

	view := txView{Node: node.Name, Item: txItem{node.Name, body.Transaction}, BlockHash: body.BlockHash, Height: body.Height}

	var proof nodeProof

	if perr := s.read(r.Context(), node, TxProof(id), &proof); perr == nil {
		view.Proof = &proof
	}

	return view, nil
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
		s.fail(w, "", noSuchNode())

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
			s.fail(w, node.Name, perr)

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

	s.layout(w, status, node.Name, buf.String())
}
