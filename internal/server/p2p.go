package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/petersonsalme/blockchain-with-go/internal/p2p"
)

// p2pDisabledCode is the error code of the /p2p routes when the node has no peer-to-peer layer.
const p2pDisabledCode = "p2p_disabled"

// helloRefusals maps the handshake refusals of ADR-0013 to their HTTP status and code.
var helloRefusals = []struct {
	err    error
	status int
	code   string
}{
	{p2p.ErrBadHello, http.StatusBadRequest, "invalid_hello"},
	{p2p.ErrIncompatibleVersion, http.StatusConflict, "incompatible_version"},
	{p2p.ErrGenesisMismatch, http.StatusConflict, "genesis_mismatch"},
	{p2p.ErrSelf, http.StatusConflict, "self_connection"},
	{p2p.ErrPeerLimit, http.StatusServiceUnavailable, "peer_limit"},
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

// handleP2PHello runs the handshake for an inbound hello and answers the local hello.
func (s *Server) handleP2PHello(w http.ResponseWriter, r *http.Request) {
	defer func() { _ = r.Body.Close() }() // closing a server request body cannot fail usefully
	if !s.p2pEnabled(w) {
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes())
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	var remote p2p.Hello
	err := dec.Decode(&remote)
	if err == nil { // only whitespace may follow the hello
		if _, tokErr := dec.Token(); !errors.Is(tokErr, io.EOF) {
			err = errors.Join(errors.New("trailing data"), tokErr)
		}
	}
	if err != nil {
		if !respondIfTooLarge(w, err) {
			respondWithJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid hello body", Code: "invalid_hello"})
		}
		return
	}

	local, err := s.p2p.Accept(remote)
	if err != nil {
		for _, m := range helloRefusals {
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
