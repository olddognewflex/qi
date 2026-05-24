// Package daemon implements qid's JSON-RPC 2.0 server. Transport is
// newline-delimited JSON over a unix-domain socket (or any net.Conn, for
// tests). Framing matches the MCP stdio convention so the same codec is
// reusable when qi-mcp lands.
package daemon

import "encoding/json"

// JSONRPCVersion is the only protocol version qid speaks.
const JSONRPCVersion = "2.0"

// Standard JSON-RPC 2.0 error codes.
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603
)

// Application error codes (reserved range -32000 to -32099).
const (
	CodeToolNotFound       = -32001
	CodeToolNotImplemented = -32002
	CodeHandlerError       = -32003
	CodePolicyDenied       = -32004
	CodeApprovalUnknown    = -32005
)

// Request is a JSON-RPC 2.0 request frame. ID is left as RawMessage so the
// server preserves whatever shape the client sent (number, string, or null).
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
	ID      json.RawMessage `json:"id,omitempty"`
}

// IsNotification reports whether the request omits the id field. JSON-RPC
// notifications must not be answered.
func (r Request) IsNotification() bool {
	if len(r.ID) == 0 {
		return true
	}
	if string(r.ID) == "null" {
		return true
	}
	return false
}

// Response is a JSON-RPC 2.0 response frame. Exactly one of Result or Error
// is set per the spec.
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
	ID      json.RawMessage `json:"id"`
}

// RPCError is the JSON-RPC 2.0 error object.
type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func successResponse(id json.RawMessage, result json.RawMessage) Response {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	return Response{JSONRPC: JSONRPCVersion, Result: result, ID: id}
}

func errorResponse(id json.RawMessage, code int, message string) Response {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	return Response{
		JSONRPC: JSONRPCVersion,
		Error:   &RPCError{Code: code, Message: message},
		ID:      id,
	}
}
