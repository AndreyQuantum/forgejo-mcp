// Package format holds pure output helpers (DESIGN.md section 6). It imports
// nothing internal.
package format

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Page describes one page of a list (forgejo.Page is an alias of this type).
type Page struct {
	Page  int
	Limit int
	Total int64 // -1 unknown
	Shown int
	More  bool
}

// B is a strings.Builder wrapper for compact text output.
type B struct{ sb []byte }

// isZero reports whether v is a zero value that KV skips.
func isZero(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Slice, reflect.Map, reflect.Array, reflect.String:
		return rv.Len() == 0
	case reflect.Pointer, reflect.Interface:
		return rv.IsNil()
	default:
		return rv.IsZero()
	}
}

// KV writes "key: val\n"; zero values are skipped; multi-line values are
// indented two spaces.
func (b *B) KV(key string, val any) {
	if isZero(val) {
		return
	}
	s := strings.TrimRight(fmt.Sprint(val), "\n")
	if s == "" {
		return
	}
	s = strings.ReplaceAll(s, "\n", "\n  ")
	b.sb = append(b.sb, key...)
	b.sb = append(b.sb, ": "...)
	b.sb = append(b.sb, s...)
	b.sb = append(b.sb, '\n')
}

// Line joins the non-empty parts with " | " and ends the line. Nothing is
// written when every part is empty.
func (b *B) Line(parts ...string) {
	n := 0
	for _, p := range parts {
		if p == "" {
			continue
		}
		if n > 0 {
			b.sb = append(b.sb, " | "...)
		}
		b.sb = append(b.sb, p...)
		n++
	}
	if n > 0 {
		b.sb = append(b.sb, '\n')
	}
}

// Raw appends s unchanged.
func (b *B) Raw(s string) { b.sb = append(b.sb, s...) }

// String returns the accumulated text.
func (b *B) String() string { return string(b.sb) }

// ListHeader returns the one list header form of section 6.4, e.g.
// "# prs 20/134 page 1/7 next: page=2" (unknown total: "# prs 20 page 1 next: page=2").
func ListHeader(kind string, p Page) string {
	page := p.Page
	if page < 1 {
		page = 1
	}
	var sb strings.Builder
	sb.WriteString("# ")
	sb.WriteString(kind)
	sb.WriteByte(' ')
	if p.Total >= 0 {
		if p.Total == 0 {
			sb.WriteString("0/0")
			return sb.String()
		}
		pages := int64(1)
		if p.Limit > 0 {
			pages = (p.Total + int64(p.Limit) - 1) / int64(p.Limit)
		}
		if pages < 1 {
			pages = 1
		}
		fmt.Fprintf(&sb, "%d/%d page %d/%d", p.Shown, p.Total, page, pages)
	} else {
		fmt.Fprintf(&sb, "%d page %d", p.Shown, page)
	}
	if p.More {
		fmt.Fprintf(&sb, " next: page=%d", page+1)
	}
	return sb.String()
}

// Trunc cuts s to max runes (rune-safe) and appends
// "\n[truncated: N of M chars; <how>]".
func Trunc(s string, max int, how string) string {
	if max < 0 {
		max = 0
	}
	total := utf8.RuneCountInString(s)
	if total <= max {
		return s
	}
	cut := runePrefix(s, max)
	return fmt.Sprintf("%s\n[truncated: %d of %d chars; %s]", cut, max, total, how)
}

// runePrefix returns the first n runes of s.
func runePrefix(s string, n int) string {
	if n <= 0 {
		return ""
	}
	i := 0
	for count := 0; count < n && i < len(s); count++ {
		_, w := utf8.DecodeRuneInString(s[i:])
		i += w
	}
	return s[:i]
}

// OneLine collapses whitespace and cuts with "…" at max runes (the result,
// ellipsis included, has at most max runes). max <= 0 means no cut.
func OneLine(s string, max int) string {
	s = strings.Join(strings.FieldsFunc(s, unicode.IsSpace), " ")
	if max <= 0 || utf8.RuneCountInString(s) <= max {
		return s
	}
	return strings.TrimRightFunc(runePrefix(s, max-1), unicode.IsSpace) + "…"
}

// SHA returns the first n characters of s.
func SHA(s string, n int) string {
	if n >= len(s) {
		return s
	}
	return s[:n]
}

// Tags renders "[a,b]" or "" for an empty slice.
func Tags(xs []string) string {
	if len(xs) == 0 {
		return ""
	}
	return "[" + strings.Join(xs, ",") + "]"
}

// Logins renders "@a,@b" or "" for an empty slice.
func Logins(xs []string) string {
	if len(xs) == 0 {
		return ""
	}
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = "@" + x
	}
	return strings.Join(out, ",")
}

// Size renders a byte count with 1000-based units and one decimal
// ("512B", "1.2KB", "17.2MB").
func Size(n int64) string {
	if n < 1000 {
		return strconv.FormatInt(n, 10) + "B"
	}
	units := []string{"KB", "MB", "GB", "TB"}
	f := float64(n)
	u := -1
	for f >= 1000 && u < len(units)-1 {
		f /= 1000
		u++
	}
	return strconv.FormatFloat(f, 'f', 1, 64) + units[u]
}

// trailerPrefixes mark lines that must survive a cap.
var trailerPrefixes = []string{"[truncated:", "next_offset:", "resume:"}

func isTrailer(line string) bool {
	for _, p := range trailerPrefixes {
		if strings.HasPrefix(line, p) {
			return true
		}
	}
	return false
}

// splitTrailer separates the contiguous block of trailer lines at the end of
// s (ignoring trailing blank lines) from the body.
func splitTrailer(s string) (body []string, trailer []string) {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	i := len(lines)
	for i > 0 && isTrailer(lines[i-1]) {
		i--
	}
	return lines[:i], lines[i:]
}

func runeLen(s string) int { return utf8.RuneCountInString(s) }

// CapHead keeps the beginning of s within limit chars, cutting at a line
// boundary; trailer lines (prefix "[truncated:", "next_offset:", "resume:")
// at the end of s are re-appended after the cut so they always survive.
func CapHead(s string, limit int) string {
	if runeLen(s) <= limit {
		return s
	}
	body, trailer := splitTrailer(s)
	tr := strings.Join(trailer, "\n")
	// Reserve room for the marker line and the trailers.
	const markerReserve = 100
	budget := limit - markerReserve - runeLen(tr) - 1
	if budget < 0 {
		budget = 0
	}
	used, kept := 0, 0
	for _, l := range body {
		need := runeLen(l) + 1
		if used+need > budget {
			break
		}
		used += need
		kept++
	}
	var out []string
	if kept == 0 && len(body) > 0 {
		// Even the first line does not fit: cut it by characters.
		out = append(out, runePrefix(body[0], budget))
		kept = 1
	} else {
		out = append(out, body[:kept]...)
	}
	omitted := len(body) - kept
	out = append(out, fmt.Sprintf("[truncated: output cut at %d chars, %d lines omitted; narrow with limit/path/q]", limit, omitted))
	out = append(out, trailer...)
	return strings.Join(out, "\n")
}

// CapTail keeps the END of s within limit chars (marker first:
// "[truncated: first N lines omitted]").
func CapTail(s string, limit int) string {
	if runeLen(s) <= limit {
		return s
	}
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	const markerReserve = 50
	budget := limit - markerReserve
	if budget < 0 {
		budget = 0
	}
	used, start := 0, len(lines)
	for start > 0 {
		need := runeLen(lines[start-1]) + 1
		if used+need > budget {
			break
		}
		used += need
		start--
	}
	var tail []string
	if start == len(lines) {
		// The last line alone does not fit: keep its end.
		last := []rune(lines[len(lines)-1])
		if budget < len(last) {
			last = last[len(last)-budget:]
		}
		tail = []string{string(last)}
		start = len(lines) - 1
	} else {
		tail = lines[start:]
	}
	marker := fmt.Sprintf("[truncated: first %d lines omitted]", start)
	return marker + "\n" + strings.Join(tail, "\n")
}
