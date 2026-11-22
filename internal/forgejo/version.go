package forgejo

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var reVersion = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)`)

// Version is a parsed server version (suffixes like "+gitea-1.22.0" ignored).
type Version struct {
	Major, Minor, Patch int
	Raw                 string
}

// ParseVersion parses `^v?(\d+)\.(\d+)\.(\d+)`.
func ParseVersion(s string) (Version, error) {
	s = strings.TrimSpace(s)
	m := reVersion.FindStringSubmatch(s)
	if m == nil {
		return Version{}, fmt.Errorf("cannot parse server version %q", s)
	}
	v := Version{Raw: s}
	v.Major, _ = strconv.Atoi(m[1])
	v.Minor, _ = strconv.Atoi(m[2])
	v.Patch, _ = strconv.Atoi(m[3])
	return v, nil
}

// Atleast reports whether the server major is >= major.
func (v Version) Atleast(major int) bool { return v.Major >= major }

// String returns the raw version text.
func (v Version) String() string { return v.Raw }
