package app

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/antonyrejith/portun/internal/modules/audit"
	"github.com/antonyrejith/portun/internal/modules/config/internal/domain"
	"github.com/antonyrejith/portun/internal/platform/db"
	"github.com/antonyrejith/portun/internal/platform/db/page"
	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/ids"
	"github.com/antonyrejith/portun/internal/platform/scope"
)

// ValidationReport is the result of Validate: the problems found in one
// version of an object, with JSON Pointers relative to the spec.
type ValidationReport struct {
	ObjectID ids.ConfigObjectID
	Version  int
	Valid    bool
	Issues   []Issue
}

// CreateDraft saves a new draft of the object kind/name: a new object, or a
// new draft version of an existing one (at most one draft at a time). The
// spec is validated against the kind's JSON Schema; CEL expressions are
// type-checked at Validate and Publish.
func (s *Service) CreateDraft(ctx context.Context, kind, name string, spec json.RawMessage, note string) (domain.Object, domain.Version, error) {
	t, err := envScope(ctx)
	if err != nil {
		return domain.Object{}, domain.Version{}, err
	}
	checksum, err := s.checkNewDraft(kind, name, spec, note)
	if err != nil {
		return domain.Object{}, domain.Version{}, err
	}
	var obj domain.Object
	var ver domain.Version
	err = s.auditor.Change(ctx, func(ctx context.Context, tx *audit.Tx) error {
		var err error
		obj, ver, err = s.saveDraft(ctx, tx, t, kind, name, spec, checksum, note)
		return err
	})
	if err != nil {
		return domain.Object{}, domain.Version{}, err //nolint:wrapcheck // already an *errs.Error
	}
	return obj, ver, nil
}

// checkNewDraft validates the input of CreateDraft and returns the spec's checksum.
func (s *Service) checkNewDraft(kind, name string, spec json.RawMessage, note string) (string, error) {
	k, ok := s.reg.kind(kind)
	if !ok {
		return "", fieldError("/kind", "unknown", "the kind "+strconv.Quote(kind)+" is not registered")
	}
	if !domain.ValidObjectName(name) {
		return "", fieldError("/name", "invalid", "the name must be 1 to 128 characters of letters, digits, '_', '.', ':' and '-'")
	}
	if err := checkNote(note); err != nil {
		return "", err
	}
	if issues := validateAgainst(k.schema, spec); len(issues) > 0 {
		return "", validationFailed("the spec does not satisfy the schema of kind "+kind, issues, "/spec")
	}
	return Checksum(spec)
}

// draftObject returns the object a new draft belongs to: an existing one,
// locked and with its etag advanced, or a new one. It refuses an object that
// already has an open draft.
func (s *Service) draftObject(ctx context.Context, tx *audit.Tx, t scope.Tenant, kind, name string) (domain.Object, error) {
	now := s.clock()
	existing, found, err := s.repo.FindObject(ctx, tx, t.TenantID, t.EnvironmentID, kind, name)
	if err != nil {
		return domain.Object{}, err //nolint:wrapcheck // already an *errs.Error
	}
	if !found {
		id, err := ids.New[ids.ConfigObjectKind]()
		if err != nil {
			return domain.Object{}, errs.Wrap(errs.CodeInternal, err, "config: new object ID")
		}
		obj := domain.Object{
			ID: id, EnvironmentID: t.EnvironmentID, Kind: kind, Name: name, Status: domain.ObjectActive,
			Version: 1, CreatedAt: now, UpdatedAt: now,
		}
		return obj, s.repo.InsertObject(ctx, tx, t.TenantID, obj) //nolint:wrapcheck // already an *errs.Error
	}
	obj, err := s.repo.GetObject(ctx, tx, t.TenantID, t.EnvironmentID, existing.ID, true)
	if err != nil {
		return domain.Object{}, err //nolint:wrapcheck // already an *errs.Error
	}
	_, open, err := s.repo.OpenDraft(ctx, tx, t.TenantID, obj.ID)
	if err != nil {
		return domain.Object{}, err //nolint:wrapcheck // already an *errs.Error
	}
	if open {
		return domain.Object{}, errs.New(errs.CodeConflict, "the object "+kind+"/"+name+" already has an open draft; update it or publish it first")
	}
	obj.Version++
	obj.UpdatedAt = now
	return obj, s.repo.UpdateObject(ctx, tx, t.TenantID, obj) //nolint:wrapcheck // already an *errs.Error
}

// saveDraft writes a new draft version and records it.
func (s *Service) saveDraft(ctx context.Context, tx *audit.Tx, t scope.Tenant, kind, name string, spec json.RawMessage, checksum, note string) (domain.Object, domain.Version, error) {
	obj, err := s.draftObject(ctx, tx, t, kind, name)
	if err != nil {
		return domain.Object{}, domain.Version{}, err
	}
	next, err := s.repo.MaxVersion(ctx, tx, t.TenantID, obj.ID)
	if err != nil {
		return domain.Object{}, domain.Version{}, err //nolint:wrapcheck // already an *errs.Error
	}
	ver := domain.Version{
		ObjectID: obj.ID, Version: next + 1, Status: domain.VersionDraft, Spec: spec, Checksum: checksum,
		ChangeNote: note, CreatedBy: actorName(ctx), CreatedAt: s.clock(),
	}
	if err := s.repo.InsertVersion(ctx, tx, t.TenantID, ver); err != nil {
		return domain.Object{}, domain.Version{}, err //nolint:wrapcheck // already an *errs.Error
	}
	return obj, ver, tx.Record(audit.Record{ //nolint:wrapcheck // already an *errs.Error
		Type: EventDraftSaved, Target: objectTarget(obj), After: spec, AfterVersion: int64(ver.Version),
		Rows:    []audit.RowRef{objectRow(t.TenantID, obj.ID), versionRow(t.TenantID, obj.ID, ver.Version)},
		Details: map[string]any{"note": note},
	})
}

func objectTarget(o domain.Object) *audit.Target {
	return &audit.Target{Type: "config_object", ID: o.ID.String(), Name: o.Kind + "/" + o.Name}
}

// UpdateDraft replaces the open draft's spec. expectedVersion must equal the
// object's current etag (Object.Version), else errs.CodePreconditionFailed.
func (s *Service) UpdateDraft(ctx context.Context, id ids.ConfigObjectID, expectedVersion int64, spec json.RawMessage, note string) (domain.Version, error) {
	t, err := envScope(ctx)
	if err != nil {
		return domain.Version{}, err
	}
	if err := checkNote(note); err != nil {
		return domain.Version{}, err
	}
	pre, err := s.GetObject(ctx, id)
	if err != nil {
		return domain.Version{}, err
	}
	k, ok := s.reg.kind(pre.Kind)
	if !ok {
		return domain.Version{}, errs.New(errs.CodeConflict, "the kind "+pre.Kind+" of this object is no longer registered")
	}
	if issues := validateAgainst(k.schema, spec); len(issues) > 0 {
		return domain.Version{}, validationFailed("the spec does not satisfy the schema of kind "+pre.Kind, issues, "/spec")
	}
	checksum, err := Checksum(spec)
	if err != nil {
		return domain.Version{}, err
	}
	var out domain.Version
	err = s.auditor.Change(ctx, func(ctx context.Context, tx *audit.Tx) error {
		var err error
		out, err = s.replaceDraft(ctx, tx, t, id, expectedVersion, spec, checksum, note)
		return err
	})
	if err != nil {
		return domain.Version{}, err //nolint:wrapcheck // already an *errs.Error
	}
	return out, nil
}

// replaceDraft writes the new spec of the open draft and records it.
func (s *Service) replaceDraft(ctx context.Context, tx *audit.Tx, t scope.Tenant, id ids.ConfigObjectID, expectedVersion int64, spec json.RawMessage, checksum, note string) (domain.Version, error) {
	obj, err := s.repo.GetObject(ctx, tx, t.TenantID, t.EnvironmentID, id, true)
	if err != nil {
		return domain.Version{}, err //nolint:wrapcheck // already an *errs.Error
	}
	if obj.Version != expectedVersion {
		return domain.Version{}, errs.New(errs.CodePreconditionFailed, "the object changed since version "+strconv.FormatInt(expectedVersion, 10)+"; reload it")
	}
	draft, open, err := s.repo.OpenDraft(ctx, tx, t.TenantID, id)
	if err != nil {
		return domain.Version{}, err //nolint:wrapcheck // already an *errs.Error
	}
	if !open {
		return domain.Version{}, errs.New(errs.CodeConflict, "the object has no open draft; create one first")
	}
	before := draft.Spec
	draft.Spec, draft.Checksum, draft.ChangeNote = spec, checksum, note
	if err := s.repo.UpdateDraft(ctx, tx, t.TenantID, draft); err != nil {
		return domain.Version{}, err //nolint:wrapcheck // already an *errs.Error
	}
	obj.Version++
	obj.UpdatedAt = s.clock()
	if err := s.repo.UpdateObject(ctx, tx, t.TenantID, obj); err != nil {
		return domain.Version{}, err //nolint:wrapcheck // already an *errs.Error
	}
	return draft, tx.Record(audit.Record{ //nolint:wrapcheck // already an *errs.Error
		Type: EventDraftSaved, Target: objectTarget(obj), Before: before, After: spec,
		BeforeVersion: int64(draft.Version), AfterVersion: int64(draft.Version),
		Rows:    []audit.RowRef{objectRow(t.TenantID, id), versionRow(t.TenantID, id, draft.Version)},
		Details: map[string]any{"note": note},
	})
}

// GetObject returns an object of the scope's environment.
func (s *Service) GetObject(ctx context.Context, id ids.ConfigObjectID) (domain.Object, error) {
	t, err := envScope(ctx)
	if err != nil {
		return domain.Object{}, err
	}
	var o domain.Object
	err = s.read(ctx, func(ctx context.Context, q db.Querier) error {
		var err error
		o, err = s.repo.GetObject(ctx, q, t.TenantID, t.EnvironmentID, id, false)
		return err //nolint:wrapcheck // already an *errs.Error
	})
	return o, err
}

// ListVersions returns the versions of an object, newest first.
func (s *Service) ListVersions(ctx context.Context, id ids.ConfigObjectID, req page.Request) ([]domain.Version, string, error) {
	t, err := envScope(ctx)
	if err != nil {
		return nil, "", err
	}
	binding := "config.versions|" + id.String()
	p, err := page.Parse(req, binding, 1)
	if err != nil {
		return nil, "", err //nolint:wrapcheck // already an *errs.Error
	}
	after := 0
	if !p.First() {
		after, err = strconv.Atoi(p.After[0])
		if err != nil || after <= 0 {
			return nil, "", errs.New(errs.CodeInvalidRequest, "invalid page token")
		}
	}
	var rows []domain.Version
	err = s.read(ctx, func(ctx context.Context, q db.Querier) error {
		if _, err := s.repo.GetObject(ctx, q, t.TenantID, t.EnvironmentID, id, false); err != nil {
			return err //nolint:wrapcheck // already an *errs.Error
		}
		var err error
		rows, err = s.repo.ListVersions(ctx, q, t.TenantID, id, after, p.Fetch())
		return err //nolint:wrapcheck // already an *errs.Error
	})
	if err != nil {
		return nil, "", err
	}
	rows, next := page.Trim(p, rows, binding, func(v domain.Version) []string { return []string{strconv.Itoa(v.Version)} })
	return rows, next, nil
}

// Validate checks one version of an object against its kind: the JSON Schema
// and every CEL expression. It never writes.
func (s *Service) Validate(ctx context.Context, id ids.ConfigObjectID, version int) (ValidationReport, error) {
	t, err := envScope(ctx)
	if err != nil {
		return ValidationReport{}, err
	}
	var rep ValidationReport
	err = s.read(ctx, func(ctx context.Context, q db.Querier) error {
		obj, err := s.repo.GetObject(ctx, q, t.TenantID, t.EnvironmentID, id, false)
		if err != nil {
			return err //nolint:wrapcheck // already an *errs.Error
		}
		v, err := s.repo.GetVersion(ctx, q, t.TenantID, id, version)
		if err != nil {
			return err //nolint:wrapcheck // already an *errs.Error
		}
		k, ok := s.reg.kind(obj.Kind)
		if !ok {
			return errs.New(errs.CodeConflict, "the kind "+obj.Kind+" of this object is no longer registered")
		}
		issues := k.checkSpec(v.Spec)
		rep = ValidationReport{ObjectID: id, Version: version, Valid: len(issues) == 0, Issues: issues}
		return nil
	})
	return rep, err
}

// Diff returns the field-level difference between two versions of an object.
func (s *Service) Diff(ctx context.Context, id ids.ConfigObjectID, from, to int) ([]domain.DiffEntry, error) {
	t, err := envScope(ctx)
	if err != nil {
		return nil, err
	}
	var out []domain.DiffEntry
	err = s.read(ctx, func(ctx context.Context, q db.Querier) error {
		if _, err := s.repo.GetObject(ctx, q, t.TenantID, t.EnvironmentID, id, false); err != nil {
			return err //nolint:wrapcheck // already an *errs.Error
		}
		a, err := s.repo.GetVersion(ctx, q, t.TenantID, id, from)
		if err != nil {
			return err //nolint:wrapcheck // already an *errs.Error
		}
		b, err := s.repo.GetVersion(ctx, q, t.TenantID, id, to)
		if err != nil {
			return err //nolint:wrapcheck // already an *errs.Error
		}
		out, err = domain.Diff(a.Spec, b.Spec)
		return err //nolint:wrapcheck // already an *errs.Error
	})
	return out, err
}
