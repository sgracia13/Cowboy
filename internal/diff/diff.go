// Package diff parses unified diffs into per-file added and removed lines.
package diff

import (
	"bufio"
	"strings"
)

// File is one file's changes in a pull request.
type File struct {
	Path    string // new path (old path for deletions)
	OldPath string
	Status  string   // added, modified, removed, renamed
	Added   []string // added lines, without the leading "+"
	Removed []string // removed lines, without the leading "-"
}

// Changes returns added + removed line count.
func (f File) Changes() int { return len(f.Added) + len(f.Removed) }

// Parse reads a unified diff (as produced by `git diff` or a forge's .diff endpoint).
func Parse(text string) []File {
	var files []File
	var cur *File
	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	inHunk := false
	flush := func() {
		if cur != nil {
			if cur.Path == "" {
				cur.Path = cur.OldPath
			}
			files = append(files, *cur)
		}
	}
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "diff --git "):
			flush()
			cur = &File{Status: "modified"}
			inHunk = false
			// diff --git a/x b/y
			parts := strings.SplitN(strings.TrimPrefix(line, "diff --git "), " b/", 2)
			if len(parts) == 2 {
				cur.OldPath = strings.TrimPrefix(parts[0], "a/")
				cur.Path = parts[1]
			}
		case cur == nil:
			continue
		case strings.HasPrefix(line, "new file mode"):
			cur.Status = "added"
		case strings.HasPrefix(line, "deleted file mode"):
			cur.Status = "removed"
		case strings.HasPrefix(line, "rename from "):
			cur.Status = "renamed"
			cur.OldPath = strings.TrimPrefix(line, "rename from ")
		case strings.HasPrefix(line, "rename to "):
			cur.Path = strings.TrimPrefix(line, "rename to ")
		case strings.HasPrefix(line, "--- ") && !inHunk:
			p := strings.TrimPrefix(line, "--- ")
			if p != "/dev/null" {
				cur.OldPath = strings.TrimPrefix(p, "a/")
			}
		case strings.HasPrefix(line, "+++ ") && !inHunk:
			p := strings.TrimPrefix(line, "+++ ")
			if p == "/dev/null" {
				cur.Status = "removed"
				cur.Path = cur.OldPath
			} else {
				cur.Path = strings.TrimPrefix(p, "b/")
			}
		case strings.HasPrefix(line, "@@"):
			inHunk = true
		case inHunk && strings.HasPrefix(line, "+"):
			cur.Added = append(cur.Added, line[1:])
		case inHunk && strings.HasPrefix(line, "-"):
			cur.Removed = append(cur.Removed, line[1:])
		}
	}
	flush()
	return files
}
