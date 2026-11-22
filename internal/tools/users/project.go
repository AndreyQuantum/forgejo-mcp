package users

// Projection structs of the users area (DESIGN.md 6.9): only the JSON paths
// the renderers use are declared, so raw bodies are dropped on decode.

// account is a user or an organization in the user-shaped API objects.
type account struct {
	ID         int64  `json:"id"`
	Login      string `json:"login"`
	FullName   string `json:"full_name"`
	Email      string `json:"email"`
	IsAdmin    bool   `json:"is_admin"`
	Active     bool   `json:"active"`
	Created    string `json:"created"`
	LastLogin  string `json:"last_login"`
	Visibility string `json:"visibility"`
	Website    string `json:"website"`
	Location   string `json:"location"`
	Desc       string `json:"description"`
	Followers  int    `json:"followers_count"`
	Following  int    `json:"following_count"`
	Starred    int    `json:"starred_repos_count"`
}

// repoItem is a repository in a list (starred, team repos).
type repoItem struct {
	FullName    string `json:"full_name"`
	Description string `json:"description"`
	Private     bool   `json:"private"`
	Fork        bool   `json:"fork"`
	Mirror      bool   `json:"mirror"`
	Archived    bool   `json:"archived"`
	Empty       bool   `json:"empty"`
	Stars       int    `json:"stars_count"`
	Updated     string `json:"updated_at"`
}

// sshKey is a public key of an account.
type sshKey struct {
	ID          int64  `json:"id"`
	Title       string `json:"title"`
	Fingerprint string `json:"fingerprint"`
	KeyType     string `json:"key_type"`
	ReadOnly    bool   `json:"read_only"`
	Created     string `json:"created_at"`
}

// accessToken is a personal access token; Sha1 is the secret, present only
// in the answer of the create call.
type accessToken struct {
	ID           int64      `json:"id"`
	Name         string     `json:"name"`
	Sha1         string     `json:"sha1"`
	LastEight    string     `json:"token_last_eight"`
	Scopes       []string   `json:"scopes"`
	Created      string     `json:"created_at"`
	Repositories []repoMeta `json:"repositories"`
}

// repoMeta is the small repository object embedded in a token.
type repoMeta struct {
	FullName string `json:"full_name"`
	Name     string `json:"name"`
	Owner    string `json:"owner"`
}

// org is an organization.
type org struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Username    string `json:"username"`
	FullName    string `json:"full_name"`
	Visibility  string `json:"visibility"`
	Description string `json:"description"`
	Website     string `json:"website"`
	Location    string `json:"location"`
	Email       string `json:"email"`
	Created     string `json:"created"`
}

func (o org) login() string {
	if o.Name != "" {
		return o.Name
	}
	return o.Username
}

// team is an organization team.
type team struct {
	ID                      int64             `json:"id"`
	Name                    string            `json:"name"`
	Description             string            `json:"description"`
	Permission              string            `json:"permission"`
	IncludesAllRepositories bool              `json:"includes_all_repositories"`
	CanCreateOrgRepo        bool              `json:"can_create_org_repo"`
	UnitsMap                map[string]string `json:"units_map"`
	Organization            *org              `json:"organization"`
}

// thread is a notification thread.
type thread struct {
	ID         int64  `json:"id"`
	Unread     bool   `json:"unread"`
	Pinned     bool   `json:"pinned"`
	UpdatedAt  string `json:"updated_at"`
	Repository *struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	Subject *struct {
		Title   string `json:"title"`
		URL     string `json:"url"`
		HTMLURL string `json:"html_url"`
		Type    string `json:"type"`
		State   string `json:"state"`
	} `json:"subject"`
}
