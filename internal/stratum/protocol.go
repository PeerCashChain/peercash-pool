package stratum

import "encoding/json"

// Line-delimited JSON-RPC messages for the Monero/XMRig stratum protocol.
// Every message is a single JSON object terminated by '\n'. All strings sent
// on the wire are pure ASCII (hex, status words, algo names).

// request is a client -> server message.
type request struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

// loginParams is the body of a "login" request.
type loginParams struct {
	Login string   `json:"login"`
	Pass  string   `json:"pass"`
	Agent string   `json:"agent"`
	Algo  []string `json:"algo"`
}

// submitParams is the body of a "submit" request.
type submitParams struct {
	ID     string `json:"id"`     // session id from login
	JobID  string `json:"job_id"` // which job this solves
	Nonce  string `json:"nonce"`  // 4-byte nonce, hex, in blob byte order
	Result string `json:"result"` // 32-byte RandomX hash, hex
}

// job is the work descriptor pushed to XMRig.
type job struct {
	Blob     string `json:"blob"` // 43-byte blob, hex (86 chars)
	JobID    string `json:"job_id"`
	Target   string `json:"target"`    // 8-byte target, little-endian hex (16 chars)
	SeedHash string `json:"seed_hash"` // RandomX seed, hex (64 chars, no 0x)
	Algo     string `json:"algo"`      // always "rx/0"
	Height   uint64 `json:"height"`
}

// loginResult is the result object of a successful login.
type loginResult struct {
	ID         string   `json:"id"` // session id
	Job        *job     `json:"job"`
	Status     string   `json:"status"`
	Extensions []string `json:"extensions"`
}

// statusResult is a bare {"status": "..."} result (submit/keepalived acks).
type statusResult struct {
	Status string `json:"status"`
}

// response is a server -> client reply to a request.
type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  interface{}     `json:"result"`
	Error   *rpcErr         `json:"error"`
}

// notification is a server -> client push (no id), e.g. a new job.
type notification struct {
	JSONRPC string      `json:"jsonrpc"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params"`
}

type rpcErr struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}
