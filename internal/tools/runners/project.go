package runners

// apiRunner is the projection of ActionRunner (only the fields we print).
type apiRunner struct {
	ID          int64    `json:"id"`
	UUID        string   `json:"uuid"`
	Name        string   `json:"name"`
	Status      string   `json:"status"`
	Version     string   `json:"version"`
	Description string   `json:"description"`
	Labels      []string `json:"labels"`
	Ephemeral   bool     `json:"ephemeral"`
	RepoID      int64    `json:"repo_id"`
	OwnerID     int64    `json:"owner_id"`
}

// apiJob is the projection of ActionRunJob used by `queue`.
type apiJob struct {
	ID     int64    `json:"id"`
	Name   string   `json:"name"`
	Status string   `json:"status"`
	RunsOn []string `json:"runs_on"`
	RepoID int64    `json:"repo_id"`
}

// registerResponse is RegisterRunnerResponse.
type registerResponse struct {
	ID    int64  `json:"id"`
	UUID  string `json:"uuid"`
	Token string `json:"token"`
}

// tokenResponse is the body of GET .../registration-token.
type tokenResponse struct {
	Token string `json:"token"`
}
