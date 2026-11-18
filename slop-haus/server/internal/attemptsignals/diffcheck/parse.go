package diffcheck

import (
	"bufio"
	"strings"
)

type fileChange struct {
	path    string // post-change path; the old path for deletions
	oldPath string // differs from path only for renames
	deleted bool
	added   []string
	removed []string
	nAdded  int
	nRemove int
	binary  bool
}

// parseDiff reads a git-style unified diff. Lines inside hunks are kept only
// as +/- bodies; headers decide the file's identity.
func parseDiff(diff string) ([]*fileChange, error) {
	var files []*fileChange
	var cur *fileChange
	inHunk := false
	sc := bufio.NewScanner(strings.NewReader(diff))
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "diff --git ") {
			cur = &fileChange{}
			cur.oldPath, cur.path = splitGitHeader(strings.TrimPrefix(line, "diff --git "))
			files = append(files, cur)
			inHunk = false
			continue
		}
		if cur == nil {
			continue
		}
		if !inHunk {
			switch {
			case strings.HasPrefix(line, "deleted file mode"):
				cur.deleted = true
			case strings.HasPrefix(line, "rename from "):
				cur.oldPath = strings.TrimPrefix(line, "rename from ")
			case strings.HasPrefix(line, "rename to "):
				cur.path = strings.TrimPrefix(line, "rename to ")
			case strings.HasPrefix(line, "Binary files "), line == "GIT binary patch":
				cur.binary = true
			case strings.HasPrefix(line, "@@"):
				inHunk = true
			}
			continue
		}
		switch {
		case strings.HasPrefix(line, "@@"):
		case strings.HasPrefix(line, "+"):
			cur.nAdded++
			cur.added = append(cur.added, line[1:])
		case strings.HasPrefix(line, "-"):
			cur.nRemove++
			cur.removed = append(cur.removed, line[1:])
		}
	}
	return files, sc.Err()
}

// splitGitHeader handles the common "a/x b/x" form. Paths containing " b/"
// are ambiguous in git's own format; rename/--- lines are not consulted, so
// such paths may be mis-split.
func splitGitHeader(rest string) (oldPath, newPath string) {
	i := strings.LastIndex(rest, " b/")
	if i < 0 || !strings.HasPrefix(rest, "a/") {
		return rest, rest
	}
	return strings.TrimPrefix(rest[:i], "a/"), rest[i+3:]
}
