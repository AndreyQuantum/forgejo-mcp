package tools

import "github.com/google/jsonschema-go/jsonschema"

// BuildInputSchema builds the tool input schema (section 2): infer with
// jsonschema.For from Spec.Params, replace properties.action with
// {type:string, enum:[allowed + "help"]}, require only `action`, set
// additionalProperties:false and prefix every property description with the
// `[actions]` that read it (`*` marks required).
func BuildInputSchema(s Spec, allowed []Action) (*jsonschema.Schema, error) { panic("WP4") }
