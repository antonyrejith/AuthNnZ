package audit

import (
	"regexp"
	"sort"
	"sync"

	"github.com/antonyrejith/portun/internal/modules/audit/internal/classify"
	"github.com/antonyrejith/portun/internal/platform/errs"
)

// FieldClass classifies a field of an audited object or of event details.
type FieldClass = classify.Class

// Field classes.
const (
	// FieldPlain values are recorded as they are.
	FieldPlain = classify.Plain
	// FieldPersonal values are encrypted under the data subject's key
	// ("pii:v1:<key ID>:<ciphertext>") and become unreadable when the
	// subject's key is shredded.
	FieldPersonal = classify.Personal
	// FieldSecret values are replaced by {"redacted":"[REDACTED]",
	// "fingerprint":"sha256:<16 hex>"}.
	FieldSecret = classify.Secret
)

// EventType is a registered event type.
type EventType struct {
	// Name is "<object>.<verb>", lowercase, for example "tenant.created".
	Name string
	// Category maps the type to an OCSF class family.
	Category Category
	// OCSFClassUID is the OCSF class (class_uid) for SIEM export.
	OCSFClassUID int
	// Fields classifies the fields of the before and after values by JSON
	// Pointer pattern ("/email", "/secrets/*"); unlisted fields are plain.
	Fields map[string]FieldClass
	// Details lists the keys Record.Details may carry and their classes.
	// A record with an undeclared detail key is rejected.
	Details map[string]FieldClass
}

// Built-in event types, registered by NewRegistry.
const (
	// TypeTransactionFailed records a Change or PlatformChange that failed
	// before any record was made (the failing operation is in
	// request.operation and the error code in error_code).
	TypeTransactionFailed = "transaction.failed"
	// TypeDataKeyCreated records the creation of a data subject's key
	// outside another change (for events written through the spool).
	TypeDataKeyCreated = "data_key.created"
)

// OCSF classes used by the built-in types.
const (
	ocsfEntityManagement = 3004
	ocsfAPIActivity      = 6003
)

var typeNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*\.[a-z][a-z0-9_]*$`)

type registered struct {
	EventType
	fields  classify.Rules
	details map[string]FieldClass
}

// Registry holds the registered event types. It is safe for concurrent use;
// types are normally registered at startup.
type Registry struct {
	mu    sync.RWMutex
	types map[string]*registered
}

// NewRegistry returns a registry holding the built-in types.
func NewRegistry() *Registry {
	r := &Registry{types: map[string]*registered{}}
	for _, t := range []EventType{
		{Name: TypeTransactionFailed, Category: CategoryData, OCSFClassUID: ocsfAPIActivity},
		{Name: TypeDataKeyCreated, Category: CategoryKeyManagement, OCSFClassUID: ocsfEntityManagement},
	} {
		if err := r.Register(t); err != nil {
			panic("audit: built-in event type: " + err.Error())
		}
	}
	return r
}

// Register adds an event type. It fails with errs.CodeInternal for an
// invalid definition or a name that is already registered.
func (r *Registry) Register(t EventType) error {
	if !typeNamePattern.MatchString(t.Name) {
		return errs.New(errs.CodeInternal, "audit: event type name "+quote(t.Name)+" is not <object>.<verb> in lowercase")
	}
	if !validCategory(t.Category) {
		return errs.New(errs.CodeInternal, "audit: event type "+quote(t.Name)+" has an unknown category")
	}
	if t.OCSFClassUID <= 0 {
		return errs.New(errs.CodeInternal, "audit: event type "+quote(t.Name)+" needs an OCSF class_uid")
	}
	rules, err := classify.Compile(t.Fields)
	if err != nil {
		return errs.Wrap(errs.CodeInternal, err, "audit: event type "+quote(t.Name)+": invalid field classification")
	}
	details := make(map[string]FieldClass, len(t.Details))
	for k, c := range t.Details {
		if k == "" || !c.Valid() {
			return errs.New(errs.CodeInternal, "audit: event type "+quote(t.Name)+": invalid detail declaration")
		}
		details[k] = c
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.types[t.Name]; ok {
		return errs.New(errs.CodeInternal, "audit: event type "+quote(t.Name)+" is already registered")
	}
	r.types[t.Name] = &registered{EventType: t, fields: rules, details: details}
	return nil
}

// Lookup returns a registered type.
func (r *Registry) Lookup(name string) (EventType, bool) {
	t, ok := r.lookup(name)
	if !ok {
		return EventType{}, false
	}
	return t.EventType, true
}

func (r *Registry) lookup(name string) (*registered, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.types[name]
	return t, ok
}

func (r *Registry) mustLookup(name string) (*registered, error) {
	t, ok := r.lookup(name)
	if !ok {
		return nil, errs.New(errs.CodeInternal, "audit: event type "+quote(name)+" is not registered")
	}
	return t, nil
}

// Names returns the registered type names, sorted.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.types))
	for n := range r.types {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func validCategory(c Category) bool {
	switch c {
	case CategoryConfiguration, CategoryData, CategoryAuthentication, CategoryAuthorization,
		CategorySensitiveRead, CategoryAuditAccess, CategoryKeyManagement, CategoryPlatform:
		return true
	default:
		return false
	}
}

func quote(s string) string { return `"` + s + `"` }
