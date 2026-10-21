// Package mcp is a small Model Context Protocol server over stdio: one
// JSON-RPC 2.0 message per line in, one per line out. It serves tools only
// (initialize, ping, tools/list, tools/call) and needs no network: coding
// agents start it as a local process (`cowboy mcp`). Logs go to stderr,
// never stdout, which carries only protocol messages.
package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
)

// Tool is one thing an agent can call.
type Tool struct {
	Name        string         `json:"name"`
	Title       string         `json:"title,omitempty"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	Annotations map[string]any `json:"annotations,omitempty"`
	// Call runs the tool. A returned error is shown to the agent as a tool
	// error (isError), not a protocol error, so it can read it and adjust.
	Call func(args json.RawMessage) (text string, structured any, err error) `json:"-"`
}

// Server answers MCP requests.
type Server struct {
	Name, Version string
	Tools         []Tool
	Log           *log.Logger // nil: no logging
}

// Supported protocol versions, newest first. A client asking for another
// one gets the newest.
var versions = []string{"2025-06-18", "2025-03-26", "2024-11-05"}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// Serve reads requests from in and writes responses to out until in ends.
func (s *Server) Serve(in io.Reader, out io.Writer) error {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	enc := json.NewEncoder(out)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var req request
		if err := json.Unmarshal(line, &req); err != nil {
			enc.Encode(response{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{-32700, "parse error"}})
			continue
		}
		res, rerr := s.handle(req)
		if len(req.ID) == 0 { // a notification: no reply
			continue
		}
		resp := response{JSONRPC: "2.0", ID: req.ID}
		if rerr != nil {
			resp.Error = rerr
		} else {
			resp.Result = res
		}
		if err := enc.Encode(resp); err != nil {
			return err
		}
	}
	return sc.Err()
}

func (s *Server) logf(f string, a ...any) {
	if s.Log != nil {
		s.Log.Printf(f, a...)
	}
}

func (s *Server) handle(req request) (any, *rpcError) {
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		json.Unmarshal(req.Params, &p)
		v := versions[0]
		for _, x := range versions {
			if x == p.ProtocolVersion {
				v = x
			}
		}
		return map[string]any{
			"protocolVersion": v,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": s.Name, "version": s.Version},
			"instructions":    "Cowboy sorts code changes the way a team's review rules would, and flags shortcuts like skipped tests or silenced warnings. Call check_changes before opening a pull request and fix what it flags. Everything runs locally and only reads.",
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": s.Tools}, nil
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, &rpcError{-32602, "invalid params"}
		}
		for _, t := range s.Tools {
			if t.Name != p.Name {
				continue
			}
			args := p.Arguments
			if len(args) == 0 || string(args) == "null" {
				args = json.RawMessage("{}")
			}
			s.logf("tool %s", t.Name)
			text, structured, err := t.Call(args)
			if err != nil {
				return map[string]any{"content": []map[string]any{{"type": "text", "text": err.Error()}}, "isError": true}, nil
			}
			res := map[string]any{"content": []map[string]any{{"type": "text", "text": text}}}
			if structured != nil {
				res["structuredContent"] = structured
			}
			return res, nil
		}
		return nil, &rpcError{-32602, fmt.Sprintf("unknown tool %q", p.Name)}
	case "notifications/initialized", "notifications/cancelled":
		return nil, nil
	}
	return nil, &rpcError{-32601, "method not found: " + req.Method}
}
