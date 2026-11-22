package format

import (
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"
)

// FileDiff is one file section of a unified diff.
type FileDiff struct {
	Path    string
	OldPath string
	Status  string // A, M, D, R, C
	Binary  bool
	Added   int
	Removed int
	Text    string // the raw section including headers
}

var diffHeader = regexp.MustCompile(`^diff --git a/(.+) b/(.+)$`)

// SplitDiff splits a unified diff into per-file sections (section 8.4).
// Text before the first "diff --git" line is ignored.
func SplitDiff(diff string) []FileDiff {
	lines := strings.SplitAfter(diff, "\n")
	var files []FileDiff
	var cur *FileDiff
	var sb strings.Builder
	inHunk := false
	finish := func() {
		if cur != nil {
			cur.Text = sb.String()
			files = append(files, *cur)
		}
		cur = nil
		sb.Reset()
		inHunk = false
	}
	for _, l := range lines {
		if l == "" {
			continue
		}
		bare := strings.TrimRight(l, "\r\n")
		if strings.HasPrefix(bare, "diff --git ") {
			finish()
			oldP, newP := parseDiffHeader(bare)
			cur = &FileDiff{Path: newP, OldPath: oldP, Status: "M"}
			sb.WriteString(l)
			continue
		}
		if cur == nil {
			continue
		}
		sb.WriteString(l)
		if inHunk {
			switch {
			case strings.HasPrefix(l, "+"):
				cur.Added++
			case strings.HasPrefix(l, "-"):
				cur.Removed++
			}
			continue
		}
		switch {
		case strings.HasPrefix(bare, "@@"):
			inHunk = true
		case strings.HasPrefix(bare, "new file mode"):
			cur.Status = "A"
		case strings.HasPrefix(bare, "deleted file mode"):
			cur.Status = "D"
		case strings.HasPrefix(bare, "rename from "):
			cur.Status = "R"
			cur.OldPath = unquoteGit(strings.TrimPrefix(bare, "rename from "))
		case strings.HasPrefix(bare, "rename to "):
			cur.Status = "R"
			cur.Path = unquoteGit(strings.TrimPrefix(bare, "rename to "))
		case strings.HasPrefix(bare, "copy from "):
			cur.Status = "C"
			cur.OldPath = unquoteGit(strings.TrimPrefix(bare, "copy from "))
		case strings.HasPrefix(bare, "copy to "):
			cur.Status = "C"
			cur.Path = unquoteGit(strings.TrimPrefix(bare, "copy to "))
		case strings.HasPrefix(bare, "Binary files "), strings.HasPrefix(bare, "GIT binary patch"):
			cur.Binary = true
		}
	}
	finish()
	for i := range files {
		if files[i].Status == "D" {
			files[i].Path = files[i].OldPath
		}
		if files[i].Status != "R" && files[i].Status != "C" {
			files[i].OldPath = ""
		}
	}
	return files
}

// parseDiffHeader extracts the old and new path from a "diff --git" line.
func parseDiffHeader(line string) (oldP, newP string) {
	rest := strings.TrimPrefix(line, "diff --git ")
	if strings.HasPrefix(rest, `"`) {
		if a, tail, ok := cutQuoted(rest); ok {
			tail = strings.TrimLeft(tail, " ")
			b := tail
			if strings.HasPrefix(tail, `"`) {
				if q, _, ok := cutQuoted(tail); ok {
					b = q
				}
			}
			return strings.TrimPrefix(a, "a/"), strings.TrimPrefix(b, "b/")
		}
	}
	if m := diffHeader.FindStringSubmatch(line); m != nil {
		// With spaces in names the greedy match may pick the wrong split;
		// prefer the split where both halves are equal.
		if m[1] != m[2] {
			if half := (len(rest) - 4) / 2; half > 0 && len(rest) == 2*half+4 && rest[2:2+half] == rest[half+4:] {
				return rest[2 : 2+half], rest[half+4:]
			}
		}
		return m[1], m[2]
	}
	return rest, rest
}

// cutQuoted reads one C-style quoted token from the start of s.
func cutQuoted(s string) (val, rest string, ok bool) {
	for i := 1; i < len(s); i++ {
		if s[i] == '\\' {
			i++
			continue
		}
		if s[i] == '"' {
			v, err := strconv.Unquote(s[:i+1])
			if err != nil {
				return "", s, false
			}
			return v, s[i+1:], true
		}
	}
	return "", s, false
}

func unquoteGit(s string) string {
	if strings.HasPrefix(s, `"`) {
		if v, _, ok := cutQuoted(s); ok {
			return v
		}
	}
	return s
}

// FilterDiff keeps the files matching any of the path globs (path.Match on
// the full path, the old path of a rename and the basename; a glob ending in
// "/" matches a directory prefix). No globs keeps everything.
func FilterDiff(files []FileDiff, globs []string) []FileDiff {
	if len(globs) == 0 {
		return files
	}
	var out []FileDiff
	for _, f := range files {
		if matchAny(f, globs) {
			out = append(out, f)
		}
	}
	return out
}

func matchAny(f FileDiff, globs []string) bool {
	for _, g := range globs {
		if g == "" {
			continue
		}
		for _, p := range []string{f.Path, f.OldPath} {
			if p == "" {
				continue
			}
			if strings.HasSuffix(g, "/") && strings.HasPrefix(p, g) {
				return true
			}
			if ok, _ := path.Match(g, p); ok {
				return true
			}
			if ok, _ := path.Match(g, path.Base(p)); ok {
				return true
			}
		}
	}
	return false
}

// fileBlock renders the header and hunks of one file (index/---/+++ lines
// dropped).
func fileBlock(f FileDiff) (header, hunks string) {
	name := f.Path
	if (f.Status == "R" || f.Status == "C") && f.OldPath != "" {
		name = f.OldPath + "->" + f.Path
	}
	switch {
	case f.Binary:
		header = fmt.Sprintf("=== %s %s (binary) ===", f.Status, name)
	default:
		header = fmt.Sprintf("=== %s %s (+%d -%d) ===", f.Status, name, f.Added, f.Removed)
	}
	if !f.Binary {
		if i := strings.Index(f.Text, "@@"); i >= 0 && (i == 0 || f.Text[i-1] == '\n') {
			hunks = f.Text[i:]
		} else if i := strings.Index(f.Text, "\n@@"); i >= 0 {
			hunks = f.Text[i+1:]
		}
	}
	hunks = strings.ReplaceAll(hunks, "\r\n", "\n")
	if hunks != "" && !strings.HasSuffix(hunks, "\n") {
		hunks += "\n"
	}
	return header, hunks
}

// RenderDiff concatenates files within max chars and adds the omitted-files
// trailer (outside the budget) when something was cut.
//
// Each file prints "=== <A|M|D|R> <path> (+a -d) ===" and its hunks; the
// headers count against max. Files are added in order until the next one no
// longer fits; the rest are omitted. A file whose block exceeds max/2 is cut
// at a line boundary with a marker (the cap is max/2 when several files are
// rendered, max for a lone file).
func RenderDiff(files []FileDiff, max int) string {
	if len(files) == 0 {
		return ""
	}
	var sb strings.Builder
	used, shown := 0, 0
	omittedBytes := 0
	var omittedNames []string
	stopped := false
	for _, f := range files {
		header, hunks := fileBlock(f)
		block := header + "\n" + hunks
		if stopped {
			omittedBytes += len(block)
			omittedNames = append(omittedNames, f.Path)
			continue
		}
		capSize := max / 2
		if len(files) == 1 {
			capSize = max
		}
		size := runeLen(block)
		if size > capSize {
			block = cutFileBlock(header, hunks, capSize, size)
			size = runeLen(block)
		}
		if used+size > max {
			stopped = true
			omittedBytes += len(block)
			omittedNames = append(omittedNames, f.Path)
			continue
		}
		sb.WriteString(block)
		used += size
		shown++
	}
	out := strings.TrimRight(sb.String(), "\n")
	if shown < len(files) {
		names := omittedNames
		more := ""
		if len(names) > 15 {
			more = fmt.Sprintf(", +%d more", len(names)-15)
			names = names[:15]
		}
		trailer := fmt.Sprintf("[truncated: %d of %d files shown, %s omitted; omitted: %s%s; pass paths=[...] or raise max_chars]",
			shown, len(files), Size(int64(omittedBytes)), strings.Join(names, ", "), more)
		if out != "" {
			out += "\n"
		}
		out += trailer
	}
	return out
}

// cutFileBlock cuts a file block to about limit chars at a line boundary and
// appends the per-file marker (counted inside the limit).
func cutFileBlock(header, hunks string, limit, total int) string {
	const markerReserve = 100
	budget := limit - runeLen(header) - 1 - markerReserve
	if budget < 0 {
		budget = 0
	}
	used := 0
	var kept strings.Builder
	for _, l := range strings.SplitAfter(hunks, "\n") {
		if l == "" {
			continue
		}
		if used+runeLen(l) > budget {
			break
		}
		used += runeLen(l)
		kept.WriteString(l)
	}
	shown := runeLen(header) + 1 + used
	return header + "\n" + kept.String() +
		fmt.Sprintf("[truncated: file shown %d of %d chars; pass paths=[...] and a larger max_chars]\n", shown, total)
}
