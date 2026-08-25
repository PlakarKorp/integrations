package openwrtclient

import "encoding/json"

type miniRPCRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params"`
}

type miniRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type miniRPCResponse struct {
	JSONRPC string            `json:"jsonrpc"`
	ID      int               `json:"id"`
	Result  []json.RawMessage `json:"result"`
	Error   *miniRPCError     `json:"error,omitempty"`
}

type execResult struct {
	Code   int    `json:"code"`
	Stdout string `json:"stdout"`
	Stderr string `json:"stderr"`
}

type loginResult struct {
	Session string `json:"ubus_rpc_session"`
}

const nilSessionID = "00000000000000000000000000000000"

type UbusObject = string

const (
	UbusObjectSession UbusObject = "session"
	UbusObjectFile    UbusObject = "file"
)

type UbusMethod = string

const (
	UbusMethodLogin UbusMethod = "login"
	UbusMethodExec  UbusMethod = "exec"
)
