package tools

// Schema-size budget (TestSchemaBudget). These are INITIAL values: WP4
// measures the real schemas of stub param structs for pr, issue, repo and run,
// then freezes the final numbers here. Other work packages cannot change them.
const (
	// BudgetTotalBytes caps the tools/list JSON of all non-admin toolsets.
	BudgetTotalBytes = 60000
	// BudgetToolBytes caps the schema JSON of one tool.
	BudgetToolBytes = 6000
	// BudgetDescriptionChars caps Spec.Description.
	BudgetDescriptionChars = 160
)
