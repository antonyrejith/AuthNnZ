package app

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"

	"github.com/antonyrejith/portun/internal/modules/audit"
	"github.com/antonyrejith/portun/internal/modules/tenancy/internal/domain"
	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/ids"
	"github.com/antonyrejith/portun/internal/platform/scope"
)

// Exporter writes one module's data of a tenant to w, as JSON lines. The
// context carries the tenant's scope (without environment).
type Exporter func(ctx context.Context, tenant ids.TenantID, w io.Writer) error

const (
	exportDirMode  = 0o700
	exportFileMode = 0o600
)

var exporterName = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,62}$`)

// RegisterExporter adds a module's exporter, called for every export in the
// order of the names. A name is registered once.
func (s *Service) RegisterExporter(name string, fn Exporter) error {
	if !exporterName.MatchString(name) || fn == nil {
		return errs.New(errs.CodeInternal, "tenancy: an exporter needs a lowercase name and a function")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.exporters[name]; ok {
		return errs.New(errs.CodeInternal, "tenancy: exporter "+name+" is already registered")
	}
	s.exporters[name] = fn
	return nil
}

// GetOperation returns an operation.
func (s *Service) GetOperation(ctx context.Context, id ids.OperationID) (domain.Operation, error) {
	return s.repo.GetOperation(ctx, id) //nolint:wrapcheck // repository errors are *errs.Error
}

// StartExport records an export operation and runs it in the background: the
// returned operation is running; poll GetOperation. A deleted tenant is
// errs.CodeNotFound; after Close it fails with errs.CodeUnavailable.
func (s *Service) StartExport(ctx context.Context, tenant ids.TenantID) (domain.Operation, error) {
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		return domain.Operation{}, errs.New(errs.CodeUnavailable, "tenancy is shutting down")
	}
	s.wg.Add(1)
	s.mu.Unlock()
	op, err := s.requestExport(ctx, tenant)
	if err != nil {
		s.wg.Done()
		return domain.Operation{}, err
	}
	// The export outlives the request: keep its values (actor, request) but
	// not its cancellation; Close cancels it.
	bg, stop := context.WithCancel(context.WithoutCancel(ctx))
	fin := make(chan struct{})
	go func() {
		select {
		case <-s.halt:
			stop()
		case <-fin:
		}
	}()
	go func() {
		defer s.wg.Done()
		defer close(fin)
		defer stop()
		s.runExport(bg, op)
	}()
	return op, nil
}

func (s *Service) requestExport(ctx context.Context, tenant ids.TenantID) (domain.Operation, error) {
	id, err := ids.New[ids.OperationKind]()
	if err != nil {
		return domain.Operation{}, err
	}
	now := s.now()
	op := domain.Operation{ID: id, TenantID: tenant, Kind: domain.OperationExport, Status: domain.OperationRunning, CreatedAt: now, UpdatedAt: now}
	err = s.aud.PlatformChange(ctx, func(ctx context.Context, tx *audit.Tx) error {
		t, err := s.repo.LockTenant(ctx, tx, tenant)
		if err != nil {
			return err //nolint:wrapcheck // repository errors are *errs.Error
		}
		if t.Status != domain.TenantActive && t.Status != domain.TenantSuspended {
			return errs.New(errs.CodeNotFound, "tenant not found")
		}
		if err := s.repo.InsertOperation(ctx, tx, op); err != nil {
			return err //nolint:wrapcheck // repository errors are *errs.Error
		}
		return tx.Record(audit.Record{
			Type: TypeTenantExportRequest, Target: tenantTarget(t), After: op,
			Details: map[string]any{DetailStatus: op.Status}, Rows: []audit.RowRef{rowRef("operations", id.UUID().String())},
		})
	})
	if err != nil {
		return domain.Operation{}, err //nolint:wrapcheck // *errs.Error from the audited change
	}
	return op, nil
}

// runExport writes the export file and records the outcome.
func (s *Service) runExport(ctx context.Context, op domain.Operation) {
	done := op
	path, err := s.writeExport(ctx, op)
	done.UpdatedAt = s.now()
	if err != nil {
		done.Status, done.ErrorCode = domain.OperationFailed, errs.CodeOf(err)
		s.log.ErrorContext(ctx, "tenant export failed",
			slog.String("operation_id", op.ID.String()), slog.String("tenant_id", op.TenantID.String()), slog.String("error_code", string(done.ErrorCode)))
	} else {
		done.Status, done.ResultPath = domain.OperationSucceeded, path
	}
	// The outcome is recorded even when the export was canceled.
	ctx = context.WithoutCancel(ctx)
	err = s.aud.PlatformChange(ctx, func(ctx context.Context, tx *audit.Tx) error {
		if err := s.repo.UpdateOperation(ctx, tx, done); err != nil {
			return err //nolint:wrapcheck // repository errors are *errs.Error
		}
		return tx.Record(audit.Record{
			Type: TypeTenantExportComplete, Target: &audit.Target{Type: "tenant", ID: op.TenantID.String()},
			Before: op, After: done, Details: map[string]any{DetailStatus: done.Status},
			Rows: []audit.RowRef{rowRef("operations", op.ID.UUID().String())},
		})
	})
	if err != nil {
		// The operation stays running; an operator finds it by its age.
		s.log.ErrorContext(ctx, "recording the tenant export outcome failed",
			slog.String("operation_id", op.ID.String()), slog.String("error_code", string(errs.CodeOf(err))))
	}
}

type exportRecord struct {
	Type string `json:"type"`
	Data any    `json:"data"`
}

// writeExport writes <ExportDir>/<tenant>/<operation>.jsonl and returns its
// path. A failed export leaves no file.
func (s *Service) writeExport(ctx context.Context, op domain.Operation) (_ string, err error) {
	rec, err := s.repo.TenantRecords(ctx, op.TenantID)
	if err != nil {
		return "", err //nolint:wrapcheck // repository errors are *errs.Error
	}
	dir := filepath.Join(s.exportDir, op.TenantID.String())
	if err := os.MkdirAll(dir, exportDirMode); err != nil {
		return "", errs.Wrap(errs.CodeInternal, err, "tenancy: create export directory")
	}
	file := filepath.Join(dir, op.ID.String()+".jsonl")
	f, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, exportFileMode) //nolint:gosec // G304: the path is built from the configured directory and generated IDs
	if err != nil {
		return "", errs.Wrap(errs.CodeInternal, err, "tenancy: create export file")
	}
	defer func() {
		if err != nil {
			_ = f.Close()
			_ = os.Remove(file)
		}
	}()
	bw := bufio.NewWriter(f)
	if err := s.writeSections(ctx, bw, op.TenantID, rec); err != nil {
		return "", err
	}
	if err := bw.Flush(); err != nil {
		return "", errs.Wrap(errs.CodeInternal, err, "tenancy: write export file")
	}
	if err := f.Sync(); err != nil {
		return "", errs.Wrap(errs.CodeInternal, err, "tenancy: sync export file")
	}
	if err := f.Close(); err != nil {
		return "", errs.Wrap(errs.CodeInternal, err, "tenancy: close export file")
	}
	return file, nil
}

func (s *Service) writeSections(ctx context.Context, w *bufio.Writer, tenant ids.TenantID, rec TenantRecords) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	put := func(v any) error {
		if err := enc.Encode(v); err != nil {
			return errs.Wrap(errs.CodeInternal, err, "tenancy: write export file")
		}
		return nil
	}
	if err := put(exportRecord{Type: "tenant", Data: rec.Tenant}); err != nil {
		return err
	}
	for _, e := range rec.Environments {
		if err := put(exportRecord{Type: "environment", Data: e}); err != nil {
			return err
		}
	}
	for _, h := range rec.Hosts {
		if err := put(exportRecord{Type: "host", Data: h}); err != nil {
			return err
		}
	}
	s.mu.Lock()
	names := make([]string, 0, len(s.exporters))
	fns := make(map[string]Exporter, len(s.exporters))
	for n, fn := range s.exporters {
		names = append(names, n)
		fns[n] = fn
	}
	s.mu.Unlock()
	sort.Strings(names)
	ectx := scope.WithTenant(ctx, scope.Tenant{TenantID: tenant})
	for _, n := range names {
		if err := put(map[string]string{"exporter": n}); err != nil {
			return err
		}
		if err := runExporter(ectx, fns[n], tenant, w); err != nil {
			return err
		}
	}
	return nil
}

// runExporter calls fn, turns a panic into an error and makes sure its
// output ends with a newline.
func runExporter(ctx context.Context, fn Exporter, tenant ids.TenantID, w *bufio.Writer) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = errs.New(errs.CodeInternal, fmt.Sprintf("tenancy: exporter panicked: %v", p))
		}
	}()
	lw := &lineWriter{w: w}
	if err := fn(ctx, tenant, lw); err != nil {
		return errs.Wrap(errs.CodeOf(err), err, "tenancy: exporter failed")
	}
	if lw.dirty {
		return lw.end()
	}
	return nil
}

// lineWriter tracks whether the last byte written was a newline.
type lineWriter struct {
	w     *bufio.Writer
	dirty bool
}

func (l *lineWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	l.dirty = p[len(p)-1] != '\n'
	n, err := l.w.Write(p)
	if err != nil {
		return n, errs.Wrap(errs.CodeInternal, err, "tenancy: write export file")
	}
	return n, nil
}

func (l *lineWriter) end() error {
	if err := l.w.WriteByte('\n'); err != nil {
		return errs.Wrap(errs.CodeInternal, err, "tenancy: write export file")
	}
	return nil
}

// Close stops accepting exports and waits for the running ones. When ctx is
// done first, the running exports are canceled (they record as failed), and
// Close returns the context's error after they have stopped.
func (s *Service) Close(ctx context.Context) error {
	s.mu.Lock()
	s.closing = true
	s.mu.Unlock()
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		s.haltOnce.Do(func() { close(s.halt) })
		<-done
		return errs.Wrap(errs.CodeOf(ctx.Err()), ctx.Err(), "tenancy: exports canceled on shutdown")
	}
}
