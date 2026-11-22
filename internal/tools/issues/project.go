package issues

import "github.com/AndreyQuantum/forgejo-mcp/internal/forgejo"

// Projection structs (DESIGN.md 6.9): only the JSON paths we print are
// declared, so the rest of an API object is dropped while decoding.

type login struct {
	Login string `json:"login"`
}

type asset struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	Size          int64  `json:"size"`
	DownloadCount int64  `json:"download_count"`
	URL           string `json:"browser_download_url"`
	Type          string `json:"type"`
}

type issueMilestone struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
	State string `json:"state"`
}

type pullInfo struct {
	Merged bool `json:"merged"`
	Draft  bool `json:"draft"`
}

type repoRef struct {
	FullName string `json:"full_name"`
}

// issue is the projection of an Issue (or a PR seen as an issue).
type issue struct {
	ID          int64           `json:"id"`
	Number      int64           `json:"number"`
	Title       string          `json:"title"`
	Body        string          `json:"body"`
	State       string          `json:"state"`
	User        *login          `json:"user"`
	Assignees   []login         `json:"assignees"`
	Labels      []forgejo.Label `json:"labels"`
	Milestone   *issueMilestone `json:"milestone"`
	Comments    int             `json:"comments"`
	CreatedAt   string          `json:"created_at"`
	UpdatedAt   string          `json:"updated_at"`
	ClosedAt    string          `json:"closed_at"`
	DueDate     string          `json:"due_date"`
	IsLocked    bool            `json:"is_locked"`
	Ref         string          `json:"ref"`
	HTMLURL     string          `json:"html_url"`
	PullRequest *pullInfo       `json:"pull_request"`
	Assets      []asset         `json:"assets"`
	Repository  *repoRef        `json:"repository"`
}

func (i *issue) isPR() bool { return i.PullRequest != nil }

func (i *issue) author() string {
	if i.User == nil {
		return ""
	}
	return i.User.Login
}

// comment is the projection of a Comment.
type comment struct {
	ID        int64   `json:"id"`
	User      *login  `json:"user"`
	Body      string  `json:"body"`
	CreatedAt string  `json:"created_at"`
	UpdatedAt string  `json:"updated_at"`
	IssueURL  string  `json:"issue_url"`
	Assets    []asset `json:"assets"`
}

func (c *comment) author() string {
	if c.User == nil {
		return ""
	}
	return c.User.Login
}

// timelineEvent is the projection of a TimelineComment (null items decode to
// the zero value and are dropped by the caller via the pointer slice).
type timelineEvent struct {
	ID              int64            `json:"id"`
	Type            string           `json:"type"`
	User            *login           `json:"user"`
	CreatedAt       string           `json:"created_at"`
	Body            string           `json:"body"`
	Label           *forgejo.Label   `json:"label"`
	OldMilestone    *issueMilestone  `json:"old_milestone"`
	Milestone       *issueMilestone  `json:"milestone"`
	Assignee        *login           `json:"assignee"`
	RemovedAssignee bool             `json:"removed_assignee"`
	OldTitle        string           `json:"old_title"`
	NewTitle        string           `json:"new_title"`
	OldRef          string           `json:"old_ref"`
	NewRef          string           `json:"new_ref"`
	RefIssue        *timelineRef     `json:"ref_issue"`
	RefCommitSHA    string           `json:"ref_commit_sha"`
	ReviewID        int64            `json:"review_id"`
	DependentIssue  *timelineRef     `json:"dependent_issue"`
	TrackedTime     *timelineTracked `json:"tracked_time"`
}

type timelineRef struct {
	Number     int64    `json:"number"`
	Repository *repoRef `json:"repository"`
}

type timelineTracked struct {
	Time int64 `json:"time"`
}

// issueTemplate is the projection of an issue template.
type issueTemplate struct {
	Name   string   `json:"name"`
	Title  string   `json:"title"`
	Labels []string `json:"labels"`
	Body   []struct {
		ID          string `json:"id"`
		Type        string `json:"type"`
		Validations struct {
			Required bool `json:"required"`
		} `json:"validations"`
	} `json:"body"`
}
