// Package node is a thin JSON-RPC 2.0 client for the peercash (go-ethereum
// fork) node. It exposes only the remote-sealer RPCs the bridge needs:
// eth_getWork, eth_submitWork and eth_blockNumber. See peercash-chain
// eth/api_mining.go for the server side.
package node

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// Work is one eth_getWork result: [sealHash, seedHash, target], each a
// 0x-prefixed 32-byte hex string. target is big-endian with the 8-byte value
// (maxUint64/difficulty) in its last 8 bytes.
type Work struct {
	SealHash string
	SeedHash string
	Target   string
}

// Client talks JSON-RPC 2.0 to the node over HTTP.
type Client struct {
	url  string
	http *http.Client
	id   atomic.Uint64
}

// New returns a Client for the given node HTTP endpoint (e.g.
// http://127.0.0.1:8545).
func New(url string) *Client {
	return &Client{
		url:  url,
		http: &http.Client{Timeout: 10 * time.Second},
	}
}

type rpcRequest struct {
	JSONRPC string        `json:"jsonrpc"`
	ID      uint64        `json:"id"`
	Method  string        `json:"method"`
	Params  []interface{} `json:"params"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

func (c *Client) call(ctx context.Context, method string, params []interface{}, out interface{}) error {
	body, err := json.Marshal(rpcRequest{JSONRPC: "2.0", ID: c.id.Add(1), Method: method, Params: params})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var rr rpcResponse
	if err := json.NewDecoder(resp.Body).Decode(&rr); err != nil {
		return fmt.Errorf("decode %s response: %w", method, err)
	}
	if rr.Error != nil {
		return fmt.Errorf("%s: rpc error %d: %s", method, rr.Error.Code, rr.Error.Message)
	}
	if out != nil {
		if err := json.Unmarshal(rr.Result, out); err != nil {
			return fmt.Errorf("unmarshal %s result: %w", method, err)
		}
	}
	return nil
}

// GetWork fetches the current work item (eth_getWork). The node credits the
// block reward to its own configured --miner.etherbase.
func (c *Client) GetWork(ctx context.Context) (Work, error) {
	var raw [3]string
	if err := c.call(ctx, "eth_getWork", []interface{}{}, &raw); err != nil {
		return Work{}, err
	}
	return Work{SealHash: raw[0], SeedHash: raw[1], Target: raw[2]}, nil
}

// SubmitWork submits a solution (eth_submitWork). nonce is the 0x-prefixed
// 8-byte header nonce, sealHash the work's seal hash, mixDigest the RandomX
// result hash. Returns whether the node accepted (verified and inserted) it.
func (c *Client) SubmitWork(ctx context.Context, nonce, sealHash, mixDigest string) (bool, error) {
	var ok bool
	if err := c.call(ctx, "eth_submitWork", []interface{}{nonce, sealHash, mixDigest}, &ok); err != nil {
		return false, err
	}
	return ok, nil
}

// BlockNumber returns the current head height (eth_blockNumber).
func (c *Client) BlockNumber(ctx context.Context) (uint64, error) {
	var hexNum string
	if err := c.call(ctx, "eth_blockNumber", []interface{}{}, &hexNum); err != nil {
		return 0, err
	}
	return strconv.ParseUint(strings.TrimPrefix(hexNum, "0x"), 16, 64)
}
