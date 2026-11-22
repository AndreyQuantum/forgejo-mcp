package packages

// pkg projects a Package object (section 6.9): only the paths we print.
type pkg struct {
	ID         int64  `json:"id"`
	Type       string `json:"type"`
	Name       string `json:"name"`
	Version    string `json:"version"`
	HTMLURL    string `json:"html_url"`
	CreatedAt  string `json:"created_at"`
	Owner      *login `json:"owner"`
	Creator    *login `json:"creator"`
	Repository *struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
}

type login struct {
	Login string `json:"login"`
}

// pkgFile projects a PackageFile. The API serialises Size without a json tag,
// so the key is "Size".
type pkgFile struct {
	ID     int64  `json:"id"`
	Size   int64  `json:"Size"`
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
}

func (p *pkg) repoName() string {
	if p.Repository == nil {
		return ""
	}
	return p.Repository.FullName
}
