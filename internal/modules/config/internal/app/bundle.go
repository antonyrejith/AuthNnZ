package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types/ref"
	"github.com/gowebpki/jcs"

	"github.com/antonyrejith/portun/internal/modules/config/internal/domain"
	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/ids"
)

const (
	// maxCost is the CEL cost limit of one evaluation: the number and
	// expense of the operations, like the cel-go defaults for untrusted
	// expressions.
	maxCost = 100000
	// interruptFrequency is how many comprehension iterations pass between
	// checks of the evaluation context.
	interruptFrequency = 100
	// EvalTimeout bounds one evaluation.
	EvalTimeout = 250 * time.Millisecond
)

// Bundle is the immutable runtime form of one tenant environment's
// configuration at one version (ADR-0003): the resolved settings, the
// published specs of its objects and their compiled CEL programs. Requests
// read it; nothing in it changes after it is built.
type Bundle struct {
	TenantID      ids.TenantID
	EnvironmentID ids.EnvironmentID
	// Version is the bundle's version, 0 for the in-memory bundle of an
	// environment that has never published.
	Version  int64
	Checksum string
	// Settings maps each registered setting key to its effective value.
	Settings map[string]json.RawMessage
	// Objects maps kind → name → the object's published spec.
	Objects map[string]map[string]json.RawMessage

	programs map[programKey]*program
}

type programKey struct{ kind, name, pointer string }

type program struct {
	prg cel.Program
	typ domain.CELType
}

// payload is the stored form of a bundle; its JCS form is checksummed.
type payload struct {
	TenantID      string                                `json:"tenant_id"`
	EnvironmentID string                                `json:"environment_id"`
	Version       int64                                 `json:"version"`
	Settings      map[string]json.RawMessage            `json:"settings"`
	Objects       map[string]map[string]json.RawMessage `json:"objects"`
}

// Checksum returns "sha256:" and the hex SHA-256 of the RFC 8785 (JCS) form
// of a JSON document.
func Checksum(doc []byte) (string, error) {
	canon, err := jcs.Transform(doc)
	if err != nil {
		return "", errs.Wrap(errs.CodeInternal, err, "config: canonicalize for checksum")
	}
	sum := sha256.Sum256(canon)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// encodePayload builds the stored payload of a bundle and its checksum.
func encodePayload(tenant ids.TenantID, env ids.EnvironmentID, version int64,
	settings map[string]json.RawMessage, objects map[string]map[string]json.RawMessage,
) (json.RawMessage, string, error) {
	p := payload{
		TenantID: tenant.String(), EnvironmentID: env.String(), Version: version,
		Settings: settings, Objects: objects,
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return nil, "", errs.Wrap(errs.CodeInternal, err, "config: encode bundle")
	}
	sum, err := Checksum(raw)
	if err != nil {
		return nil, "", err
	}
	return raw, sum, nil
}

// buildBundle decodes a stored payload, verifies its checksum and compiles
// the CEL programs of its objects.
func (r *Registry) buildBundle(raw json.RawMessage, checksum string) (*Bundle, error) {
	got, err := Checksum(raw)
	if err != nil {
		return nil, err
	}
	if got != checksum {
		return nil, errs.New(errs.CodeInternal, "config: bundle checksum mismatch: the stored bundle was altered")
	}
	var p payload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, errs.Wrap(errs.CodeInternal, err, "config: decode bundle")
	}
	tenant, err := ids.Parse[ids.TenantKind](p.TenantID)
	if err != nil {
		return nil, errs.Wrap(errs.CodeInternal, err, "config: bundle tenant")
	}
	env, err := ids.Parse[ids.EnvironmentKind](p.EnvironmentID)
	if err != nil {
		return nil, errs.Wrap(errs.CodeInternal, err, "config: bundle environment")
	}
	b := &Bundle{
		TenantID: tenant, EnvironmentID: env, Version: p.Version, Checksum: checksum,
		Settings: nonNilSettings(p.Settings), Objects: nonNilObjects(p.Objects),
		programs: map[programKey]*program{},
	}
	if err := r.compilePrograms(b); err != nil {
		return nil, err
	}
	return b, nil
}

// compilePrograms compiles the CEL expressions of the bundle's objects.
func (r *Registry) compilePrograms(b *Bundle) error {
	for kindName, objs := range b.Objects {
		k, ok := r.kind(kindName)
		if !ok || len(k.cel) == 0 {
			continue
		}
		for name, spec := range objs {
			exprs, issues := k.compileExpressions(spec)
			if len(issues) > 0 {
				return errs.New(errs.CodeInternal, fmt.Sprintf("config: bundle object %s/%s no longer compiles: %s", kindName, name, issues[0].Message))
			}
			for ptr, e := range exprs {
				prg, err := e.field.env.Program(e.ast, cel.CostLimit(maxCost), cel.InterruptCheckFrequency(interruptFrequency))
				if err != nil {
					return errs.Wrap(errs.CodeInternal, err, "config: build CEL program for "+kindName+"/"+name+ptr)
				}
				b.programs[programKey{kindName, name, ptr}] = &program{prg: prg, typ: e.field.Type}
			}
		}
	}
	return nil
}

func nonNilSettings(m map[string]json.RawMessage) map[string]json.RawMessage {
	if m == nil {
		return map[string]json.RawMessage{}
	}
	return m
}

func nonNilObjects(m map[string]map[string]json.RawMessage) map[string]map[string]json.RawMessage {
	if m == nil {
		return map[string]map[string]json.RawMessage{}
	}
	return m
}

// Eval evaluates the CEL expression at pointer (a concrete JSON Pointer such
// as "/rules/0/condition") of the object kind/name with vars, within the
// cost limit and EvalTimeout. The result has the Go type of the field's
// declared CEL type: bool, string, int64, float64, []any or map[string]any.
func (b *Bundle) Eval(kind, name, pointer string, vars map[string]any) (any, error) {
	return b.EvalContext(context.Background(), kind, name, pointer, vars)
}

// EvalContext is Eval with a caller's context, which can cancel it earlier.
func (b *Bundle) EvalContext(ctx context.Context, kind, name, pointer string, vars map[string]any) (any, error) {
	p, ok := b.programs[programKey{kind, name, pointer}]
	if !ok {
		return nil, errs.New(errs.CodeNotFound, "config: no CEL expression at "+kind+"/"+name+pointer)
	}
	ctx, cancel := context.WithTimeout(ctx, EvalTimeout)
	defer cancel()
	val, _, err := p.prg.ContextEval(ctx, vars)
	if err != nil {
		code := errs.CodeInvalidRequest
		if ctx.Err() != nil {
			code = errs.CodeDeadlineExceeded
		}
		return nil, errs.Wrap(code, err, "config: evaluate "+kind+"/"+name+pointer)
	}
	return nativeValue(val, p.typ)
}

// nativeValue converts a CEL result to plain Go values.
func nativeValue(val ref.Val, t domain.CELType) (any, error) {
	var target reflect.Type
	switch t {
	case domain.CELList:
		target = reflect.TypeFor[[]any]()
	case domain.CELMap:
		target = reflect.TypeFor[map[string]any]()
	case domain.CELBool, domain.CELString, domain.CELInt, domain.CELDouble:
		return val.Value(), nil
	}
	out, err := val.ConvertToNative(target)
	if err != nil {
		return nil, errs.Wrap(errs.CodeInternal, err, "config: convert the CEL result")
	}
	return out, nil
}
