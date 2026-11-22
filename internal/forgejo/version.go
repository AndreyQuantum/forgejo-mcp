package forgejo

// Version is a parsed server version (suffixes like "+gitea-1.22.0" ignored).
type Version struct {
	Major, Minor, Patch int
	Raw                 string
}

// ParseVersion parses `^v?(\d+)\.(\d+)\.(\d+)`.
func ParseVersion(s string) (Version, error) { panic("WP2") }

// Atleast reports whether the server major is >= major.
func (v Version) Atleast(major int) bool { return v.Major >= major }

// String returns the raw version text.
func (v Version) String() string { return v.Raw }
