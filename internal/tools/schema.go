package tools

import (
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
)

// paramsType returns the struct type behind Spec.Params (value or pointer).
func paramsType(s Spec) (reflect.Type, error) {
	if s.Params == nil {
		return nil, fmt.Errorf("tool %s: Params is nil", s.Name)
	}
	t := reflect.TypeOf(s.Params)
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil, fmt.Errorf("tool %s: Params must be a struct, got %s", s.Name, t)
	}
	return t, nil
}

// BuildInputSchema builds the tool input schema (section 2): infer with
// jsonschema.For from Spec.Params, replace properties.action with
// {type:string, enum:[allowed + "help"]}, require only `action`, set
// additionalProperties:false and prefix every property description with the
// `[actions]` that read it (`*` marks required).
//
// Single tools have no `action` property and no `help`; their required list
// is the one of their only action and descriptions carry no prefix.
func BuildInputSchema(s Spec, allowed []Action) (*jsonschema.Schema, error) {
	t, err := paramsType(s)
	if err != nil {
		return nil, err
	}
	sc, err := jsonschema.ForType(t, nil)
	if err != nil {
		return nil, err
	}
	if sc.Properties == nil {
		sc.Properties = map[string]*jsonschema.Schema{}
	}
	for _, p := range sc.Properties {
		normalizeNullable(p)
	}
	sc.AdditionalProperties = &jsonschema.Schema{Not: &jsonschema.Schema{}}
	sc.Required = nil

	if s.Single {
		delete(sc.Properties, "action")
		if len(allowed) == 1 {
			for _, r := range allowed[0].Required {
				if _, ok := sc.Properties[r]; ok && r != "repo" {
					sc.Required = append(sc.Required, r)
				}
			}
		}
		sc.PropertyOrder = nil
		return sc, nil
	}

	names := make([]any, 0, len(allowed)+1)
	for _, a := range allowed {
		names = append(names, a.Name)
	}
	names = append(names, "help")
	sc.Properties["action"] = &jsonschema.Schema{Type: "string", Enum: names}
	sc.Properties["help"] = &jsonschema.Schema{Type: "string", Description: "action name to describe (same as action=help)"}
	sc.Required = []string{"action"}

	for name, p := range sc.Properties {
		if name == "action" || name == "repo" || name == "help" {
			continue
		}
		p.Description = prefixFor(name, allowed) + p.Description
	}
	sc.PropertyOrder = nil
	return sc, nil
}

// prefixFor renders the `[a*,b] ` prefix of property name: the actions that
// read it, `*` after those that require it. `[all] ` when every action reads
// it (and there is more than one).
func prefixFor(name string, allowed []Action) string {
	var parts []string
	reqAll := true
	for _, a := range allowed {
		req := slices.Contains(a.Required, name)
		if req || slices.Contains(a.Params, name) {
			if req {
				parts = append(parts, a.Name+"*")
			} else {
				parts = append(parts, a.Name)
				reqAll = false
			}
		}
	}
	switch {
	case len(parts) == 0:
		return "[-] "
	case len(parts) == len(allowed) && len(allowed) > 1:
		if reqAll {
			return "[all*] "
		}
		return "[all] "
	}
	return "[" + strings.Join(parts, ",") + "] "
}

// normalizeNullable turns {"type":["null","x"]} into {"type":"x"}: pointer
// and slice fields are inferred as nullable, which only costs tokens.
func normalizeNullable(s *jsonschema.Schema) {
	if s == nil {
		return
	}
	if len(s.Types) > 0 {
		var rest []string
		for _, t := range s.Types {
			if t != "null" {
				rest = append(rest, t)
			}
		}
		switch len(rest) {
		case 1:
			s.Type, s.Types = rest[0], nil
		default:
			s.Types = rest
		}
	}
	normalizeNullable(s.Items)
	for _, p := range s.Properties {
		normalizeNullable(p)
	}
}
