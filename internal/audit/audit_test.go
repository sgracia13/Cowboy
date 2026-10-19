package audit

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestChain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	l, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	l.Append("sorted", "acme/web", 1, "", map[string]string{"pen": "careful", "agent": "claude-code"})
	l.Append("moved", "acme/web", 1, "maria", map[string]string{"from": "careful", "to": "normal"})
	l.Append("merged", "acme/web", 1, "", map[string]string{"approvers": "maria"})
	if n, err := Verify(path); err != nil || n != 3 {
		t.Fatalf("verify: %d %v", n, err)
	}
	// reopening continues the same chain
	l2, _ := Open(path)
	l2.Append("outcome", "acme/web", 1, "", map[string]string{"kind": "revert"})
	if n, err := Verify(path); err != nil || n != 4 {
		t.Fatalf("after reopen: %d %v", n, err)
	}
	if n, fp := l2.Fingerprint(); n != 4 || len(fp) != 12 {
		t.Errorf("fingerprint %d %q", n, fp)
	}
	orig, _ := os.ReadFile(path)

	// changing a detail is caught at that entry
	os.WriteFile(path, bytes.Replace(orig, []byte(`"to":"normal"`), []byte(`"to":"easy"`), 1), 0o600)
	if _, err := Verify(path); err == nil || !strings.Contains(err.Error(), "entry 2") {
		t.Errorf("edit: %v", err)
	}
	// removing an entry is caught
	lines := bytes.Split(bytes.TrimSpace(orig), []byte("\n"))
	os.WriteFile(path, append(bytes.Join(append(lines[:1:1], lines[2:]...), []byte("\n")), '\n'), 0o600)
	if _, err := Verify(path); err == nil {
		t.Error("a removed entry wasn't caught")
	}
	// the untouched file still verifies, and exports
	os.WriteFile(path, orig, 0o600)
	es, _ := Read(path)
	var buf bytes.Buffer
	WriteCSV(&buf, Filter(es, time.Time{}, "acme/web"))
	if !strings.Contains(buf.String(), "moved,acme/web,1,maria,from=careful; to=normal") {
		t.Errorf("csv:\n%s", buf.String())
	}
}
