// Package audit keeps a tamper-evident record of what happened to each
// change: how Cowboy sorted it, who moved it, who approved it, what happened
// after merge. Each entry carries the hash of the one before it, so editing
// or deleting an entry breaks every hash after it, and `cowboy audit verify`
// says where. Rewriting the whole chain from that point is still possible
// for someone with the file, so Cowboy also prints the latest fingerprint in
// the weekly tally: a copy outside the server to compare against.
package audit

import (
	"bufio"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Entry is one line of the record.
type Entry struct {
	Seq    int               `json:"seq"`
	At     time.Time         `json:"at"`
	Kind   string            `json:"kind"` // sorted, moved, not-agent, merged, closed, merge-recorded, outcome, outcome-confirmed, outcome-rejected
	Repo   string            `json:"repo,omitempty"`
	Number int               `json:"number,omitempty"`
	Actor  string            `json:"actor"` // cowboy, or the person
	Data   map[string]string `json:"data,omitempty"`
	Prev   string            `json:"prev"`
	Hash   string            `json:"hash"`
}

func (e Entry) digest() string {
	c := e
	c.Hash = ""
	// keys of Data are sorted by encoding/json, so this is stable
	b, _ := json.Marshal(c)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// Log appends entries to a file (one JSON object per line).
type Log struct {
	mu   sync.Mutex
	path string
	seq  int
	last string
	now  func() time.Time
}

// Open reads the end of an existing record, or starts a new one.
func Open(path string) (*Log, error) {
	l := &Log{path: path, now: time.Now}
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return l, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		var e Entry
		if json.Unmarshal(sc.Bytes(), &e) == nil {
			l.seq, l.last = e.Seq, e.Hash
		}
	}
	return l, sc.Err()
}

// Append adds one entry.
func (l *Log) Append(kind, repo string, number int, actor string, data map[string]string) error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if actor == "" {
		actor = "cowboy"
	}
	e := Entry{Seq: l.seq + 1, At: l.now().UTC().Truncate(time.Second), Kind: kind, Repo: repo, Number: number, Actor: actor, Data: data, Prev: l.last}
	e.Hash = e.digest()
	b, _ := json.Marshal(e)
	os.MkdirAll(filepath.Dir(l.path), 0o755)
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	l.seq, l.last = e.Seq, e.Hash
	return nil
}

// Fingerprint is the number of entries and the latest hash (short).
func (l *Log) Fingerprint() (int, string) {
	if l == nil {
		return 0, ""
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.last) < 12 {
		return l.seq, l.last
	}
	return l.seq, l.last[:12]
}

// Path is the file the record is kept in.
func (l *Log) Path() string { return l.path }

// Read loads every entry.
func Read(path string) ([]Entry, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Entry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	line := 0
	for sc.Scan() {
		line++
		var e Entry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			return out, fmt.Errorf("line %d isn't a record entry: %v", line, err)
		}
		out = append(out, e)
	}
	return out, sc.Err()
}

// Verify checks every hash and link. It returns the number of entries and,
// if the record was changed, an error naming the first broken entry.
func Verify(path string) (int, error) {
	es, err := Read(path)
	if err != nil {
		return len(es), err
	}
	prev := ""
	for i, e := range es {
		if e.Seq != i+1 {
			return i, fmt.Errorf("entry %d is numbered %d: an entry was removed or added", i+1, e.Seq)
		}
		if e.Prev != prev {
			return i, fmt.Errorf("entry %d doesn't follow entry %d: something before it was changed or removed", e.Seq, e.Seq-1)
		}
		if e.digest() != e.Hash {
			return i, fmt.Errorf("entry %d (%s %s#%d) was changed after it was written", e.Seq, e.Kind, e.Repo, e.Number)
		}
		prev = e.Hash
	}
	return len(es), nil
}

// Filter keeps entries since a time and, optionally, for one repo.
func Filter(es []Entry, since time.Time, repo string) []Entry {
	var out []Entry
	for _, e := range es {
		if e.At.Before(since) || (repo != "" && !strings.EqualFold(e.Repo, repo)) {
			continue
		}
		out = append(out, e)
	}
	return out
}

// WriteCSV writes entries as a spreadsheet-friendly table.
func WriteCSV(w io.Writer, es []Entry) error {
	cw := csv.NewWriter(w)
	cw.Write([]string{"seq", "at", "kind", "repo", "pr", "actor", "details", "hash"})
	for _, e := range es {
		keys := make([]string, 0, len(e.Data))
		for k := range e.Data {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var parts []string
		for _, k := range keys {
			parts = append(parts, k+"="+e.Data[k])
		}
		pr := ""
		if e.Number > 0 {
			pr = strconv.Itoa(e.Number)
		}
		cw.Write([]string{strconv.Itoa(e.Seq), e.At.Format(time.RFC3339), e.Kind, e.Repo, pr, e.Actor, strings.Join(parts, "; "), e.Hash})
	}
	cw.Flush()
	return cw.Error()
}
