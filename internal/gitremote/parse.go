package gitremote

import (
	"errors"
	"net/url"
	"sort"
	"strings"
)

// remoteURL is a parsed git remote URL. Credentials are never kept.
type remoteURL struct {
	Scheme   string // http, https, ssh, git, or "scp" for the scp-like form
	Hostname string // lower-case, no brackets, no port
	Port     string // "" when absent
	Path     string // without the leading "/"
}

// ParseRemoteURL extracts the host and path of a git remote URL. The host is
// lower-case and includes ":port" when the URL has one; the path has no
// leading "/". Credentials in the URL are discarded. Supported forms:
// http(s)://, ssh://, git://, and scp-like [user@]host:path.
func ParseRemoteURL(raw string) (host, path string, err error) {
	r, err := parseRemote(raw)
	if err != nil {
		return "", "", err
	}
	host = r.Hostname
	if strings.Contains(host, ":") { // IPv6 literal
		host = "[" + host + "]"
	}
	if r.Port != "" {
		host += ":" + r.Port
	}
	return host, r.Path, nil
}

func parseRemote(raw string) (remoteURL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return remoteURL{}, errors.New("empty remote URL")
	}
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil {
			// url.Error embeds the raw URL (possibly with credentials): drop it.
			return remoteURL{}, errors.New("unparsable remote URL")
		}
		scheme := strings.ToLower(u.Scheme)
		switch scheme {
		case "http", "https", "ssh", "git", "git+ssh", "ssh+git":
		default:
			return remoteURL{}, errors.New("unsupported remote URL scheme " + scheme)
		}
		if scheme == "git+ssh" || scheme == "ssh+git" {
			scheme = "ssh"
		}
		if u.Hostname() == "" {
			return remoteURL{}, errors.New("remote URL has no host")
		}
		return remoteURL{
			Scheme:   scheme,
			Hostname: strings.ToLower(u.Hostname()),
			Port:     u.Port(),
			Path:     strings.TrimLeft(u.Path, "/"),
		}, nil
	}
	// scp-like: [user@]host:path with the colon before the first slash.
	colon := strings.Index(raw, ":")
	if colon <= 0 {
		return remoteURL{}, errors.New("not a remote URL")
	}
	if slash := strings.Index(raw, "/"); slash >= 0 && slash < colon {
		return remoteURL{}, errors.New("not a remote URL")
	}
	hostPart, path := raw[:colon], raw[colon+1:]
	if at := strings.LastIndex(hostPart, "@"); at >= 0 {
		hostPart = hostPart[at+1:]
	}
	hostPart = strings.Trim(hostPart, "[]")
	if hostPart == "" || strings.ContainsAny(hostPart, `\ `) {
		return remoteURL{}, errors.New("not a remote URL")
	}
	if len(hostPart) == 1 { // a Windows drive letter such as C:\repo
		return remoteURL{}, errors.New("local path, not a remote URL")
	}
	return remoteURL{
		Scheme:   "scp",
		Hostname: strings.ToLower(hostPart),
		Path:     strings.TrimLeft(path, "/"),
	}, nil
}

// iniSection is one `[name "sub"]` block of a git config file.
type iniSection struct {
	Name string // lower-case, e.g. "remote", "url"
	Sub  string // case-sensitive subsection, e.g. "origin"
	Keys map[string][]string
}

// parseGitConfig parses the INI subset of a git config file: sections with
// optional quoted subsections, key = value pairs (multi-valued keys keep every
// value), # and ; comments, surrounding quotes. `include` is not followed.
func parseGitConfig(data string) []iniSection {
	var out []iniSection
	var cur *iniSection
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		if line[0] == '[' {
			end := strings.Index(line, "]")
			if end < 0 {
				cur = nil
				continue
			}
			head := strings.TrimSpace(line[1:end])
			sec := iniSection{Keys: map[string][]string{}}
			if q := strings.Index(head, `"`); q >= 0 {
				sec.Name = strings.ToLower(strings.TrimSpace(head[:q]))
				sub := head[q+1:]
				if e := strings.LastIndex(sub, `"`); e >= 0 {
					sub = sub[:e]
				}
				sec.Sub = strings.NewReplacer(`\"`, `"`, `\\`, `\`).Replace(sub)
			} else if dot := strings.Index(head, "."); dot >= 0 { // [remote.origin] legacy form
				sec.Name, sec.Sub = strings.ToLower(head[:dot]), head[dot+1:]
			} else {
				sec.Name = strings.ToLower(head)
			}
			out = append(out, sec)
			cur = &out[len(out)-1]
			// `[section] key = value` on one line.
			if rest := strings.TrimSpace(line[end+1:]); rest != "" && rest[0] != '#' && rest[0] != ';' {
				addKey(cur, rest)
			}
			continue
		}
		if cur != nil {
			addKey(cur, line)
		}
	}
	return out
}

func addKey(s *iniSection, line string) {
	k, v, found := strings.Cut(line, "=")
	k = strings.ToLower(strings.TrimSpace(k))
	if k == "" {
		return
	}
	if !found {
		s.Keys[k] = append(s.Keys[k], "true")
		return
	}
	s.Keys[k] = append(s.Keys[k], cleanValue(v))
}

// cleanValue strips surrounding whitespace, an unquoted trailing comment and
// double quotes.
func cleanValue(v string) string {
	v = strings.TrimSpace(v)
	var b strings.Builder
	inQ := false
	for i := 0; i < len(v); i++ {
		c := v[i]
		switch {
		case c == '\\' && i+1 < len(v):
			i++
			b.WriteByte(v[i])
		case c == '"':
			inQ = !inQ
		case !inQ && (c == '#' || c == ';'):
			return strings.TrimSpace(b.String())
		default:
			b.WriteByte(c)
		}
	}
	return strings.TrimSpace(b.String())
}

type rewrite struct{ from, to string }

// applyInsteadOf rewrites raw with the longest matching `insteadOf` prefix.
func applyInsteadOf(raw string, rules []rewrite) string {
	best := -1
	for i, r := range rules {
		if r.from != "" && strings.HasPrefix(raw, r.from) && (best < 0 || len(r.from) > len(rules[best].from)) {
			best = i
		}
	}
	if best < 0 {
		return raw
	}
	return rules[best].to + raw[len(rules[best].from):]
}

// remoteCandidate is one remote with the URLs to try, in order.
type remoteCandidate struct {
	Name string
	URLs []string
}

// remotesFromConfig returns remotes ordered origin, upstream, then the rest
// alphabetically, with insteadOf applied (pushInsteadOf adds a second URL).
func remotesFromConfig(data string) []remoteCandidate {
	secs := parseGitConfig(data)
	var insteadOf, pushInsteadOf []rewrite
	for _, s := range secs {
		if s.Name != "url" || s.Sub == "" {
			continue
		}
		for _, f := range s.Keys["insteadof"] {
			insteadOf = append(insteadOf, rewrite{from: f, to: s.Sub})
		}
		for _, f := range s.Keys["pushinsteadof"] {
			pushInsteadOf = append(pushInsteadOf, rewrite{from: f, to: s.Sub})
		}
	}
	var out []remoteCandidate
	for _, s := range secs {
		if s.Name != "remote" || s.Sub == "" {
			continue
		}
		c := remoteCandidate{Name: s.Sub}
		for _, u := range s.Keys["url"] {
			fetch := applyInsteadOf(u, insteadOf)
			c.URLs = append(c.URLs, fetch)
			if push := applyInsteadOf(u, pushInsteadOf); push != u && push != fetch {
				c.URLs = append(c.URLs, push)
			}
		}
		if len(c.URLs) > 0 {
			out = append(out, c)
		}
	}
	rank := func(n string) int {
		switch n {
		case "origin":
			return 0
		case "upstream":
			return 1
		}
		return 2
	}
	sort.SliceStable(out, func(i, j int) bool {
		ri, rj := rank(out[i].Name), rank(out[j].Name)
		if ri != rj {
			return ri < rj
		}
		return out[i].Name < out[j].Name
	})
	return out
}
