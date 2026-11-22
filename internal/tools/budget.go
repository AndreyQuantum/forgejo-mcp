package tools

// Schema-size budget (TestSchemaBudget). Frozen by WP4 after measuring the
// real tools/list JSON (name + description + inputSchema + annotations) of
// stub param structs with about 45 properties and action-prefixed descriptions
// (see TestMeasureStubSchemas):
//
//	pr    (11 actions) 4.1-4.4 KB    issue (18 actions) 4.5-5.0 KB
//	repo  (20 actions) 4.9-5.5 KB    run   (12 actions) 4.3-4.6 KB
//
// (lower figure: 8 params per action, upper: 12). The widest tool therefore
// needs about 5.5 KB, so 6000 bytes per tool holds with roughly 8% headroom.
// A typical tool costs about 1.5 KB plus 130 bytes per action; 29 non-admin
// tools estimate to 65-75 KB in total, hence 75000. Other work packages cannot
// change these numbers: rarely used params belong to forgejo_api instead.
const (
	// BudgetTotalBytes caps the tools/list JSON of all non-admin toolsets.
	BudgetTotalBytes = 75000
	// BudgetToolBytes caps the schema JSON of one tool.
	BudgetToolBytes = 6000
	// BudgetDescriptionChars caps Spec.Description.
	BudgetDescriptionChars = 160
)
