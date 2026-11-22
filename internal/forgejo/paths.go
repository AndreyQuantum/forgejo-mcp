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
func EscapeKeepPct(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '%' && i+2 < len(s) && isHex(s[i+1]) && isHex(s[i+2]):
			b.WriteString(s[i : i+3])
			i += 2
		case c == '/', isUnreserved(c), strings.IndexByte("!$&'()*+,;=:@", c) >= 0:
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&15])
		}
	}
	return b.String()
}

func isHex(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

func isUnreserved(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '.' || c == '_' || c == '~'
}
