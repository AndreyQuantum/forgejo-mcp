package meta

import (
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/AndreyQuantum/forgejo-mcp/internal/config"
	"github.com/AndreyQuantum/forgejo-mcp/internal/endpoints"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools"
)

// policy implements the forgejo_api rules of DESIGN.md section 4 step 5: it
// is what makes FORGEJO_DENY, toolsets and read-only real controls for the
// generic escape hatch (they apply by ENDPOINT, not only by tool name).
type policy struct {
	reg *tools.Registry
}

// decision is the outcome of evaluating one forgejo_api call.
type decision struct {
	Method string
	Canon  string        // canonical path relative to the API root, e.g. /repos/o/r
	API    string        // "" (/api/v1) or "forgejo" (/api/forgejo/v1)
	Op     *endpoints.Op // matched index operation, nil when unknown
	Mode   tools.Mode
	Err    string // non-empty: refuse the call with this text ("error: ..." already included)
}

var (
	reScheme    = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*:`)
	reEncodedXS = regexp.MustCompile(`(?i)%(2e|2f|5c|00)`)

	// builtinDestructive are the DELETE templates that always need `confirm`
	// (section 4 step 5f), in addition to every Destructive Action.Endpoints.
	builtinDestructive = []string{
		"DELETE /repos/{owner}/{repo}",
		"DELETE /orgs/{org}",
		"DELETE /teams/{id}",
		"DELETE /admin/users/{username}",
		"DELETE /admin/unadopted/{owner}/{repo}",
		"DELETE /packages/{owner}/{type}/{name}/{version}",
		"DELETE /repos/{owner}/{repo}/issues/{index}",
	}

	reDestructiveFallback = regexp.MustCompile(`^/(repos/[^/]+/[^/]+|orgs/[^/]+|admin/users/[^/]+|packages(/.*)?)$`)

	renderingPosts = map[string]bool{"/markdown": true, "/markdown/raw": true, "/markup": true}
)

// canonicalize turns a user supplied path into the canonical path the policy
// evaluates and the request is built from (4 step 5a). api is "" or "forgejo".
func canonicalize(raw string) (canon, api string, err error) {
	for _, r := range raw {
		if r < 0x20 || r == 0x7f {
			return "", "", fmt.Errorf("invalid: path contains control characters")
		}
	}
	p := strings.TrimSpace(raw)
	switch {
	case p == "":
		return "", "", fmt.Errorf("invalid: path is required")
	case strings.ContainsAny(p, "?#"):
		return "", "", fmt.Errorf("invalid: path must not contain ? or # (use query)")
	case strings.Contains(p, `\`):
		return "", "", fmt.Errorf("invalid: path must not contain backslashes")
	case reScheme.MatchString(p) || strings.Contains(p, "://"):
		return "", "", fmt.Errorf("invalid: path must be relative to /api/v1, not an absolute URL")
	case reEncodedXS.MatchString(p):
		return "", "", fmt.Errorf("invalid: path must not contain encoded %%2e, %%2f, %%5c or %%00")
	}
	if !strings.HasPrefix(p, "/") {
		return "", "", fmt.Errorf("invalid: path must start with / (relative to /api/v1)")
	}
	p = decodeUnreserved(p)
	segs := make([]string, 0, 8)
	for _, s := range strings.Split(p, "/") {
		switch s {
		case "":
			continue
		case ".", "..":
			return "", "", fmt.Errorf("invalid: path must not contain . or .. segments")
		}
		segs = append(segs, s)
	}
	switch {
	case len(segs) >= 2 && segs[0] == "api" && segs[1] == "v1":
		segs = segs[2:]
	case len(segs) >= 3 && segs[0] == "api" && segs[1] == "forgejo" && segs[2] == "v1":
		segs, api = segs[3:], "forgejo"
	case len(segs) >= 2 && segs[0] == "forgejo" && segs[1] == "v1":
		segs, api = segs[2:], "forgejo"
	}
	if len(segs) == 0 {
		return "", "", fmt.Errorf("invalid: path is empty after normalisation")
	}
	return "/" + strings.Join(segs, "/"), api, nil
}

// matchMethod maps HEAD to GET: the index has no HEAD operations, and HEAD
// is served by the GET route.
func matchMethod(m string) string {
	if m == http.MethodHead {
		return http.MethodGet
	}
	return m
}

// decodeUnreserved decodes percent-escapes of RFC 3986 unreserved characters
// (ALPHA, DIGIT, "-", ".", "_", "~"; section 6.2.2.2) so that an encoded
// literal such as /rep%6Fs cannot dodge index matching, deny rules or the
// destructive checks. A decoded "." is still caught by the "."/".." check.
func decodeUnreserved(p string) string {
	if !strings.Contains(p, "%") {
		return p
	}
	var b strings.Builder
	for i := 0; i < len(p); i++ {
		if p[i] == '%' && i+2 < len(p) && isHex(p[i+1]) && isHex(p[i+2]) {
			c := unhex(p[i+1])<<4 | unhex(p[i+2])
			if isUnreserved(c) {
				b.WriteByte(c)
				i += 2
				continue
			}
		}
		b.WriteByte(p[i])
	}
	return b.String()
}

func isHex(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

func unhex(c byte) byte {
	switch {
	case c >= 'a':
		return c - 'a' + 10
	case c >= 'A':
		return c - 'A' + 10
	}
	return c - '0'
}

func isUnreserved(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
		c == '-' || c == '.' || c == '_' || c == '~'
}

// destructiveSet returns the "METHOD template" keys that need `confirm`.
func (p *policy) destructiveSet() map[string]bool {
	set := map[string]bool{}
	for _, k := range builtinDestructive {
		set[k] = true
	}
	if p.reg == nil {
		return set
	}
	for _, s := range p.reg.Specs() {
		for _, a := range s.Actions {
			if a.Mode != tools.Destructive {
				continue
			}
			for _, ep := range a.Endpoints {
				if strings.HasPrefix(ep, "DELETE ") {
					set[ep] = true
				}
			}
		}
	}
	return set
}

// isDestructive reports whether a DELETE on this canonical path is destructive.
func (p *policy) isDestructive(method string, op *endpoints.Op, canon string) bool {
	if method != http.MethodDelete {
		return false
	}
	if op != nil {
		return p.destructiveSet()[op.Key()]
	}
	return reDestructiveFallback.MatchString(canon)
}

// classify returns the Mode of a call by its params (Action.ModeFor).
func (p *policy) classify(method, rawPath string) tools.Mode {
	method = strings.ToUpper(strings.TrimSpace(method))
	switch method {
	case http.MethodGet, http.MethodHead:
		return tools.Read
	}
	canon, api, err := canonicalize(rawPath)
	if err != nil {
		return tools.Write
	}
	var op *endpoints.Op
	if api == "" {
		op = endpoints.Match(matchMethod(method), canon)
	}
	if method == http.MethodPost && api == "" && renderingPosts[canon] {
		return tools.Read
	}
	if p.isDestructive(method, op, canon) {
		return tools.Destructive
	}
	return tools.Write
}

// denyReason names why a "tool.action" is blocked: FORGEJO_DENY or a
// disabled toolset.
func (p *policy) denyReason(owner string) string {
	tool, _, _ := strings.Cut(owner, ".")
	if p.reg != nil {
		on := p.reg.EnabledToolsets()
		for _, s := range p.reg.Specs() {
			if s.Name == tool && on != nil && !on[s.Toolset] {
				return "toolset off"
			}
		}
	}
	return "FORGEJO_DENY"
}

// Deviation from DESIGN.md 4.5e (deliberate): the "unmatched non-GET is
// refused when any toolset is off" rule ignores the admin toolset, which is
// off by default; otherwise every unmatched write (including /forgejo/v1)
// would be refused in the default config. Admin paths are covered by the
// /admin/ prefix rule instead.
//
// toolsetOffExceptAdmin reports whether any toolset other than admin (which
// is off by default and has its own /admin/ rule) is switched off.
func (p *policy) toolsetOffExceptAdmin() bool {
	if p.reg == nil {
		return false
	}
	on := p.reg.EnabledToolsets()
	if on == nil {
		return false
	}
	for _, ts := range tools.Toolsets {
		if ts != "admin" && !on[ts] {
			return true
		}
	}
	return false
}

func adminOff(on map[string]bool) bool { return on != nil && !on["admin"] }

// evaluate applies the policy of section 4 step 5 to one call. confirm is the
// caller's `confirm` value (compared with the canonical path for destructive
// DELETEs).
func (p *policy) evaluate(cfg *config.Config, method, rawPath, confirm string) decision {
	d := decision{Method: strings.ToUpper(strings.TrimSpace(method))}
	if !slices.Contains([]string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD"}, d.Method) {
		d.Err = fmt.Sprintf("error: invalid: method %q; valid: GET,POST,PUT,PATCH,DELETE,HEAD", method)
		return d
	}
	canon, api, err := canonicalize(rawPath)
	if err != nil {
		d.Err = "error: " + err.Error()
		return d
	}
	d.Canon, d.API = canon, api
	if api == "" {
		d.Op = endpoints.Match(matchMethod(d.Method), canon)
	}
	readLike := d.Method == http.MethodGet || d.Method == http.MethodHead
	d.Mode = tools.Write
	if readLike {
		d.Mode = tools.Read
	}

	var on map[string]bool
	if p.reg != nil {
		on = p.reg.EnabledToolsets()
	}

	// c. deny / toolset off, by endpoint.
	if d.Op != nil && p.reg != nil {
		if owner, ok := p.reg.DeniedEndpoints()[d.Op.Key()]; ok {
			d.Err = fmt.Sprintf("error: forgejo_api: blocked by %s (%s)", p.denyReason(owner), owner)
			return d
		}
	}
	if strings.HasPrefix(canon, "/admin/") && adminOff(on) {
		d.Err = "error: forgejo_api: blocked by toolset off (admin)"
		return d
	}

	// d. read-only.
	if cfg != nil && cfg.ReadOnly {
		switch {
		case readLike:
			if p.sideEffectGet(d) {
				d.Err = "error: forgejo_api: read-only mode blocks GET " + canon + " (it has side effects)"
				return d
			}
		case d.Method == http.MethodPost && api == "" && renderingPosts[canon]:
			d.Mode = tools.Read
		default:
			d.Err = fmt.Sprintf("error: forgejo_api: read-only mode allows only GET/HEAD (got %s %s)", d.Method, canon)
			return d
		}
	}

	// e. unknown op.
	if d.Op == nil && !readLike {
		deny := cfg != nil && len(cfg.Deny) > 0
		if deny || p.toolsetOffExceptAdmin() {
			d.Err = "error: forgejo_api: path not in the endpoint index; deny rules cannot be applied"
			return d
		}
	}

	// f. destructive confirm.
	if p.isDestructive(d.Method, d.Op, canon) {
		d.Mode = tools.Destructive
		if confirm != canon {
			d.Err = fmt.Sprintf("error: destructive: pass confirm=%q", canon)
			return d
		}
	}
	return d
}

// sideEffectGet reports GETs that change state (registration tokens).
func (p *policy) sideEffectGet(d decision) bool {
	if strings.HasSuffix(d.Canon, "/registration-token") {
		return true
	}
	if d.Op != nil && p.reg != nil {
		return slices.Contains(p.reg.SideEffectGets(), d.Op.Key())
	}
	return false
}
