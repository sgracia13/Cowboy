package app

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sgracia13/Cowboy/internal/forge"
	"github.com/sgracia13/Cowboy/internal/store"
)

// archForge is a forge that can serve archives.
type archForge struct {
	forge.Forge
	mu        sync.Mutex
	sha       string
	files     map[string]string
	downloads int
	heads     int
}

func (f *archForge) Head(string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.heads++
	return f.sha, nil
}

func (f *archForge) Archive(_ context.Context, _ string, sha string) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.downloads++
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for name, body := range f.files {
		tw.WriteHeader(&tar.Header{Name: "acme-web-" + sha + "/" + name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(body))})
		tw.Write([]byte(body))
	}
	tw.Close()
	zw.Close()
	return io.NopCloser(&buf), nil
}

func TestGraphFromArchive(t *testing.T) {
	st, _ := store.Open(filepath.Join(t.TempDir(), "s.json"))
	f := &archForge{Forge: forge.NewFake("", ""), sha: "aaaaaaaaaaaaaaaa", files: map[string]string{
		"src/lib.ts": "export const x = 1", "src/a.ts": "import { x } from './lib'", "src/b.ts": "import { x } from './lib'",
	}}
	a := New(f, st, nil)
	if a.Graph("acme/web") != nil {
		t.Fatal("archives are off unless ArchiveGraphs is set")
	}
	a.ArchiveGraphs = true
	if s := a.GraphStatus("acme/web"); !strings.Contains(s, "builds from the default branch") {
		t.Errorf("status before build: %q", s)
	}
	g := a.Graph("acme/web")
	if g == nil {
		t.Fatal("no graph")
	}
	if n, _ := g.FanIn("src/lib.ts"); n != 2 {
		t.Errorf("fan-in %d, want 2", n)
	}
	a.Graph("acme/web")
	if f.downloads != 1 {
		t.Errorf("downloads %d, want 1 (cached)", f.downloads)
	}
	if s := a.GraphStatus("acme/web"); !strings.Contains(s, "from the archive at aaaaaaaaaaaa") {
		t.Errorf("status: %q", s)
	}

	// an hour later, same commit: one cheap check, no download
	a.mu.Lock()
	a.graphAt["acme/web"] = time.Now().Add(-2 * GraphMaxAge)
	a.mu.Unlock()
	a.Graph("acme/web")
	waitFor(t, func() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.heads == 2 })
	time.Sleep(20 * time.Millisecond)
	if f.downloads != 1 {
		t.Errorf("downloaded again though the default branch didn't move")
	}

	// the default branch moved: the old graph keeps working while the new one builds
	f.mu.Lock()
	f.sha = "bbbbbbbbbbbbbbbb"
	f.files["src/c.ts"] = "import { x } from './lib'"
	f.mu.Unlock()
	a.mu.Lock()
	a.graphAt["acme/web"] = time.Now().Add(-2 * GraphMaxAge)
	a.mu.Unlock()
	if old := a.Graph("acme/web"); old != g {
		t.Error("a stale graph should be returned while the new one builds")
	}
	waitFor(t, func() bool {
		g2 := a.Graph("acme/web")
		n, _ := g2.FanIn("src/lib.ts")
		return n == 3
	})
}

type bigForge struct{ archForge }

func (f *bigForge) Archive(context.Context, string, string) (io.ReadCloser, error) {
	f.downloads++
	return io.NopCloser(strings.NewReader(strings.Repeat("x", 100))), nil
}

func TestGraphArchiveFailureIsRemembered(t *testing.T) {
	st, _ := store.Open(filepath.Join(t.TempDir(), "s.json"))
	f := &bigForge{archForge{Forge: forge.NewFake("", ""), sha: "c"}}
	a := New(f, st, nil)
	a.ArchiveGraphs = true
	if a.Graph("acme/web") != nil {
		t.Fatal("a broken archive should give no graph")
	}
	a.Graph("acme/web")
	if f.downloads != 1 {
		t.Errorf("retried a failed download right away (%d downloads)", f.downloads)
	}
	if s := a.GraphStatus("acme/web"); !strings.HasPrefix(s, "not working: ") {
		t.Errorf("status %q", s)
	}
	_ = errors.New
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out")
}
