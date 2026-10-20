package db

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.db")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Write(Op{Kind: "a", Key: "1", Body: []byte(`{"n":1}`)}, Op{Kind: "a", Key: "2", Body: []byte(`{"n":2}`)}, Op{Kind: "b", Key: "1", Body: []byte(`x`)}); err != nil {
		t.Fatal(err)
	}
	d.Write(Op{Kind: "a", Key: "1", Body: []byte(`{"n":10}`)}, Op{Kind: "a", Key: "2", Delete: true})
	d.Close()
	d, _ = Open(path)
	m, err := d.Load("a")
	if err != nil || len(m) != 1 || string(m["1"]) != `{"n":10}` {
		t.Fatalf("after reopen: %v %v", m, err)
	}
	if b, _ := d.Load("b"); string(b["1"]) != "x" {
		t.Error("kinds should be separate")
	}
	d.SetMeta("migrated", "yes")
	if d.Meta("migrated") != "yes" || d.Meta("nope") != "" {
		t.Error("meta")
	}
	// concurrent writers don't lose rows or error
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 25; j++ {
				if err := d.Write(Op{Kind: "c", Key: string(rune('a'+i)) + string(rune('a'+j)), Body: []byte("1")}); err != nil {
					t.Error(err)
				}
			}
		}(i)
	}
	wg.Wait()
	if c, _ := d.Load("c"); len(c) != 200 {
		t.Errorf("concurrent rows: %d", len(c))
	}
	d.Close()
}

func TestNewerSchemaRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.db")
	d, _ := Open(path)
	d.SetMeta("schema", "99")
	d.Close()
	if _, err := Open(path); err == nil {
		t.Fatal("a file from a newer Cowboy should be refused")
	}
}

func TestPathFor(t *testing.T) {
	if PathFor("data/cowboy.json") != "data/cowboy.db" || PathFor("x") != "x.db" {
		t.Error(PathFor("data/cowboy.json"))
	}
}

func TestFilesArePrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "p.db")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	d.Write(Op{Kind: "a", Key: "1", Body: []byte("secret")})
	for _, f := range []string{path, path + "-wal", path + "-shm"} {
		if st, err := os.Stat(f); err == nil && st.Mode().Perm()&0o077 != 0 {
			t.Errorf("%s is %v, want readable by its owner only", filepath.Base(f), st.Mode().Perm())
		}
	}
	d.Close()
}

// A process killed mid-run leaves its write-ahead log behind; the next
// start must replay it, not crash or lose rows. (v0.21 of the SQLite
// library crashed here; this keeps it from coming back.)
func TestReopenAfterKill(t *testing.T) {
	if os.Getenv("COWBOY_DB_CHILD") != "" {
		d, err := Open(os.Getenv("COWBOY_DB_CHILD"))
		if err != nil {
			os.Exit(3)
		}
		for i := 0; i < 300; i++ {
			d.Write(Op{Kind: "k", Key: fmt.Sprint(i), Body: []byte(strings.Repeat("x", 500))})
		}
		os.Exit(0) // no Close: the log stays, as after a kill
	}
	path := filepath.Join(t.TempDir(), "killed.db")
	cmd := exec.Command(os.Args[0], "-test.run=^TestReopenAfterKill$")
	cmd.Env = append(os.Environ(), "COWBOY_DB_CHILD="+path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child: %v %s", err, out)
	}
	if st, err := os.Stat(path + "-wal"); err != nil || st.Size() == 0 {
		t.Fatalf("expected a leftover write-ahead log: %v", err)
	}
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	m, err := d.Load("k")
	if err != nil || len(m) != 300 {
		t.Fatalf("after reopening: %d rows, %v", len(m), err)
	}
}
