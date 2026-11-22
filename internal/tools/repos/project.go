package repos

// Projection structs of the repos area (DESIGN.md section 6.9): they carry
// only the JSON paths we print, so raw API bodies are dropped on decode.

type userRef struct {
	Login string `json:"login"`
}

type perms struct {
	Admin bool `json:"admin"`
	Push  bool `json:"push"`
	Pull  bool `json:"pull"`
}

type parentRef struct {
	FullName string `json:"full_name"`
}

// apiRepo is the repository object.
type apiRepo struct {
	ID             int64      `json:"id"`
	FullName       string     `json:"full_name"`
	Description    string     `json:"description"`
	Website        string     `json:"website"`
	Private        bool       `json:"private"`
	Fork           bool       `json:"fork"`
	Template       bool       `json:"template"`
	Mirror         bool       `json:"mirror"`
	Archived       bool       `json:"archived"`
	Empty          bool       `json:"empty"`
	DefaultBranch  string     `json:"default_branch"`
	StarsCount     int        `json:"stars_count"`
	ForksCount     int        `json:"forks_count"`
	OpenIssues     int        `json:"open_issues_count"`
	OpenPRs        int        `json:"open_pr_counter"`
	Size           int64      `json:"size"` // KiB
	Language       string     `json:"language"`
	UpdatedAt      string     `json:"updated_at"`
	HTMLURL        string     `json:"html_url"`
	CloneURL       string     `json:"clone_url"`
	SSHURL         string     `json:"ssh_url"`
	Topics         []string   `json:"topics"`
	Permissions    *perms     `json:"permissions"`
	Parent         *parentRef `json:"parent"`
	Owner          *userRef   `json:"owner"`
	HasIssues      bool       `json:"has_issues"`
	HasPulls       bool       `json:"has_pull_requests"`
	HasWiki        bool       `json:"has_wiki"`
	HasActions     bool       `json:"has_actions"`
	HasReleases    bool       `json:"has_releases"`
	HasPackages    bool       `json:"has_packages"`
	HasProjects    bool       `json:"has_projects"`
	MirrorInterval string     `json:"mirror_interval"`
	MirrorUpdated  string     `json:"mirror_updated"`
	AllowMerge     bool       `json:"allow_merge_commits"`
	AllowRebase    bool       `json:"allow_rebase"`
	AllowRebaseExp bool       `json:"allow_rebase_explicit"`
	AllowSquash    bool       `json:"allow_squash_merge"`
	AllowFFOnly    bool       `json:"allow_fast_forward_only_merge"`
	DefaultMerge   string     `json:"default_merge_style"`
	DefaultUpdate  string     `json:"default_update_style"`
	DeleteBranchPR bool       `json:"default_delete_branch_after_merge"`
}

// searchResult is the wrapped answer of GET /repos/search.
type searchResult struct {
	OK   bool      `json:"ok"`
	Data []apiRepo `json:"data"`
}

type topicList struct {
	Topics []string `json:"topics"`
}

type commitMeta struct {
	ID        string `json:"id"`
	SHA       string `json:"sha"`
	Message   string `json:"message"`
	Timestamp string `json:"timestamp"`
	Created   string `json:"created"`
}

// apiBranch is the branch object.
type apiBranch struct {
	Name              string     `json:"name"`
	Commit            commitMeta `json:"commit"`
	Protected         bool       `json:"protected"`
	UserCanPush       bool       `json:"user_can_push"`
	UserCanMerge      bool       `json:"user_can_merge"`
	RequiredApprovals int        `json:"required_approvals"`
	EnableStatusCheck bool       `json:"enable_status_check"`
	StatusContexts    []string   `json:"status_check_contexts"`
}

// apiTag is the tag object.
type apiTag struct {
	Name    string     `json:"name"`
	Message string     `json:"message"`
	ID      string     `json:"id"`
	Commit  commitMeta `json:"commit"`
}

type commitUser struct {
	Name  string `json:"name"`
	Date  string `json:"date"`
	Email string `json:"email"`
}

// annotatedTag is GET /git/tags/{sha}.
type annotatedTag struct {
	Tag     string      `json:"tag"`
	SHA     string      `json:"sha"`
	Message string      `json:"message"`
	Tagger  *commitUser `json:"tagger"`
	Object  struct {
		Type string `json:"type"`
		SHA  string `json:"sha"`
	} `json:"object"`
}

// tagProtection is a tag protection rule.
type tagProtection struct {
	ID        int64    `json:"id"`
	Pattern   string   `json:"name_pattern"`
	Usernames []string `json:"whitelist_usernames"`
	Teams     []string `json:"whitelist_teams"`
}

type wikiCommit struct {
	SHA       string      `json:"sha"`
	Message   string      `json:"message"`
	Author    *commitUser `json:"author"`
	Committer *commitUser `json:"commiter"` // sic: the API key is misspelled
}

// wikiMeta is one entry of the wiki page list.
type wikiMeta struct {
	Title      string      `json:"title"`
	SubURL     string      `json:"sub_url"`
	LastCommit *wikiCommit `json:"last_commit"`
}

// wikiPage is a full wiki page.
type wikiPage struct {
	Title       string      `json:"title"`
	SubURL      string      `json:"sub_url"`
	ContentB64  string      `json:"content_base64"`
	CommitCount int         `json:"commit_count"`
	LastCommit  *wikiCommit `json:"last_commit"`
}

type wikiRevisions struct {
	Commits []wikiCommit `json:"commits"`
	Count   int          `json:"count"`
}

type pushMirror struct {
	RemoteName   string `json:"remote_name"`
	RemoteAddr   string `json:"remote_address"`
	Interval     string `json:"interval"`
	SyncOnCommit bool   `json:"sync_on_commit"`
	LastUpdate   string `json:"last_update"`
	LastError    string `json:"last_error"`
	BranchFilter string `json:"branch_filter"`
	PublicKey    string `json:"public_key"`
}

type syncInfo struct {
	Allowed       bool   `json:"allowed"`
	ForkCommit    string `json:"fork_commit"`
	BaseCommit    string `json:"base_commit"`
	CommitsBehind int    `json:"commits_behind"`
}
