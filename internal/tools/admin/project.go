package admin

// Projection structs of section 6.9: JSON decoding keeps only the paths we print.

// user projects a User object of the admin API.
type user struct {
	ID            int64  `json:"id"`
	Login         string `json:"login"`
	LoginName     string `json:"login_name"`
	SourceID      int64  `json:"source_id"`
	FullName      string `json:"full_name"`
	Email         string `json:"email"`
	IsAdmin       bool   `json:"is_admin"`
	Active        bool   `json:"active"`
	Restricted    bool   `json:"restricted"`
	ProhibitLogin bool   `json:"prohibit_login"`
	Created       string `json:"created"`
}

// org projects an Organization object.
type org struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	FullName    string `json:"full_name"`
	Visibility  string `json:"visibility"`
	Description string `json:"description"`
}

// email projects an Email object.
type email struct {
	Email    string `json:"email"`
	Verified bool   `json:"verified"`
	Primary  bool   `json:"primary"`
	UserName string `json:"username"`
}

// cron projects a Cron object.
type cron struct {
	Name      string `json:"name"`
	Schedule  string `json:"schedule"`
	Next      string `json:"next"`
	ExecTimes int64  `json:"exec_times"`
}

// quotaRule projects a QuotaRuleInfo.
type quotaRule struct {
	Name     string   `json:"name"`
	Limit    int64    `json:"limit"`
	Subjects []string `json:"subjects"`
}

// quotaGroup projects a QuotaGroup.
type quotaGroup struct {
	Name  string      `json:"name"`
	Rules []quotaRule `json:"rules"`
}

// quotaInfo projects a QuotaInfo.
type quotaInfo struct {
	Used struct {
		Size struct {
			Repos struct {
				Public  int64 `json:"public"`
				Private int64 `json:"private"`
			} `json:"repos"`
			Git struct {
				LFS int64 `json:"LFS"`
			} `json:"git"`
			Assets struct {
				Attachments struct {
					Issues   int64 `json:"issues"`
					Releases int64 `json:"releases"`
				} `json:"attachments"`
				Artifacts int64 `json:"artifacts"`
				Packages  struct {
					All int64 `json:"all"`
				} `json:"packages"`
			} `json:"assets"`
		} `json:"size"`
	} `json:"used"`
	Groups []quotaGroup `json:"groups"`
}
