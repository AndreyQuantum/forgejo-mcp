package forgejo

// APISettings mirrors GET /settings/api (cached for the process lifetime).
type APISettings struct {
	DefaultPagingNum int `json:"default_paging_num"`
	MaxResponseItems int `json:"max_response_items"`
}
