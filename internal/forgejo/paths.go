package forgejo

import (
	"net/url"
	"strings"
)

const wildMark = "\x00"

// Seg escapes one path segment ('/' becomes %2F).
func Seg(s string) string { return url.PathEscape(s) }

// Wild marks a multi-segment part (file path, branch with '/') so Path keeps
// its '/' separators while escaping every segment.
func Wild(s string) string { return wildMark + s }

// Path returns "/" + escaped parts joined by "/".
func Path(parts ...string) string {
	var b strings.Builder
	for _, p := range parts {
		b.WriteByte('/')
		if rest, ok := strings.CutPrefix(p, wildMark); ok {
			for i, s := range strings.Split(rest, "/") {
				if i > 0 {
					b.WriteByte('/')
				}
				b.WriteString(url.PathEscape(s))
			}
			continue
		}
		b.WriteString(url.PathEscape(p))
	}
	return b.String()
}

// EscapeKeepPct escapes per segment keeping '/', but leaves valid %XX
// sequences as given (no %20 -> %2520). Used by forgejo_api only.
func EscapeKeepPct(s string) string { panic("WP2") }
