package proxy

import "encoding/json"

type JSONRpcReq struct {
	Id     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

type StratumReq struct {
	JSONRpcReq
	Worker string `json:"worker"`
}

// Stratum
type JSONPushMessage struct {
	// Preserve the numeric notification ID required by legacy Claymore clients.
	Id      int64       `json:"id"`
	Version string      `json:"jsonrpc"`
	Result  interface{} `json:"result"`
}

type JSONRpcResp struct {
	Id      json.RawMessage `json:"id"`
	Version string          `json:"jsonrpc"`
	Result  interface{}     `json:"result"`
	Error   interface{}     `json:"error,omitempty"`
}

type SubmitReply struct {
	Status string `json:"status"`
}

type ErrorReply struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// StratumPushMessage is a standard Stratum V1 server notification. Bitcoin-
// family ASICs expect method/params with a null id rather than an Ethereum
// JSON-RPC result push.
type StratumPushMessage struct {
	Id     interface{} `json:"id"`
	Method string      `json:"method"`
	Params interface{} `json:"params"`
}

// StratumResponse is the Bitcoin-family Stratum V1 response shape. Unlike the
// legacy Ethereum JSON-RPC path it intentionally omits a jsonrpc field.
type StratumResponse struct {
	Id     json.RawMessage `json:"id"`
	Result interface{}     `json:"result"`
	Error  interface{}     `json:"error"`
}
