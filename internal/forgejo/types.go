package forgejo

// Projection structs shared across areas (section 6.9). Only the fields we
// use are declared; JSON decoding drops everything else.

// User is a Forgejo account.
type User struct {
	ID       int64  `json:"id"`
	Login    string `json:"login"`
	FullName string `json:"full_name"`
	Email    string `json:"email"`
	IsAdmin  bool   `json:"is_admin"`
	Active   bool   `json:"active"`
	Created  string `json:"created"`
}

// Permission is the caller's permission on a repo.
type Permission struct {
	Admin bool `json:"admin"`
	Push  bool `json:"push"`
	Pull  bool `json:"pull"`
}

// Repo is the repository object with the fields every area needs (also the
// cached repo meta of section 5.5).
type Repo struct {
	ID            int64       `json:"id"`
	FullName      string      `json:"full_name"`
	Name          string      `json:"name"`
	Owner         *User       `json:"owner"`
	Description   string      `json:"description"`
	Private       bool        `json:"private"`
	Fork          bool        `json:"fork"`
	Mirror        bool        `json:"mirror"`
	Template      bool        `json:"template"`
	Archived      bool        `json:"archived"`
	Empty         bool        `json:"empty"`
	DefaultBranch string      `json:"default_branch"`
	HTMLURL       string      `json:"html_url"`
	Permissions   *Permission `json:"permissions"`

	AllowMergeCommits             bool   `json:"allow_merge_commits"`
	AllowRebase                   bool   `json:"allow_rebase"`
	AllowRebaseExplicit           bool   `json:"allow_rebase_explicit"`
	AllowSquashMerge              bool   `json:"allow_squash_merge"`
	AllowFastForwardOnlyMerge     bool   `json:"allow_fast_forward_only_merge"`
	DefaultMergeStyle             string `json:"default_merge_style"`
	DefaultDeleteBranchAfterMerge bool   `json:"default_delete_branch_after_merge"`
}

// Label is a repo or org label.
type Label struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Color       string `json:"color"`
	Description string `json:"description"`
	Exclusive   bool   `json:"exclusive"`
	IsArchived  bool   `json:"is_archived"`
}

// Milestone is a repo milestone.
type Milestone struct {
	ID           int64  `json:"id"`
	Title        string `json:"title"`
	State        string `json:"state"`
	OpenIssues   int    `json:"open_issues"`
	ClosedIssues int    `json:"closed_issues"`
	DueOn        string `json:"due_on"`
	Description  string `json:"description"`
}

// Status is a commit status.
type Status struct {
	ID          int64  `json:"id"`
	Status      string `json:"status"`
	Context     string `json:"context"`
	Description string `json:"description"`
	TargetURL   string `json:"target_url"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}
