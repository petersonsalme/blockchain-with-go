package indexer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/petersonsalme/blockchain-with-go/client"
	"github.com/petersonsalme/blockchain-with-go/internal/domain"
)

// NodeClient reads a node through the generated client package.
type NodeClient struct{ api *client.ClientWithResponses }

// NewNodeClient returns a Node over the node at baseURL; opts are generated-client options
// (for example client.WithHTTPClient).
func NewNodeClient(baseURL string, opts ...client.ClientOption) (*NodeClient, error) {
	api, err := client.NewClientWithResponses(baseURL, opts...)
	if err != nil {
		return nil, err
	}

	return &NodeClient{api: api}, nil
}

// Blocks implements Node. A 410 block_pruned answer is ErrPruned.
func (c *NodeClient) Blocks(ctx context.Context, from, limit int) ([]Block, error) {
	resp, err := c.api.GetBlocksWithResponse(ctx, &client.GetBlocksParams{From: &from, Limit: &limit})
	if err != nil {
		return nil, err
	}

	if resp.StatusCode() == http.StatusGone && errorCode(resp.Body) == "block_pruned" {
		return nil, ErrPruned
	}

	if resp.JSON200 == nil {
		return nil, answerError("GET /blocks", resp.StatusCode(), resp.Body)
	}

	blocks := make([]Block, 0, len(resp.JSON200.Blocks))
	genesis := ""

	for _, b := range resp.JSON200.Blocks {
		conv, err := convert(b)
		if err != nil {
			return nil, err
		}

		switch {
		case b.Index == 0:
			genesis = conv.Hash
		case b.Index == 1 && b.PrevHash == "":
			if genesis == "" {
				genesis, err = c.genesisID(ctx)
				if err != nil {
					return nil, err
				}
			}

			conv.PrevHash = genesis
		}

		blocks = append(blocks, conv)
	}

	return blocks, nil
}

// Headers implements Node.
func (c *NodeClient) Headers(ctx context.Context, from, limit int) ([]Header, error) {
	resp, err := c.api.GetHeadersWithResponse(ctx, &client.GetHeadersParams{From: &from, Limit: &limit})
	if err != nil {
		return nil, err
	}

	if resp.JSON200 == nil {
		return nil, answerError("GET /headers", resp.StatusCode(), resp.Body)
	}

	hs := make([]Header, 0, len(resp.JSON200.Blocks))

	for _, b := range resp.JSON200.Blocks {
		h := Header{Height: b.Index, Hash: b.Hash}

		if b.Index == 0 && b.Hash == "" {
			h.Hash, err = c.genesisID(ctx)
			if err != nil {
				return nil, err
			}
		}

		hs = append(hs, h)
	}

	return hs, nil
}

// genesisID names the genesis of the node. The genesis is never sealed, so its Hash is empty
// and block 1 points at ""; the identity two nodes compare is what it commits to (the same hash
// the peer-to-peer handshake uses), recomputed here from the block the API serves.
func (c *NodeClient) genesisID(ctx context.Context) (string, error) {
	from, limit := 0, 1

	resp, err := c.api.GetBlocksWithResponse(ctx, &client.GetBlocksParams{From: &from, Limit: &limit})
	if err != nil {
		return "", err
	}

	if resp.JSON200 == nil || len(resp.JSON200.Blocks) != 1 || resp.JSON200.Blocks[0].Index != 0 {
		return "", answerError("GET /blocks", resp.StatusCode(), resp.Body)
	}

	conv, err := convert(resp.JSON200.Blocks[0])
	if err != nil {
		return "", err
	}

	return conv.Hash, nil
}

func convert(b client.Block) (Block, error) {
	out := Block{Height: b.Index, Hash: b.Hash, PrevHash: b.PrevHash, Time: b.Timestamp}

	var (
		committed []domain.Transaction
		txs       []client.Transaction
	)

	if b.Transactions != nil {
		txs = *b.Transactions
	}

	for _, t := range txs {
		if t.ID == nil || *t.ID == "" {
			return Block{}, fmt.Errorf("block %d: transaction without ID", b.Index)
		}

		tx := Tx{ID: *t.ID}
		dtx := domain.Transaction{ID: *t.ID, Timestamp: t.Timestamp}

		if t.Inputs != nil {
			for _, in := range *t.Inputs {
				tx.Inputs = append(tx.Inputs, OutRef{TxID: in.PrevTxID, Index: in.OutIndex})
				dtx.Inputs = append(dtx.Inputs, domain.TxIn{PrevTxID: in.PrevTxID, OutIndex: in.OutIndex, PublicKey: in.PublicKey, Signature: in.Signature})
			}
		}

		for _, o := range t.Outputs {
			tx.Outputs = append(tx.Outputs, Out{Address: o.Address, Amount: o.Amount})
			dtx.Outputs = append(dtx.Outputs, domain.TxOut{Amount: o.Amount, Address: o.Address})
		}

		out.Txs = append(out.Txs, tx)
		committed = append(committed, dtx)
	}

	if b.Index == 0 && b.Hash == "" {
		out.Hash = (&domain.Block{
			Index: 0, Timestamp: b.Timestamp, MerkleRoot: domain.MerkleRoot(committed), PrevHash: b.PrevHash,
			Nonce: b.Nonce, Difficulty: b.Difficulty, Validator: b.Validator,
		}).CalculateHash()
	}

	return out, nil
}

func errorCode(body []byte) string {
	var e client.ErrorResponse

	if json.Unmarshal(body, &e) != nil {
		return ""
	}

	if e.Code == nil {
		return ""
	}

	return *e.Code
}

func answerError(route string, status int, body []byte) error {
	var e client.ErrorResponse
	if json.Unmarshal(body, &e) == nil && e.Error != "" {
		return fmt.Errorf("%s: node answered %d: %s", route, status, e.Error)
	}

	return errors.New(route + ": node answered " + http.StatusText(status))
}
