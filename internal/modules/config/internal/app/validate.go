package app

import (
	"encoding/json"
	"fmt"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"

	"github.com/antonyrejith/portun/internal/modules/config/internal/domain"
)

// maxExpressionLength bounds the size of one CEL expression.
const maxExpressionLength = 4096

// checkSpec validates a spec against its kind: the JSON Schema first, then
// every CEL expression (compiled and type-checked against the declared
// variables and result type). It writes nothing. Pointers are relative to
// the spec.
func (k *kindDef) checkSpec(spec json.RawMessage) []Issue {
	issues := validateAgainst(k.schema, spec)
	_, celIssues := k.compileExpressions(spec)
	return append(issues, celIssues...)
}

// compiledExpr is a type-checked expression at one concrete pointer.
type compiledExpr struct {
	field *celField
	ast   *cel.Ast
}

// compileExpressions finds the CEL expressions of a spec and type-checks
// them. A pointer pattern that selects nothing (an optional field) is fine.
func (k *kindDef) compileExpressions(spec json.RawMessage) (map[string]compiledExpr, []Issue) {
	if len(k.cel) == 0 {
		return nil, nil
	}
	doc, err := domain.Decode(spec)
	if err != nil {
		return nil, []Issue{{Code: "invalid_json", Message: "the spec is not valid JSON"}}
	}
	var issues []Issue
	out := map[string]compiledExpr{}
	for _, f := range k.cel {
		found, err := domain.Expand(doc, f.Pointer)
		if err != nil {
			issues = append(issues, Issue{Pointer: f.Pointer, Code: "cel", Message: err.Error()})
			continue
		}
		for _, pv := range found {
			expr, ok := pv.Value.(string)
			if !ok {
				issues = append(issues, Issue{Pointer: pv.Pointer, Code: "cel", Message: "a CEL expression must be a string"})
				continue
			}
			ast, issue := f.compile(expr)
			if issue != "" {
				issues = append(issues, Issue{Pointer: pv.Pointer, Code: "cel", Message: issue})
				continue
			}
			out[pv.Pointer] = compiledExpr{field: f, ast: ast}
		}
	}
	return out, issues
}

// compile parses and type-checks one expression; the second result is a
// problem description, empty when the expression is valid.
func (f *celField) compile(expr string) (*cel.Ast, string) {
	if len(expr) > maxExpressionLength {
		return nil, fmt.Sprintf("the expression is longer than %d characters", maxExpressionLength)
	}
	ast, iss := f.env.Compile(expr)
	if iss != nil && iss.Err() != nil {
		return nil, "invalid CEL expression: " + iss.Err().Error()
	}
	out := ast.OutputType()
	if out.Kind() != types.DynKind && out.Kind() != resultKind(f.Type) {
		return nil, fmt.Sprintf("the expression returns %s, want %s", out, f.Type)
	}
	return ast, ""
}
