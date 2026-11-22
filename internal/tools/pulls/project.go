package pulls

import "strings"

// Projection structs of the pull request area (DESIGN.md section 6.9). They
// declare only the JSON paths we use, so everything else in the (huge) API
// objects is dropped while decoding.

type userRef struct {
	Login string `json:"login"`
}

type repoRef struct {
	FullName string `json:"full_name"`
}

type branchRef struct {
	Label string   `json:"label"`
	Ref   string   `json:"ref"`
	SHA   string   `json:"sha"`
	Repo  *repoRef `json:"repo"`
}

type nameRef struct {
	Name string `json:"name"`
}

// pullReq is the projection of a pull request.
type pullReq struct {
	Number             int64     `json:"number"`
	Title              string    `json:"title"`
	State              string    `json:"state"`
	Draft              bool      `json:"draft"`
	Merged             bool      `json:"merged"`
	Mergeable          bool      `json:"mergeable"`
	User               *userRef  `json:"user"`
	Base               branchRef `json:"base"`
	Head               branchRef `json:"head"`
	Labels             []nameRef `json:"labels"`
	Assignees          []userRef `json:"assignees"`
	RequestedReviewers []userRef `json:"requested_reviewers"`
	Milestone          *titleRef `json:"milestone"`
	Comments           int       `json:"comments"`
	ReviewComments     int       `json:"review_comments"`
	Additions          int       `json:"additions"`
	Deletions          int       `json:"deletions"`
	ChangedFiles       int       `json:"changed_files"`
	CreatedAt          string    `json:"created_at"`
	UpdatedAt          string    `json:"updated_at"`
	ClosedAt           string    `json:"closed_at"`
	MergedAt           string    `json:"merged_at"`
	MergedBy           *userRef  `json:"merged_by"`
	MergeCommitSHA     string    `json:"merge_commit_sha"`
	DueDate            string    `json:"due_date"`
	HTMLURL            string    `json:"html_url"`
	Body               string    `json:"body"`
	IsLocked           bool      `json:"is_locked"`
	AllowMaintainer    bool      `json:"allow_maintainer_edit"`
}

type titleRef struct {
	Title string `json:"title"`
}

func (p *pullReq) author() string {
	if p.User == nil {
		return ""
	}
	return p.User.Login
}

func (p *pullReq) labelNames() []string {
	out := make([]string, 0, len(p.Labels))
	for _, l := range p.Labels {
		out = append(out, l.Name)
	}
	return out
}

func (p *pullReq) assigneeLogins() []string { return logins(p.Assignees) }

func (p *pullReq) requestedLogins() []string { return logins(p.RequestedReviewers) }

func logins(us []userRef) []string {
	out := make([]string, 0, len(us))
	for _, u := range us {
		out = append(out, u.Login)
	}
	return out
}

// stateWord is draft|open|merged|closed (merged PRs are `closed` + merged).
func (p *pullReq) stateWord() string {
	switch {
	case p.Merged:
		return "merged"
	case p.State == "closed":
		return "closed"
	case p.Draft:
		return "draft"
	}
	return "open"
}

// isOpen reports an open (possibly draft) PR.
func (p *pullReq) isOpen() bool { return !p.Merged && p.State != "closed" }

// headName is the head branch as shown (`head.label`, else `head.ref`).
func (p *pullReq) headName() string {
	if p.Head.Label != "" {
		return p.Head.Label
	}
	return p.Head.Ref
}

func (p *pullReq) baseName() string {
	if p.Base.Label != "" {
		return p.Base.Label
	}
	return p.Base.Ref
}

// forkRepo returns the head repository full name when it differs from the
// base repository (a fork PR), else "".
func (p *pullReq) forkRepo() string {
	if p.Head.Repo == nil || p.Base.Repo == nil {
		return ""
	}
	if strings.EqualFold(p.Head.Repo.FullName, p.Base.Repo.FullName) {
		return ""
	}
	return p.Head.Repo.FullName
}

func (p *pullReq) milestoneTitle() string {
	if p.Milestone == nil {
		return ""
	}
	return p.Milestone.Title
}

// changedFile is one entry of `GET R/pulls/{n}/files`.
type changedFile struct {
	Filename         string `json:"filename"`
	PreviousFilename string `json:"previous_filename"`
	Status           string `json:"status"`
	Additions        int    `json:"additions"`
	Deletions        int    `json:"deletions"`
}

// commitItem is one entry of `GET R/pulls/{n}/commits`.
type commitItem struct {
	SHA    string `json:"sha"`
	Commit struct {
		Message string `json:"message"`
		Author  struct {
			Name string `json:"name"`
			Date string `json:"date"`
		} `json:"author"`
	} `json:"commit"`
	Author *userRef `json:"author"`
}

// issueComment is one entry of `GET R/issues/{n}/comments`.
type issueComment struct {
	ID        int64    `json:"id"`
	User      *userRef `json:"user"`
	Body      string   `json:"body"`
	CreatedAt string   `json:"created_at"`
}

// branchInfo is the part of `GET R/branches/{b}` that explains a blocked merge.
type branchInfo struct {
	RequiredApprovals int64 `json:"required_approvals"`
}

// reviewComment is one inline review comment.
type reviewComment struct {
	ID               int64    `json:"id"`
	Path             string   `json:"path"`
	Position         int      `json:"position"`
	OriginalPosition int      `json:"original_position"`
	Body             string   `json:"body"`
	User             *userRef `json:"user"`
	Resolver         *userRef `json:"resolver"`
	DiffHunk         string   `json:"diff_hunk"`
}
