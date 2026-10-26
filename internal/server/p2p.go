package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/petersonsalme/blockchain-with-go/internal/domain"
	"github.com/petersonsalme/blockchain-with-go/internal/p2p"
)

// p2pDisabledCode is the error code of the /p2p routes when the node has no peer-to-peer layer.
const p2pDisabledCode = "p2p_disabled"

// helloRefusals maps the handshake refusals of ADR-0013 to their HTTP status and code.
func helloRefusals() []errorRule {
	return []errorRule{
		{p2p.ErrBadHello, http.StatusBadRequest, "invalid_hello"},
		{p2p.ErrIncompatibleVersion, http.StatusConflict, "incompatible_version"},
		{p2p.ErrGenesisMismatch, http.StatusConflict, "genesis_mismatch"},
		{p2p.ErrSelf, http.StatusConflict, "self_connection"},
		{p2p.ErrBanned, http.StatusForbidden, "peer_banned"},
		{p2p.ErrPeerLimit, http.StatusServiceUnavailable, "peer_limit"},
	}
}

// peerResponse is one connected peer in GET /p2p/peers.
type peerResponse struct {
	NodeID   string `json:"node_id"`
	Addr     string `json:"addr"`
	Height   int    `json:"height"`
	TipHash  string `json:"tip_hash"`
	Inbound  bool   `json:"inbound"`
	LastSeen string `json:"last_seen"`
}

// peersResponse is the body of GET /p2p/peers.
type peersResponse struct {
	Peers []peerResponse `json:"peers"`
	Addrs []string       `json:"addrs"`
}

// p2pEnabled answers 404 p2p_disabled and reports false when the node has no peer layer.
func (s *Server) p2pEnabled(w http.ResponseWriter) bool {
	if s.p2p == nil {
		respondWithJSON(w, http.StatusNotFound, errorResponse{Error: "peer-to-peer is not enabled on this node", Code: p2pDisabledCode})
		return false
	}

	return true
}

// decodeP2PBody decodes exactly one JSON value with no unknown field into v, bounded by
// maxRequestBodyBytes. On failure it answers 413, or 400 with msg and code, and reports false.
func decodeP2PBody(w http.ResponseWriter, r *http.Request, v any, msg, code string) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes())
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	err := dec.Decode(v)
	if err == nil { // only whitespace may follow the value
		_, tokErr := dec.Token()
		if !errors.Is(tokErr, io.EOF) {
			err = errors.Join(errors.New("trailing data"), tokErr)
		}
	}

	if err != nil {
		if !respondIfTooLarge(w, err) {
			respondWithJSON(w, http.StatusBadRequest, errorResponse{Error: msg, Code: code})
		}

		return false
	}

	return true
}

// handleP2PHello runs the handshake for an inbound hello and answers the local hello.
func (s *Server) handleP2PHello(w http.ResponseWriter, r *http.Request) {
	defer func() { _ = r.Body.Close() }() // closing a server request body cannot fail usefully

	if !s.p2pEnabled(w) {
		return
	}

	var remote p2p.Hello
	if !decodeP2PBody(w, r, &remote, "invalid hello body", "invalid_hello") {
		return
	}

	local, err := s.p2p.Accept(remote)
	if err != nil {
		for _, m := range helloRefusals() {
			if errors.Is(err, m.err) {
				respondWithJSON(w, m.status, errorResponse{Error: err.Error(), Code: m.code})
				return
			}
		}

		respondWithJSON(w, http.StatusInternalServerError, errorResponse{Error: err.Error()})

		return
	}

	respondWithJSON(w, http.StatusOK, local)
}

// handleP2PPeers lists the connected peers and the known addresses, in the order the port returns them.
func (s *Server) handleP2PPeers(w http.ResponseWriter, _ *http.Request) {
	if !s.p2pEnabled(w) {
		return
	}

	infos := s.p2p.Peers()

	resp := peersResponse{Peers: make([]peerResponse, len(infos)), Addrs: append([]string{}, s.p2p.Addrs()...)}
	for i, p := range infos {
		resp.Peers[i] = peerResponse{
			NodeID:   p.NodeID,
			Addr:     p.Addr,
			Height:   p.Height,
			TipHash:  p.TipHash,
			Inbound:  p.Inbound,
			LastSeen: p.LastSeen.UTC().Format(time.RFC3339),
		}
	}

	respondWithJSON(w, http.StatusOK, resp)
}

// invRequest is the body of POST /p2p/inv.
type invRequest struct {
	NodeID string        `json:"node_id"`
	Items  []p2p.InvItem `json:"items"`
}

// wantResponse is the body of POST /p2p/inv: the items the node wants, in request order.
type wantResponse struct {
	Want []p2p.InvItem `json:"want"`
}

// blockGossipRequest is the body of POST /p2p/block.
type blockGossipRequest struct {
	NodeID string        `json:"node_id"`
	Block  *domain.Block `json:"block"`
}

// txGossipRequest is the body of POST /p2p/tx.
type txGossipRequest struct {
	NodeID string             `json:"node_id"`
	Tx     domain.Transaction `json:"tx"`
}

// gossipResponse is the body of POST /p2p/block and POST /p2p/tx: "accepted" or "known".
type gossipResponse struct {
	Status string `json:"status"`
}

// respondWithGossipError maps the gossip errors of ADR-0018 to their status and code;
// invalidCode is the 422 code of the object kind.
func respondWithGossipError(w http.ResponseWriter, err error, invalidCode string) {
	rules := []errorRule{
		{p2p.ErrBadInv, http.StatusBadRequest, "invalid_inv"},
		{p2p.ErrUnknownPeer, http.StatusForbidden, "unknown_peer"},
		{p2p.ErrNotOnTip, http.StatusConflict, "not_on_tip"},
		{p2p.ErrInvalidObject, http.StatusUnprocessableEntity, invalidCode},
		{p2p.ErrGossipDisabled, http.StatusNotFound, p2pDisabledCode},
	}
	for _, m := range rules {
		if errors.Is(err, m.err) {
			respondWithJSON(w, m.status, errorResponse{Error: err.Error(), Code: m.code})
			return
		}
	}

	respondWithJSON(w, http.StatusInternalServerError, errorResponse{Error: err.Error()})
}

// respondWithGossipStatus answers 200 "accepted" when the object was new and "known" otherwise.
func respondWithGossipStatus(w http.ResponseWriter, accepted bool) {
	status := "known"
	if accepted {
		status = "accepted"
	}

	respondWithJSON(w, http.StatusOK, gossipResponse{Status: status})
}

// handleP2PInv answers an inv with the items the node wants.
func (s *Server) handleP2PInv(w http.ResponseWriter, r *http.Request) {
	defer func() { _ = r.Body.Close() }() // closing a server request body cannot fail usefully

	if !s.p2pEnabled(w) {
		return
	}

	var req invRequest
	if !decodeP2PBody(w, r, &req, "invalid inv body", "invalid_inv") {
		return
	}

	want, err := s.p2p.Inv(req.NodeID, req.Items)
	if err != nil {
		respondWithGossipError(w, err, "")
		return
	}

	if want == nil {
		want = []p2p.InvItem{}
	}

	respondWithJSON(w, http.StatusOK, wantResponse{Want: want})
}

// handleP2PBlock hands a gossiped block to the peer layer.
func (s *Server) handleP2PBlock(w http.ResponseWriter, r *http.Request) {
	defer func() { _ = r.Body.Close() }() // closing a server request body cannot fail usefully

	if !s.p2pEnabled(w) {
		return
	}

	var req blockGossipRequest
	if !decodeP2PBody(w, r, &req, "invalid block body", "invalid_body") {
		return
	}

	accepted, err := s.p2p.ReceiveBlock(r.Context(), req.NodeID, req.Block)
	if err != nil {
		respondWithGossipError(w, err, "invalid_block")
		return
	}

	respondWithGossipStatus(w, accepted)
}

// handleP2PTx hands a gossiped transaction to the peer layer.
func (s *Server) handleP2PTx(w http.ResponseWriter, r *http.Request) {
	defer func() { _ = r.Body.Close() }() // closing a server request body cannot fail usefully

	if !s.p2pEnabled(w) {
		return
	}

	var req txGossipRequest
	if !decodeP2PBody(w, r, &req, "invalid tx body", "invalid_body") {
		return
	}

	accepted, err := s.p2p.ReceiveTx(req.NodeID, req.Tx)
	if err != nil {
		respondWithGossipError(w, err, "invalid_tx")
		return
	}

	respondWithGossipStatus(w, accepted)
}
