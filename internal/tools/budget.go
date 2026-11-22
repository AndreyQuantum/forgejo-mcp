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
// Measured on the finished server (docs/VERIFIED.md): the 29 default tools
// are 51,804 bytes of tools/list JSON (about 13k tokens), all 30 tools are
// 55,529 bytes, and the widest tool (repo) is 4,259 bytes. The limits below
// keep headroom above those figures; rarely used params belong to forgejo_api
// instead.
const (
	// BudgetTotalBytes caps the tools/list JSON of all non-admin toolsets.
	BudgetTotalBytes = 75000
	// BudgetToolBytes caps the schema JSON of one tool.
	BudgetToolBytes = 6000
	// BudgetDescriptionChars caps Spec.Description.
	BudgetDescriptionChars = 160
)
