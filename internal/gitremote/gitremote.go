// Package gitremote detects the default owner/name from a git checkout
// (DESIGN.md section 3.1). It executes no git binary. WP1 implements it.
package gitremote

// Options configures a Detector with plain values (no dependency on config).
type Options struct {
	InstanceURL string   // FORGEJO_URL
	SSHHosts    []string // FORGEJO_SSH_HOSTS
	DefaultRepo string   // FORGEJO_REPO, "" when unset
	Dir         string   // FORGEJO_DIR, "" = process cwd
}

// Detector resolves repositories and the current branch.
type Detector struct{ opts Options }

// NewDetector returns a Detector for the given options.
func NewDetector(opts Options) *Detector { return &Detector{opts: opts} }

// Resolve returns owner, name and src (param|env|git:<remote>) for an
// explicit repo argument or the default repository.
func (d *Detector) Resolve(explicit string) (owner, name, src string, err error) {
	panic("WP1")
}

// GitRepo returns the git-detected repository (ok is false when none).
func (d *Detector) GitRepo() (owner, name string, ok bool) { panic("WP1") }

// CurrentBranch returns the checked-out branch ("" when detached or unknown).
func (d *Detector) CurrentBranch() string { panic("WP1") }

// ParseRemoteURL extracts the host and path of a git remote URL.
func ParseRemoteURL(raw string) (host, path string, err error) { panic("WP1") }
