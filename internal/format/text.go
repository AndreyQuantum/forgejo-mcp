// Package format holds pure output helpers (DESIGN.md section 6). It imports
// nothing internal. WP0 defines signatures; WP3 fills the bodies.
package format

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

// KV writes "key: val\n"; zero values are skipped; multi-line values are
// indented two spaces.
func (b *B) KV(key string, val any) { panic("WP3") }

// Line joins the non-empty parts with " | " and ends the line.
func (b *B) Line(parts ...string) { panic("WP3") }

// Raw appends s unchanged.
func (b *B) Raw(s string) { b.sb = append(b.sb, s...) }

// String returns the accumulated text.
func (b *B) String() string { return string(b.sb) }

// ListHeader returns the one list header form of section 6.4, e.g.
// "# prs 20/134 page 1/7 next: page=2" (unknown total: "# prs 20 page 1 next: page=2").
func ListHeader(kind string, p Page) string { panic("WP3") }

// Trunc cuts s to max runes (rune-safe) and appends
// "\n[truncated: N of M chars; <how>]".
func Trunc(s string, max int, how string) string { panic("WP3") }

// OneLine collapses whitespace and cuts with "…" at max runes.
func OneLine(s string, max int) string { panic("WP3") }

// SHA returns the first n characters of s.
func SHA(s string, n int) string {
	if n >= len(s) {
		return s
	}
	return s[:n]
}

// Tags renders "[a,b]" or "" for an empty slice.
func Tags(xs []string) string { panic("WP3") }

// Logins renders "@a,@b" or "" for an empty slice.
func Logins(xs []string) string { panic("WP3") }

// CapHead keeps the beginning of s within limit chars, cutting at a line
// boundary; trailer lines (prefix "[truncated:", "next_offset:", "resume:")
// are re-appended after the cut so they always survive.
func CapHead(s string, limit int) string { panic("WP3") }

// CapTail keeps the END of s within limit chars (marker first:
// "[truncated: first N lines omitted]").
func CapTail(s string, limit int) string { panic("WP3") }
