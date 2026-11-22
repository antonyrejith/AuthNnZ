package app

import (
	"context"
	"log/slog"
	"strconv"
	"time"

	"github.com/antonyrejith/portun/internal/modules/audit"
	"github.com/antonyrejith/portun/internal/platform/bus"
	"github.com/antonyrejith/portun/internal/platform/db"
	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/ids"
	"github.com/antonyrejith/portun/internal/platform/scope"
)

// Event type names (registered by the facade).
const (
	EventDraftSaved             = "config.draft_saved"
	EventVersionPublished       = "config.version_published"
	EventRolledBack             = "config.rolled_back"
	EventBundlePublished        = "config.bundle_published"
	EventSettingChanged         = "config.setting_changed"
	EventSettingsReset          = "config.settings_reset"
	EventPlatformSettingChanged = "config.platform_setting_changed"
)

// Defaults of the runtime.
const (
	DefaultCacheSize    = 1024
	DefaultPollInterval = 10 * time.Second
)

// maxNoteLength bounds a change note.
const maxNoteLength = 1000

// Deps are the Service's dependencies.
type Deps struct {
	DB           *db.DB
	Auditor      *audit.Auditor
	Bus          *bus.Bus // optional: without it bundles propagate by polling only
	Registry     *Registry
	Repo         Repo
	InstanceID   string
	Clock        func() time.Time
	Logger       *slog.Logger
	CacheSize    int
	PollInterval time.Duration
}

// Service implements the config use cases.
type Service struct {
	db         *db.DB
	auditor    *audit.Auditor
	bus        *bus.Bus
	reg        *Registry
	repo       Repo
	instanceID string
	clock      func() time.Time
	logger     *slog.Logger
	cache      *bundleCache
	poll       time.Duration
}

// New returns a Service. It fails with errs.CodeInternal when a required
// dependency is missing.
func New(d Deps) (*Service, error) {
	switch {
	case d.DB == nil, d.Auditor == nil, d.Registry == nil, d.Repo == nil:
		return nil, errs.New(errs.CodeInternal, "config: DB, Auditor, Registry and Repo are required")
	case d.InstanceID == "":
		return nil, errs.New(errs.CodeInternal, "config: InstanceID is required (it names the bus consumer)")
	}
	if d.Clock == nil {
		d.Clock = time.Now
	}
	if d.Logger == nil {
		d.Logger = slog.New(slog.DiscardHandler)
	}
	if d.CacheSize <= 0 {
		d.CacheSize = DefaultCacheSize
	}
	if d.PollInterval <= 0 {
		d.PollInterval = DefaultPollInterval
	}
	return &Service{
		db: d.DB, auditor: d.Auditor, bus: d.Bus, reg: d.Registry, repo: d.Repo,
		instanceID: d.InstanceID, clock: func() time.Time { return d.Clock().UTC() },
		logger: d.Logger.With(slog.String("component", "config")),
		cache:  newBundleCache(d.CacheSize), poll: d.PollInterval,
	}, nil
}

// envScope returns the tenant scope, which must carry an environment.
func envScope(ctx context.Context) (scope.Tenant, error) {
	t, err := scope.TenantFrom(ctx)
	if err != nil {
		return scope.Tenant{}, err //nolint:wrapcheck // already an *errs.Error
	}
	if t.EnvironmentID.IsZero() {
		return scope.Tenant{}, errs.New(errs.CodeInvalidRequest, "an environment is required")
	}
	return t, nil
}

// actorName is the recorded name of the acting party.
func actorName(ctx context.Context) string {
	a, ok := scope.ActorFrom(ctx)
	if !ok || a.ID == "" {
		return "system"
	}
	return string(a.Type) + ":" + a.ID
}

func validationFailed(msg string, issues []Issue, prefix string) error {
	fields := make([]errs.FieldError, 0, len(issues))
	for _, i := range issues {
		fields = append(fields, errs.FieldError{Pointer: prefix + i.Pointer, Code: i.Code, Detail: i.Message})
	}
	return errs.New(errs.CodeValidationFailed, msg).WithFields(fields...)
}

func fieldError(pointer, code, detail string) error {
	return errs.New(errs.CodeValidationFailed, detail).WithFields(errs.FieldError{Pointer: pointer, Code: code, Detail: detail})
}

func checkNote(note string) error {
	if len(note) > maxNoteLength {
		return fieldError("/note", "too_long", "the note is longer than "+strconv.Itoa(maxNoteLength)+" bytes")
	}
	return nil
}

// read runs fn in a read-only transaction bound to the context's tenant.
func (s *Service) read(ctx context.Context, fn func(ctx context.Context, q db.Querier) error) error {
	return s.db.ReadTenant(ctx, fn) //nolint:wrapcheck // db maps errors
}

// rowRef builds the audit reference of a written row.
func rowRef(table string, key ...string) audit.RowRef { return audit.RowRef{Table: table, Key: key} }

func objectRow(tenant ids.TenantID, id ids.ConfigObjectID) audit.RowRef {
	return rowRef("config.objects", tenant.UUID().String(), id.UUID().String())
}

func versionRow(tenant ids.TenantID, id ids.ConfigObjectID, version int) audit.RowRef {
	return rowRef("config.versions", tenant.UUID().String(), id.UUID().String(), strconv.Itoa(version))
}

func settingRow(tenant ids.TenantID, levelEnv ids.EnvironmentID, key string) audit.RowRef {
	return rowRef("config.settings", tenant.UUID().String(), key, levelEnv.UUID().String())
}
