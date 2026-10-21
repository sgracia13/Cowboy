package mcp

import (
	"bufio"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func serve(t *testing.T, s *Server, lines ...string) []map[string]any {
	t.Helper()
	var out strings.Builder
	if err := s.Serve(strings.NewReader(strings.Join(lines, "\n")+"\n"), &out); err != nil {
		t.Fatal(err)
	}
	var got []map[string]any
	sc := bufio.NewScanner(strings.NewReader(out.String()))
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("not JSON on stdout: %q", sc.Text())
		}
		got = append(got, m)
	}
	return got
}

var echo = Tool{Name: "echo", Description: "says it back", InputSchema: map[string]any{"type": "object"},
	Call: func(a json.RawMessage) (string, any, error) {
		var p struct{ Say string }
		json.Unmarshal(a, &p)
		if p.Say == "" {
			return "", nil, errors.New("nothing to say")
		}
		return p.Say, map[string]any{"said": p.Say}, nil
	}}

func TestHandshakeAndTools(t *testing.T) {
	s := &Server{Name: "cowboy", Version: "test", Tools: []Tool{echo}}
	got := serve(t, s,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":"x","method":"tools/call","params":{"name":"echo","arguments":{"say":"hi"}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"echo","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"nope"}}`,
		`{"jsonrpc":"2.0","id":6,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":7,"method":"resources/list"}`,
		`not json`,
	)
	if len(got) != 8 {
		t.Fatalf("want 8 replies (the notification gets none), got %d: %v", len(got), got)
	}
	init := got[0]["result"].(map[string]any)
	if init["protocolVersion"] != "2025-03-26" || init["serverInfo"].(map[string]any)["name"] != "cowboy" {
		t.Errorf("initialize: %v", init)
	}
	if tools := got[1]["result"].(map[string]any)["tools"].([]any); len(tools) != 1 || tools[0].(map[string]any)["name"] != "echo" {
		t.Errorf("tools/list: %v", tools)
	}
	ok := got[2]["result"].(map[string]any)
	if got[2]["id"] != "x" || ok["content"].([]any)[0].(map[string]any)["text"] != "hi" || ok["structuredContent"].(map[string]any)["said"] != "hi" || ok["isError"] != nil {
		t.Errorf("tools/call: %v", got[2])
	}
	if bad := got[3]["result"].(map[string]any); bad["isError"] != true {
		t.Errorf("a tool's own error is a tool result with isError: %v", got[3])
	}
	if got[4]["error"].(map[string]any)["code"].(float64) != -32602 {
		t.Errorf("unknown tool: %v", got[4])
	}
	if got[6]["error"].(map[string]any)["code"].(float64) != -32601 || got[7]["error"].(map[string]any)["code"].(float64) != -32700 {
		t.Errorf("unknown method and parse error: %v %v", got[6], got[7])
	}
}

func TestUnknownVersionGetsNewest(t *testing.T) {
	got := serve(t, &Server{Name: "c"}, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"1999-01-01"}}`)
	if got[0]["result"].(map[string]any)["protocolVersion"] != versions[0] {
		t.Errorf("%v", got[0])
	}
}
