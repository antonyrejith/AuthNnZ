package app

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/antonyrejith/portun/internal/modules/audit"
	"github.com/antonyrejith/portun/internal/modules/config/internal/domain"
	"github.com/antonyrejith/portun/internal/platform/bus"
	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/ids"
	"github.com/antonyrejith/portun/internal/platform/outbox"
	"github.com/antonyrejith/portun/internal/platform/scope"
)

// publishItem is one object about to be published.
type publishItem struct {
	obj   domain.Object
	draft domain.Version
	prev  json.RawMessage // the spec in force before, nil if none
}

// Publish makes each object's open draft the published version in force and
// compiles the environment's bundle, all in one audited transaction. With no
// objects it compiles a bundle of the current settings and published specs
// (how setting changes take effect). Every spec is validated again, CEL
// included; any problem rejects the whole publish.
func (s *Service) Publish(ctx context.Context, objectIDs []ids.ConfigObjectID, note string) (domain.Publication, error) {
	t, err := envScope(ctx)
	if err != nil {
		return domain.Publication{}, err
	}
	if err := checkNote(note); err != nil {
		return domain.Publication{}, err
	}
	seen := map[ids.ConfigObjectID]bool{}
	var unique []ids.ConfigObjectID
	for _, id := range objectIDs {
		if !seen[id] {
			seen[id] = true
			unique = append(unique, id)
		}
	}
	var pub domain.Publication
	err = s.auditor.Change(ctx, func(ctx context.Context, tx *audit.Tx) error {
		return s.publishDrafts(ctx, tx, t, unique, note, &pub)
	})
	if err != nil {
		return domain.Publication{}, err //nolint:wrapcheck // already an *errs.Error
	}
	s.refreshPublished(ctx, t, pub.BundleVersion)
	return pub, nil
}

// refreshPublished updates this process's cache right after its own publish,
// so the publisher reads its writes without waiting for the notification. A
// failure only delays the update to the next poll.
func (s *Service) refreshPublished(ctx context.Context, t scope.Tenant, version int64) {
	if err := s.refresh(ctx, cacheKey{t.TenantID, t.EnvironmentID}, version); err != nil {
		s.logger.WarnContext(ctx, "config bundle reload after publish failed", slog.String("error", err.Error()))
	}
}

func (s *Service) publishDrafts(ctx context.Context, tx *audit.Tx, t scope.Tenant, objectIDs []ids.ConfigObjectID, note string, pub *domain.Publication) error {
	if err := s.repo.LockEnvironment(ctx, tx, t.TenantID, t.EnvironmentID); err != nil {
		return err //nolint:wrapcheck // already an *errs.Error
	}
	items, err := s.loadPublishItems(ctx, tx, t, objectIDs)
	if err != nil {
		return err
	}
	now := s.clock()
	for _, it := range items {
		if err := s.publishItem(ctx, tx, t, it, note, now); err != nil {
			return err
		}
		pub.Objects = append(pub.Objects, domain.PublishedObject{ObjectID: it.obj.ID, Version: it.draft.Version})
	}
	return s.compileAndStore(ctx, tx, t, note, now, pub)
}

// loadPublishItems locks the objects, finds their drafts and validates every
// spec; all problems are reported together as one validation failure.
func (s *Service) loadPublishItems(ctx context.Context, tx *audit.Tx, t scope.Tenant, objectIDs []ids.ConfigObjectID) ([]publishItem, error) {
	var items []publishItem
	var fields []errs.FieldError
	for i, id := range objectIDs {
		it, k, err := s.loadDraft(ctx, tx, t, id)
		if err != nil {
			return nil, err
		}
		prefix := "/object_ids/" + strconv.Itoa(i) + "/spec"
		for _, is := range k.checkSpec(it.draft.Spec) {
			fields = append(fields, errs.FieldError{Pointer: prefix + is.Pointer, Code: is.Code, Detail: it.obj.Kind + "/" + it.obj.Name + ": " + is.Message})
		}
		items = append(items, it)
	}
	if len(fields) > 0 {
		return nil, errs.New(errs.CodeValidationFailed, "the configuration does not validate; nothing was published").WithFields(fields...)
	}
	return items, nil
}

// loadDraft locks an active object and returns it with its open draft, the
// spec in force and the object's kind.
func (s *Service) loadDraft(ctx context.Context, tx *audit.Tx, t scope.Tenant, id ids.ConfigObjectID) (publishItem, *kindDef, error) {
	obj, err := s.activeObject(ctx, tx, t, id)
	if err != nil {
		return publishItem{}, nil, err
	}
	draft, open, err := s.repo.OpenDraft(ctx, tx, t.TenantID, id)
	if err != nil {
		return publishItem{}, nil, err //nolint:wrapcheck // already an *errs.Error
	}
	if !open {
		return publishItem{}, nil, errs.New(errs.CodeConflict, "the object "+obj.Kind+"/"+obj.Name+" has no draft to publish")
	}
	k, ok := s.reg.kind(obj.Kind)
	if !ok {
		return publishItem{}, nil, errs.New(errs.CodeConflict, "the kind "+obj.Kind+" is no longer registered")
	}
	prev, err := s.specInForce(ctx, tx, t, obj)
	if err != nil {
		return publishItem{}, nil, err
	}
	return publishItem{obj: obj, draft: draft, prev: prev}, k, nil
}

// activeObject locks an object of the environment and requires it active.
func (s *Service) activeObject(ctx context.Context, tx *audit.Tx, t scope.Tenant, id ids.ConfigObjectID) (domain.Object, error) {
	obj, err := s.repo.GetObject(ctx, tx, t.TenantID, t.EnvironmentID, id, true)
	if err != nil {
		return domain.Object{}, err //nolint:wrapcheck // already an *errs.Error
	}
	if obj.Status != domain.ObjectActive {
		return domain.Object{}, errs.New(errs.CodeConflict, "the object "+obj.Kind+"/"+obj.Name+" is archived")
	}
	return obj, nil
}

// specInForce returns the object's published spec in force, nil if none.
func (s *Service) specInForce(ctx context.Context, tx *audit.Tx, t scope.Tenant, obj domain.Object) (json.RawMessage, error) {
	if obj.CurrentVersion == 0 {
		return nil, nil
	}
	v, err := s.repo.GetVersion(ctx, tx, t.TenantID, obj.ID, obj.CurrentVersion)
	if err != nil {
		return nil, err //nolint:wrapcheck // already an *errs.Error
	}
	return v.Spec, nil
}

// publishItem marks the draft published, makes it the object's current
// version and records it.
func (s *Service) publishItem(ctx context.Context, tx *audit.Tx, t scope.Tenant, it publishItem, note string, now time.Time) error {
	if err := s.repo.MarkPublished(ctx, tx, t.TenantID, it.obj.ID, it.draft.Version, now); err != nil {
		return err //nolint:wrapcheck // already an *errs.Error
	}
	before := int64(it.obj.CurrentVersion)
	it.obj.CurrentVersion = it.draft.Version
	it.obj.Version++
	it.obj.UpdatedAt = now
	if err := s.repo.UpdateObject(ctx, tx, t.TenantID, it.obj); err != nil {
		return err //nolint:wrapcheck // already an *errs.Error
	}
	return tx.Record(audit.Record{ //nolint:wrapcheck // already an *errs.Error
		Type: EventVersionPublished, Target: objectTarget(it.obj), Before: jsonOrNil(it.prev), After: it.draft.Spec,
		BeforeVersion: before, AfterVersion: int64(it.draft.Version),
		Rows:    []audit.RowRef{objectRow(t.TenantID, it.obj.ID), versionRow(t.TenantID, it.obj.ID, it.draft.Version)},
		Details: map[string]any{"note": note},
	})
}

// Rollback creates a new version of the object that copies the spec of
// toVersion, publishes it and compiles the bundle. Nothing is rewritten:
// versions only ever grow. An open draft must be published first.
func (s *Service) Rollback(ctx context.Context, id ids.ConfigObjectID, toVersion int, note string) (domain.Publication, error) {
	t, err := envScope(ctx)
	if err != nil {
		return domain.Publication{}, err
	}
	if err := checkNote(note); err != nil {
		return domain.Publication{}, err
	}
	var pub domain.Publication
	err = s.auditor.Change(ctx, func(ctx context.Context, tx *audit.Tx) error {
		return s.rollbackObject(ctx, tx, t, id, toVersion, note, &pub)
	})
	if err != nil {
		return domain.Publication{}, err //nolint:wrapcheck // already an *errs.Error
	}
	s.refreshPublished(ctx, t, pub.BundleVersion)
	return pub, nil
}

func (s *Service) rollbackObject(ctx context.Context, tx *audit.Tx, t scope.Tenant, id ids.ConfigObjectID, toVersion int, note string, pub *domain.Publication) error {
	if err := s.repo.LockEnvironment(ctx, tx, t.TenantID, t.EnvironmentID); err != nil {
		return err //nolint:wrapcheck // already an *errs.Error
	}
	obj, err := s.activeObject(ctx, tx, t, id)
	if err != nil {
		return err
	}
	target, err := s.rollbackTarget(ctx, tx, t, obj, toVersion)
	if err != nil {
		return err
	}
	prev, err := s.specInForce(ctx, tx, t, obj)
	if err != nil {
		return err
	}
	next, err := s.repo.MaxVersion(ctx, tx, t.TenantID, id)
	if err != nil {
		return err //nolint:wrapcheck // already an *errs.Error
	}
	now := s.clock()
	nv := domain.Version{
		ObjectID: id, Version: next + 1, Status: domain.VersionPublished, Spec: target.Spec, Checksum: target.Checksum,
		ChangeNote: note, CreatedBy: actorName(ctx), CreatedAt: now, PublishedAt: &now,
	}
	if err := s.repo.InsertVersion(ctx, tx, t.TenantID, nv); err != nil {
		return err //nolint:wrapcheck // already an *errs.Error
	}
	before := int64(obj.CurrentVersion)
	obj.CurrentVersion = nv.Version
	obj.Version++
	obj.UpdatedAt = now
	if err := s.repo.UpdateObject(ctx, tx, t.TenantID, obj); err != nil {
		return err //nolint:wrapcheck // already an *errs.Error
	}
	if err := tx.Record(audit.Record{
		Type: EventRolledBack, Target: objectTarget(obj), Before: jsonOrNil(prev), After: target.Spec,
		BeforeVersion: before, AfterVersion: int64(nv.Version),
		Rows:    []audit.RowRef{objectRow(t.TenantID, id), versionRow(t.TenantID, id, nv.Version)},
		Details: map[string]any{"note": note, "to_version": toVersion},
	}); err != nil {
		return err //nolint:wrapcheck // already an *errs.Error
	}
	pub.Objects = []domain.PublishedObject{{ObjectID: id, Version: nv.Version}}
	return s.compileAndStore(ctx, tx, t, note, now, pub)
}

// rollbackTarget returns the version to roll back to after checking that it
// can be: no open draft, a published version other than the current one,
// whose spec still validates against its kind.
func (s *Service) rollbackTarget(ctx context.Context, tx *audit.Tx, t scope.Tenant, obj domain.Object, toVersion int) (domain.Version, error) {
	_, open, err := s.repo.OpenDraft(ctx, tx, t.TenantID, obj.ID)
	if err != nil {
		return domain.Version{}, err //nolint:wrapcheck // already an *errs.Error
	}
	if open {
		return domain.Version{}, errs.New(errs.CodeConflict, "the object has an open draft; publish it before rolling back")
	}
	target, err := s.repo.GetVersion(ctx, tx, t.TenantID, obj.ID, toVersion)
	if err != nil {
		return domain.Version{}, err //nolint:wrapcheck // already an *errs.Error
	}
	switch {
	case target.Status == domain.VersionDraft:
		return domain.Version{}, errs.New(errs.CodeConflict, "version "+strconv.Itoa(toVersion)+" was never published")
	case toVersion == obj.CurrentVersion:
		return domain.Version{}, errs.New(errs.CodeConflict, "version "+strconv.Itoa(toVersion)+" is already the current version")
	}
	k, ok := s.reg.kind(obj.Kind)
	if !ok {
		return domain.Version{}, errs.New(errs.CodeConflict, "the kind "+obj.Kind+" is no longer registered")
	}
	if issues := k.checkSpec(target.Spec); len(issues) > 0 {
		return domain.Version{}, validationFailed("version "+strconv.Itoa(toVersion)+" no longer validates against kind "+obj.Kind, issues, "/spec")
	}
	return target, nil
}

// bundleNotice is the CONFIG message that announces a published bundle.
type bundleNotice struct {
	Type     string `json:"type"`
	Version  int64  `json:"version"`
	Checksum string `json:"checksum"`
}

// NoticeBundlePublished is the type of the message sent on publish.
const NoticeBundlePublished = "bundle.published"

// compileAndStore compiles the environment's bundle from the effective
// settings and the published specs, stores it with its publication, queues
// the notification and records the audit event. It runs inside the caller's
// audited transaction, after the caller's own changes.
func (s *Service) compileAndStore(ctx context.Context, tx *audit.Tx, t scope.Tenant, note string, now time.Time, pub *domain.Publication) error {
	prevMax, err := s.repo.MaxBundleVersion(ctx, tx, t.TenantID, t.EnvironmentID)
	if err != nil {
		return err //nolint:wrapcheck // already an *errs.Error
	}
	version := prevMax + 1
	raw, checksum, err := s.compilePayload(ctx, tx, t, version)
	if err != nil {
		return err
	}
	if err := s.repo.InsertBundle(ctx, tx, t.TenantID, t.EnvironmentID, domain.BundleRow{Version: version, Checksum: checksum, Payload: raw, CreatedAt: now}); err != nil {
		return err //nolint:wrapcheck // already an *errs.Error
	}
	*pub = domain.Publication{
		EnvironmentID: t.EnvironmentID, BundleVersion: version, Checksum: checksum, PublishedAt: now,
		PublishedBy: actorName(ctx), ChangeNote: note, Objects: pub.Objects,
	}
	if err := s.repo.InsertPublication(ctx, tx, t.TenantID, *pub); err != nil {
		return err //nolint:wrapcheck // already an *errs.Error
	}
	msgID, err := s.queueNotice(ctx, tx, t, version, checksum)
	if err != nil {
		return err
	}
	env, tenant := t.EnvironmentID.UUID().String(), t.TenantID.UUID().String()
	bv := strconv.FormatInt(version, 10)
	return tx.Record(audit.Record{ //nolint:wrapcheck // already an *errs.Error
		Type:   EventBundlePublished,
		Target: &audit.Target{Type: "config_bundle", ID: t.EnvironmentID.String() + "/" + bv, Name: "bundle " + bv},
		After:  map[string]any{"version": version, "checksum": checksum}, AfterVersion: version,
		Rows: []audit.RowRef{
			rowRef("config.bundles", tenant, env, bv),
			rowRef("config.publications", tenant, env, bv),
			rowRef(outbox.Table, tenant, msgID.String()),
		},
		Details: map[string]any{"note": note, "bundle_version": version},
	})
}

// compilePayload builds the stored payload of the next bundle and checks
// that it loads (every CEL program compiles).
func (s *Service) compilePayload(ctx context.Context, tx *audit.Tx, t scope.Tenant, version int64) (json.RawMessage, string, error) {
	settings, err := s.effectiveForEnvironment(ctx, tx, t)
	if err != nil {
		return nil, "", err
	}
	specs, err := s.repo.CurrentSpecs(ctx, tx, t.TenantID, t.EnvironmentID)
	if err != nil {
		return nil, "", err //nolint:wrapcheck // already an *errs.Error
	}
	objects := map[string]map[string]json.RawMessage{}
	for _, c := range specs {
		if objects[c.Kind] == nil {
			objects[c.Kind] = map[string]json.RawMessage{}
		}
		objects[c.Kind][c.Name] = c.Spec
	}
	raw, checksum, err := encodePayload(t.TenantID, t.EnvironmentID, version, settings, objects)
	if err != nil {
		return nil, "", err
	}
	if _, err := s.reg.buildBundle(raw, checksum); err != nil {
		return nil, "", err
	}
	return raw, checksum, nil
}

// queueNotice inserts the CONFIG message that announces the bundle; the
// outbox relay publishes it once the transaction commits.
func (s *Service) queueNotice(ctx context.Context, tx *audit.Tx, t scope.Tenant, version int64, checksum string) (uuid.UUID, error) {
	msgID, err := uuid.NewV7()
	if err != nil {
		return uuid.Nil, errs.Wrap(errs.CodeInternal, err, "config: new message ID")
	}
	subject, err := bus.ConfigSubject(t.TenantID, t.EnvironmentID)
	if err != nil {
		return uuid.Nil, errs.Wrap(errs.CodeInternal, err, "config: config subject")
	}
	body, err := json.Marshal(bundleNotice{Type: NoticeBundlePublished, Version: version, Checksum: checksum})
	if err != nil {
		return uuid.Nil, errs.Wrap(errs.CodeInternal, err, "config: encode notice")
	}
	err = outbox.Insert(ctx, tx, outbox.Message{TenantID: t.TenantID, ID: msgID, Stream: bus.StreamConfig, Subject: subject, Payload: body})
	return msgID, err //nolint:wrapcheck // already an *errs.Error
}

// jsonOrNil returns raw as an audit value, or nil when there is none (a nil
// json.RawMessage inside an interface is not valid JSON).
func jsonOrNil(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	return raw
}
