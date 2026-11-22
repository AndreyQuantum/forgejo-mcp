// Package gitremote detects the default owner/name from a git checkout
// (DESIGN.md section 3.1). It executes no git binary.
package gitremote

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// retryAfterFailure is how long a failed detection is cached.
const retryAfterFailure = 30 * time.Second

// Options configures a Detector with plain values (no dependency on config).
type Options struct {
	InstanceURL string   // FORGEJO_URL
	SSHHosts    []string // FORGEJO_SSH_HOSTS
	DefaultRepo string   // FORGEJO_REPO, "" when unset
	Dir         string   // FORGEJO_DIR, "" = process cwd
}

// Detector resolves repositories and the current branch.
type Detector struct {
	opts Options
	now  func() time.Time // tests replace it

	mu       sync.Mutex
	done     bool // a successful detection is cached for the process life
	owner    string
	name     string
	remote   string
	failErr  error
	failedAt time.Time
}

// NewDetector returns a Detector for the given options.
func NewDetector(opts Options) *Detector { return &Detector{opts: opts, now: time.Now} }

// Resolve returns owner, name and src (param|env|git:<remote>) for an
// explicit repo argument or the default repository.
func (d *Detector) Resolve(explicit string) (owner, name, src string, err error) {
	if explicit = strings.TrimSpace(explicit); explicit != "" {
		owner, name, err = d.parseExplicit(explicit)
		if err != nil {
			return "", "", "", err
		}
		return owner, name, "param", nil
	}
	if d.opts.DefaultRepo != "" {
		owner, name, err = splitOwnerName(d.opts.DefaultRepo)
		if err != nil {
			return "", "", "", fmt.Errorf("FORGEJO_REPO: %w", err)
		}
		return owner, name, "env", nil
	}
	o, n, remote, err := d.detect()
	if err != nil {
		return "", "", "", err
	}
	return o, n, "git:" + remote, nil
}

// GitRepo returns the git-detected repository (ok is false when none).
func (d *Detector) GitRepo() (owner, name string, ok bool) {
	o, n, _, err := d.detect()
	if err != nil {
		return "", "", false
	}
	return o, n, true
}

// CurrentBranch returns the checked-out branch ("" when detached or unknown).
// It is read from HEAD on every call because the branch changes at any time.
func (d *Detector) CurrentBranch() string {
	loc, err := d.locate()
	if err != nil {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(loc.gitDir, "HEAD"))
	if err != nil {
		return ""
	}
	s := strings.TrimSpace(string(b))
	if ref, ok := strings.CutPrefix(s, "ref:"); ok {
		if br, ok := strings.CutPrefix(strings.TrimSpace(ref), "refs/heads/"); ok {
			return br
		}
	}
	return ""
}

func splitOwnerName(s string) (owner, name string, err error) {
	s = strings.TrimSuffix(strings.Trim(strings.TrimSpace(s), "/"), ".git")
	parts := strings.Split(s, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid repo %q: want owner/name", s)
	}
	return parts[0], parts[1], nil
}

// instance returns the lower-case host (with port), hostname and sub-path of
// the configured instance URL.
func (d *Detector) instance() (host, hostname, subPath string) {
	u, err := url.Parse(d.opts.InstanceURL)
	if err != nil {
		return "", "", ""
	}
	return strings.ToLower(u.Host), strings.ToLower(u.Hostname()), strings.Trim(u.Path, "/")
}

func (d *Detector) parseExplicit(s string) (owner, name string, err error) {
	if strings.Contains(s, "://") {
		u, perr := url.Parse(s)
		if perr != nil || u.Host == "" {
			return "", "", errors.New("invalid repo: unparsable URL")
		}
		host, _, sub := d.instance()
		if !strings.EqualFold(u.Host, host) {
			return "", "", fmt.Errorf("invalid repo: URL host %s is not the instance host %s", u.Host, host)
		}
		p := strings.Trim(u.Path, "/")
		if sub != "" {
			rest, ok := cutPathPrefix(p, sub)
			if !ok {
				return "", "", fmt.Errorf("invalid repo: URL path is outside the instance path /%s", sub)
			}
			p = rest
		}
		segs := splitSegs(p)
		if len(segs) < 2 {
			return "", "", fmt.Errorf("invalid repo %q: want owner/name or a repository URL", s)
		}
		return segs[0], strings.TrimSuffix(segs[1], ".git"), nil
	}
	t := strings.TrimSuffix(strings.Trim(s, "/"), ".git")
	switch parts := strings.Split(t, "/"); {
	case len(parts) == 2 && parts[0] != "" && parts[1] != "":
		return parts[0], parts[1], nil
	case len(parts) == 1 && parts[0] != "":
		def, _, err := splitOwnerName(d.opts.DefaultRepo)
		if d.opts.DefaultRepo == "" || err != nil {
			def, _, _, err = d.detect()
			if err != nil {
				return "", "", fmt.Errorf("repo %q has no owner and there is no default repo to take it from", t)
			}
		}
		return def, parts[0], nil
	}
	return "", "", fmt.Errorf("invalid repo %q: want owner/name", s)
}

func splitSegs(p string) []string {
	var out []string
	for _, s := range strings.Split(p, "/") {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// cutPathPrefix removes the segment prefix prefix from p.
func cutPathPrefix(p, prefix string) (string, bool) {
	if p == prefix {
		return "", true
	}
	if rest, ok := strings.CutPrefix(p, prefix+"/"); ok {
		return rest, true
	}
	return p, false
}

// location is a found git checkout.
type location struct {
	gitDir    string // holds HEAD (the worktree gitdir for linked worktrees)
	commonDir string // holds config
}

func (d *Detector) startDir() (string, error) {
	if d.opts.Dir != "" {
		return filepath.Abs(d.opts.Dir)
	}
	return os.Getwd()
}

func (d *Detector) locate() (location, error) {
	dir, err := d.startDir()
	if err != nil {
		return location{}, err
	}
	for {
		g := filepath.Join(dir, ".git")
		if fi, err := os.Lstat(g); err == nil {
			if fi.Mode()&os.ModeSymlink != 0 {
				// .git may be a symlink to a directory: follow it
				if sfi, serr := os.Stat(g); serr == nil {
					fi = sfi
				}
			}
			if fi.IsDir() {
				return location{gitDir: g, commonDir: g}, nil
			}
			if loc, ok := followGitFile(g, dir); ok {
				return loc, nil
			}
			return location{}, errors.New("unreadable .git file")
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return location{}, errors.New("not inside a git checkout")
		}
		dir = parent
	}
}

// followGitFile resolves a `.git` file ("gitdir: <path>") and, for a linked
// worktree, the common dir that holds the config.
func followGitFile(file, dir string) (location, bool) {
	b, err := os.ReadFile(file)
	if err != nil {
		return location{}, false
	}
	line, _, _ := strings.Cut(strings.TrimSpace(string(b)), "\n")
	rest, ok := strings.CutPrefix(strings.TrimSpace(line), "gitdir:")
	if !ok {
		return location{}, false
	}
	gd := strings.TrimSpace(rest)
	if !filepath.IsAbs(gd) {
		gd = filepath.Join(dir, gd)
	}
	gd = filepath.Clean(gd)
	loc := location{gitDir: gd, commonDir: gd}
	if cb, err := os.ReadFile(filepath.Join(gd, "commondir")); err == nil {
		cd := strings.TrimSpace(string(cb))
		if cd != "" {
			if !filepath.IsAbs(cd) {
				cd = filepath.Join(gd, cd)
			}
			loc.commonDir = filepath.Clean(cd)
		}
	}
	return loc, true
}

// detect runs (or returns the cached result of) git-based detection.
func (d *Detector) detect() (owner, name, remote string, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.done {
		return d.owner, d.name, d.remote, nil
	}
	if d.failErr != nil && d.now().Sub(d.failedAt) < retryAfterFailure {
		return "", "", "", d.failErr
	}
	owner, name, remote, err = d.scan()
	if err != nil {
		d.failErr, d.failedAt = err, d.now()
		return "", "", "", err
	}
	d.done, d.owner, d.name, d.remote, d.failErr = true, owner, name, remote, nil
	return owner, name, remote, nil
}

func (d *Detector) scan() (owner, name, remote string, err error) {
	const base = "no default repo: pass repo=owner/name or set FORGEJO_REPO"
	loc, lerr := d.locate()
	if lerr != nil {
		return "", "", "", errors.New(base)
	}
	cfg, rerr := os.ReadFile(filepath.Join(loc.commonDir, "config"))
	if rerr != nil {
		return "", "", "", errors.New(base)
	}
	remotes := remotesFromConfig(string(cfg))
	var seen []string
	for _, rc := range remotes {
		for _, raw := range rc.URLs {
			r, perr := parseRemote(raw)
			if perr != nil {
				continue
			}
			if o, n, ok := d.match(r); ok {
				return o, n, rc.Name, nil
			}
		}
		if r, perr := parseRemote(rc.URLs[0]); perr == nil {
			seen = append(seen, fmt.Sprintf("%s(host %s)", rc.Name, r.Hostname))
		}
	}
	if len(seen) > 0 {
		return "", "", "", fmt.Errorf("%s; remotes seen: %s", base, strings.Join(seen, ", "))
	}
	return "", "", "", errors.New(base)
}

// match decides whether a remote points at the instance and extracts
// owner/name (section 3.1 steps e and f).
func (d *Detector) match(r remoteURL) (owner, name string, ok bool) {
	_, hostname, sub := d.instance()
	if hostname == "" {
		return "", "", false
	}
	web := r.Scheme == "http" || r.Scheme == "https"
	matched := r.Hostname == hostname
	if !matched {
		matched = d.sshHostMatch(r, web)
	}
	if !matched {
		return "", "", false
	}
	p := strings.Trim(r.Path, "/")
	stripped := false
	if web && sub != "" {
		rest, found := cutPathPrefix(p, sub)
		if !found {
			return "", "", false
		}
		p, stripped = rest, true
	}
	p = strings.TrimSuffix(p, ".git")
	segs := splitSegs(p)
	switch {
	case len(segs) == 2:
	case len(segs) > 2 && stripped:
	default:
		return "", "", false
	}
	return segs[0], strings.TrimSuffix(segs[1], ".git"), true
}

func (d *Detector) sshHostMatch(r remoteURL, web bool) bool {
	for _, h := range d.opts.SSHHosts {
		h = strings.ToLower(strings.TrimSpace(h))
		hn, port := h, ""
		if i := strings.LastIndex(h, ":"); i >= 0 && !strings.HasSuffix(h, "]") {
			hn, port = h[:i], h[i+1:]
		}
		hn = strings.Trim(hn, "[]")
		if hn == "" || hn != r.Hostname {
			continue
		}
		switch {
		case port == "":
			return true
		case web:
			// An https remote never speaks the ssh port.
			continue
		case r.Scheme == "scp":
			return true // scp-like URLs carry no port: any entry for the host
		default:
			rp := r.Port
			if rp == "" && r.Scheme == "ssh" {
				rp = "22"
			}
			if rp == port {
				return true
			}
		}
	}
	return false
}
