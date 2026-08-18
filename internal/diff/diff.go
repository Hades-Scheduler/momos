// Package diff parses unified diffs enough to (a) enforce size/file limits and
// (b) classify which (file, line) pairs are addable inline-comment positions on
// the head side. The publisher only posts inline comments on added lines, so a
// finding elsewhere is folded into the summary rather than rejected by GitHub
// (plan.md §11.7, §12).
package diff

import (
	"bufio"
	"strconv"
	"strings"
)

// FileDiff is the per-file result of parsing.
type FileDiff struct {
	Path       string
	AddedLines map[int]bool // new-side line numbers of added ('+') lines
	Additions  int
	Deletions  int
}

// Diff is a parsed unified diff.
type Diff struct {
	Files map[string]*FileDiff
	Bytes int
}

// Parse parses a unified diff (git diff output).
func Parse(unified string) *Diff {
	d := &Diff{Files: map[string]*FileDiff{}, Bytes: len(unified)}
	sc := bufio.NewScanner(strings.NewReader(unified))
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)

	var cur *FileDiff
	var newLine int
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "diff --git"):
			// Start of a new file section: forget the previous file so its
			// "index"/"---"/"new file mode" header lines are not miscounted.
			cur = nil
		case strings.HasPrefix(line, "+++ "):
			path := strings.TrimPrefix(line, "+++ ")
			path = strings.TrimPrefix(path, "b/")
			if path == "/dev/null" {
				cur = nil
				continue
			}
			cur = &FileDiff{Path: path, AddedLines: map[int]bool{}}
			d.Files[path] = cur
		case strings.HasPrefix(line, "--- "):
			// old-file header; never a content deletion
			continue
		case strings.HasPrefix(line, "@@"):
			// @@ -a,b +c,d @@
			newLine = parseHunkNewStart(line)
		case cur == nil:
			// header lines before the first +++; ignore
			continue
		case strings.HasPrefix(line, "+"):
			cur.AddedLines[newLine] = true
			cur.Additions++
			newLine++
		case strings.HasPrefix(line, "-"):
			cur.Deletions++
			// deletions do not advance the new-side counter
		case strings.HasPrefix(line, "\\"):
			// "\ No newline at end of file"
			continue
		default:
			// context line
			newLine++
		}
	}
	return d
}

func parseHunkNewStart(hunk string) int {
	// Find the "+c,d" token.
	plus := strings.Index(hunk, "+")
	if plus < 0 {
		return 0
	}
	rest := hunk[plus+1:]
	end := strings.IndexAny(rest, ", ")
	if end < 0 {
		end = len(rest)
	}
	n, _ := strconv.Atoi(rest[:end])
	return n
}

// IsAddedLine reports whether (path, line) is an added line on the head side.
func (d *Diff) IsAddedLine(path string, line int) bool {
	fd, ok := d.Files[path]
	if !ok {
		return false
	}
	return fd.AddedLines[line]
}

// ChangedFiles returns the number of files touched by the diff.
func (d *Diff) ChangedFiles() int { return len(d.Files) }
