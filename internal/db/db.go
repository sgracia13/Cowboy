// Package db is Cowboy's SQLite file: one table of records, each a JSON
// document under a kind and a key. Changes are written row by row inside a
// transaction, so saving one decision no longer rewrites everything, and a
// crash mid-write leaves the last good state. SQLite runs in pure Go (no
// cgo), so builds stay one static binary.
package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
)

// SchemaVersion is the layout of the records table. Open refuses a file
// written by a newer Cowboy instead of guessing.
const SchemaVersion = 1

// DB is one SQLite file.
type DB struct {
	sql  *sql.DB
	Path string
}

// Op is one change: a record written (Body) or removed (Delete).
type Op struct {
	Kind, Key string
	Body      []byte
	Delete    bool
}

var (
	mu   sync.Mutex
	open = map[string]*DB{}
)

// Open opens (or creates) the database at path. The same file opened twice
// in one process shares one connection pool.
func Open(path string) (*DB, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	mu.Lock()
	defer mu.Unlock()
	if d := open[abs]; d != nil {
		return d, nil
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		return nil, err
	}
	// create the file private first: SQLite gives its -wal and -shm side
	// files the same permissions as the database
	if f, err := os.OpenFile(abs, os.O_RDWR|os.O_CREATE, 0o600); err == nil {
		f.Close()
	}
	os.Chmod(abs, 0o600)
	dsn := "file:" + abs + "?_pragma=busy_timeout(10000)&_pragma=journal_mode(wal)&_pragma=synchronous(normal)&_pragma=foreign_keys(on)"
	s, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, err
	}
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS records (kind TEXT NOT NULL, key TEXT NOT NULL, body BLOB NOT NULL, PRIMARY KEY (kind, key)) WITHOUT ROWID`,
		`CREATE TABLE IF NOT EXISTS meta (k TEXT PRIMARY KEY, v TEXT NOT NULL)`,
	} {
		if _, err := s.Exec(q); err != nil {
			s.Close()
			return nil, fmt.Errorf("%s: %w", abs, err)
		}
	}
	var v int
	err = s.QueryRow(`SELECT CAST(v AS INTEGER) FROM meta WHERE k = 'schema'`).Scan(&v)
	switch {
	case err == sql.ErrNoRows:
		if _, err := s.Exec(`INSERT INTO meta (k, v) VALUES ('schema', ?)`, fmt.Sprint(SchemaVersion)); err != nil {
			s.Close()
			return nil, err
		}
	case err != nil:
		s.Close()
		return nil, fmt.Errorf("%s: %w", abs, err)
	case v > SchemaVersion:
		s.Close()
		return nil, fmt.Errorf("%s was written by a newer version of Cowboy (data layout %d; this one reads up to %d): upgrade Cowboy", abs, v, SchemaVersion)
	}
	for _, side := range []string{"-wal", "-shm"} {
		os.Chmod(abs+side, 0o600)
	}
	d := &DB{sql: s, Path: abs}
	open[abs] = d
	return d, nil
}

// Load returns every record of a kind, by key.
func (d *DB) Load(kind string) (map[string][]byte, error) {
	rows, err := d.sql.Query(`SELECT key, body FROM records WHERE kind = ?`, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]byte{}
	for rows.Next() {
		var k string
		var b []byte
		if err := rows.Scan(&k, &b); err != nil {
			return nil, err
		}
		out[k] = b
	}
	return out, rows.Err()
}

// Write applies the changes in one transaction: all or none.
func (d *DB) Write(ops ...Op) error {
	if len(ops) == 0 {
		return nil
	}
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	put, err := tx.Prepare(`INSERT INTO records (kind, key, body) VALUES (?, ?, ?) ON CONFLICT (kind, key) DO UPDATE SET body = excluded.body`)
	if err != nil {
		tx.Rollback()
		return err
	}
	defer put.Close()
	for _, o := range ops {
		if o.Delete {
			_, err = tx.Exec(`DELETE FROM records WHERE kind = ? AND key = ?`, o.Kind, o.Key)
		} else {
			_, err = put.Exec(o.Kind, o.Key, o.Body)
		}
		if err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// Meta reads a setting ("" if unset); SetMeta writes one.
func (d *DB) Meta(k string) string {
	var v string
	d.sql.QueryRow(`SELECT v FROM meta WHERE k = ?`, k).Scan(&v)
	return v
}

func (d *DB) SetMeta(k, v string) error {
	_, err := d.sql.Exec(`INSERT INTO meta (k, v) VALUES (?, ?) ON CONFLICT (k) DO UPDATE SET v = excluded.v`, k, v)
	return err
}

// Close closes the file (tests; the app keeps it open).
func (d *DB) Close() error {
	mu.Lock()
	delete(open, d.Path)
	mu.Unlock()
	return d.sql.Close()
}

// Path turns a legacy JSON data path (data/cowboy.json) into the database
// path next to it (data/cowboy.db).
func PathFor(jsonPath string) string {
	ext := filepath.Ext(jsonPath)
	return jsonPath[:len(jsonPath)-len(ext)] + ".db"
}
