// Package app holds the use cases of the config module: validation of specs
// and CEL expressions, the registries of kinds and settings, publishing and
// rollback, settings resolution and the runtime bundle cache.
package app

import (
	"bytes"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/antonyrejith/portun/internal/modules/config/internal/domain"
	"github.com/antonyrejith/portun/internal/platform/errs"
)

// Registry holds the registered kinds and settings. It is a value passed to
// the module, not a global; it is safe for concurrent use and is normally
// filled at startup.
type Registry struct {
	mu       sync.RWMutex
	kinds    map[string]*kindDef
	settings map[string]*settingDef
}

type kindDef struct {
	domain.Kind
	schema *jsonschema.Schema
	cel    []*celField
}

type celField struct {
	domain.CELField
	env *cel.Env
}

type settingDef struct {
	domain.Setting
	schema *jsonschema.Schema
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{kinds: map[string]*kindDef{}, settings: map[string]*settingDef{}}
}

// RegisterKind adds a kind of configuration object. The schema must compile
// as JSON Schema 2020-12 and the CEL field declarations must be well formed.
// It fails with errs.CodeInternal, since a kind comes from code.
func (r *Registry) RegisterKind(k domain.Kind) error {
	if err := k.Check(); err != nil {
		return err //nolint:wrapcheck // already an *errs.Error
	}
	sch, err := compileSchema("kind/"+k.Name, k.Schema)
	if err != nil {
		return errs.Wrap(errs.CodeInternal, err, "config: kind "+k.Name+": invalid schema")
	}
	d := &kindDef{Kind: k, schema: sch}
	for _, f := range k.CELFields {
		env, err := newCELEnv(f.Vars)
		if err != nil {
			return errs.Wrap(errs.CodeInternal, err, "config: kind "+k.Name+": CEL environment for "+f.Pointer)
		}
		d.cel = append(d.cel, &celField{CELField: f, env: env})
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.kinds[k.Name]; ok {
		return errs.New(errs.CodeInternal, "config: kind "+k.Name+" is already registered")
	}
	r.kinds[k.Name] = d
	return nil
}

// RegisterSetting adds a setting. The definition must be consistent: the
// merge rule fits the default, the default satisfies the schema, and the
// policy fits the merge rule.
func (r *Registry) RegisterSetting(s domain.Setting) error {
	if err := s.Check(); err != nil {
		return err //nolint:wrapcheck // already an *errs.Error
	}
	sch, err := compileSchema("setting/"+s.Key, s.Schema)
	if err != nil {
		return errs.Wrap(errs.CodeInternal, err, "config: setting "+s.Key+": invalid schema")
	}
	if issues := validateAgainst(sch, s.Default); len(issues) > 0 {
		return errs.New(errs.CodeInternal, "config: setting "+s.Key+": the default does not satisfy the schema: "+issues[0].Message)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.settings[s.Key]; ok {
		return errs.New(errs.CodeInternal, "config: setting "+s.Key+" is already registered")
	}
	r.settings[s.Key] = &settingDef{Setting: s, schema: sch}
	return nil
}

func (r *Registry) kind(name string) (*kindDef, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	k, ok := r.kinds[name]
	return k, ok
}

func (r *Registry) setting(key string) (*settingDef, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.settings[key]
	return s, ok
}

// SettingKeys returns the registered setting keys, sorted.
func (r *Registry) SettingKeys() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	keys := make([]string, 0, len(r.settings))
	for k := range r.settings {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// KindNames returns the registered kind names, sorted.
func (r *Registry) KindNames() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.kinds))
	for k := range r.kinds {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// Issue is one problem found in a spec or value: a JSON Pointer (relative to
// the document checked) and what is wrong.
type Issue struct {
	Pointer string
	Code    string
	Message string
}

// compileSchema compiles a JSON Schema 2020-12 document.
func compileSchema(name string, raw []byte) (*jsonschema.Schema, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("parse schema: %w", err)
	}
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	url := "mem://portun.invalid/config/" + name + ".json"
	if err := c.AddResource(url, doc); err != nil {
		return nil, fmt.Errorf("add schema: %w", err)
	}
	sch, err := c.Compile(url)
	if err != nil {
		return nil, fmt.Errorf("compile schema: %w", err)
	}
	return sch, nil
}

// validateAgainst checks a JSON document against a schema and returns one
// issue per violation, with the JSON Pointer of the offending value.
func validateAgainst(sch *jsonschema.Schema, raw []byte) []Issue {
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return []Issue{{Pointer: "", Code: "invalid_json", Message: "the document is not valid JSON"}}
	}
	err = sch.Validate(inst)
	if err == nil {
		return nil
	}
	var ve *jsonschema.ValidationError
	if !errors.As(err, &ve) {
		return []Issue{{Pointer: "", Code: "invalid", Message: err.Error()}}
	}
	var out []Issue
	seen := map[string]bool{}
	for _, u := range ve.BasicOutput().Errors {
		if u.Error == nil {
			continue
		}
		msg := u.Error.String()
		if k := u.InstanceLocation + "\x00" + msg; !seen[k] {
			seen[k] = true
			out = append(out, Issue{Pointer: u.InstanceLocation, Code: "schema", Message: msg})
		}
	}
	if len(out) == 0 {
		out = append(out, Issue{Pointer: "", Code: "schema", Message: ve.Error()})
	}
	return out
}

// celTypeOf maps a declared type to the CEL type used for variables.
func celTypeOf(t domain.CELType) *cel.Type {
	switch t {
	case domain.CELBool:
		return cel.BoolType
	case domain.CELString:
		return cel.StringType
	case domain.CELInt:
		return cel.IntType
	case domain.CELDouble:
		return cel.DoubleType
	case domain.CELList:
		return cel.ListType(cel.DynType)
	case domain.CELMap:
		return cel.MapType(cel.StringType, cel.DynType)
	default:
		return cel.DynType
	}
}

func newCELEnv(vars []domain.CELVar) (*cel.Env, error) {
	opts := make([]cel.EnvOption, 0, len(vars))
	for _, v := range vars {
		opts = append(opts, cel.Variable(v.Name, celTypeOf(v.Type)))
	}
	env, err := cel.NewEnv(opts...)
	if err != nil {
		return nil, fmt.Errorf("new CEL environment: %w", err)
	}
	return env, nil
}

// resultKind is the CEL kind an expression of the declared type returns.
func resultKind(t domain.CELType) types.Kind {
	switch t {
	case domain.CELBool:
		return types.BoolKind
	case domain.CELString:
		return types.StringKind
	case domain.CELInt:
		return types.IntKind
	case domain.CELDouble:
		return types.DoubleKind
	case domain.CELList:
		return types.ListKind
	case domain.CELMap:
		return types.MapKind
	default:
		return types.DynKind
	}
}
