package packages

import (
	"fmt"
	"strings"

	"github.com/AndreyQuantum/forgejo-mcp/internal/format"
)

// listLine renders `npm foo 1.0.2 T repo:o/r`.
func listLine(p *pkg) string {
	s := fmt.Sprintf("%s %s %s", p.Type, p.Name, p.Version)
	if t := format.T(p.CreatedAt); t != "" {
		s += " " + t
	}
	if r := p.repoName(); r != "" {
		s += " repo:" + r
	}
	return s
}

// renderOne renders a single package.
func renderOne(p *pkg) string {
	var b format.B
	b.Raw(fmt.Sprintf("package %s %s %s\n", p.Type, p.Name, p.Version))
	if p.Owner != nil {
		b.KV("owner", p.Owner.Login)
	}
	b.KV("repo", p.repoName())
	if p.Creator != nil {
		b.KV("by", p.Creator.Login)
	}
	b.KV("created", format.T(p.CreatedAt))
	b.KV("url", p.HTMLURL)
	return b.String()
}

// fileLine renders `file 4 | name | 1.2KB | sha256:abcdef012345`.
func fileLine(f *pkgFile) string {
	var b format.B
	b.Line(fmt.Sprintf("file %d", f.ID), f.Name, format.Size(f.Size), sha(f.SHA256))
	return strings.TrimRight(b.String(), "\n")
}

func sha(h string) string {
	if h == "" {
		return ""
	}
	return "sha256:" + format.SHA(h, 12)
}
