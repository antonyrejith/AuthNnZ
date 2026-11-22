package keystore_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/ids"
	"github.com/antonyrejith/portun/internal/platform/keystore"
	"github.com/antonyrejith/portun/internal/platform/kms"
	"github.com/antonyrejith/portun/internal/platform/kms/local"
)

// stubRow is a pgx.Row that fails or fills a single string column.
type stubRow struct {
	err error
	val string
}

func (r stubRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if p, ok := dest[0].(*string); ok {
		*p = r.val
	}
	return nil
}

// stubTx is a keystore.DBTX whose answers the test controls.
type stubTx struct {
	execErr  error
	execs    int
	queryRow func(sql string) pgx.Row
}

func (s *stubTx) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	s.execs++
	return pgconn.CommandTag{}, s.execErr
}

func (s *stubTx) QueryRow(_ context.Context, sql string, _ ...any) pgx.Row {
	return s.queryRow(sql)
}

func failingRows(err error) *stubTx {
	return &stubTx{execErr: err, queryRow: func(string) pgx.Row { return stubRow{err: err} }}
}

// failKMS fails the operations the key store uses to create and destroy keys.
type failKMS struct {
	kms.KMS
	createErr, destroyErr, encryptErr error
}

func (f failKMS) CreateKey(ctx context.Context, name string, p kms.Purpose) (kms.KeyRef, error) {
	if f.createErr != nil {
		return "", f.createErr
	}
	return f.KMS.CreateKey(ctx, name, p)
}

func (f failKMS) DestroyKey(ctx context.Context, ref kms.KeyRef) error {
	if f.destroyErr != nil {
		return f.destroyErr
	}
	return f.KMS.DestroyKey(ctx, ref)
}

func (f failKMS) Encrypt(ctx context.Context, ref kms.KeyRef, pt, aad []byte) ([]byte, error) {
	if f.encryptErr != nil {
		return nil, f.encryptErr
	}
	return f.KMS.Encrypt(ctx, ref, pt, aad)
}

func freshTenant(t *testing.T) ids.TenantID {
	t.Helper()
	id, err := ids.New[ids.TenantKind]()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func memKMS(t *testing.T) *local.KMS {
	t.Helper()
	k, err := local.NewMemory()
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// TestSubjectLengthValidated: an empty or oversized subject is refused
// before the database is touched.
//
// Covers: SEC-08
func TestSubjectLengthValidated(t *testing.T) {
	t.Parallel()
	s := keystore.New(memKMS(t), keystore.Config{})
	tn := freshTenant(t)
	tx := &stubTx{queryRow: func(string) pgx.Row { t.Error("database queried for an invalid subject"); return stubRow{} }}
	for _, subject := range []string{"", strings.Repeat("s", keystore.MaxSubjectLength+1)} {
		if _, err := s.SubjectKey(t.Context(), tx, tn, subject); !errs.Is(err, errs.CodeInternal) {
			t.Errorf("SubjectKey(len %d) = %v", len(subject), err)
		}
		if _, _, err := s.LookupSubjectKey(t.Context(), tx, tn, subject); !errs.Is(err, errs.CodeInternal) {
			t.Errorf("LookupSubjectKey(len %d) = %v", len(subject), err)
		}
		if _, err := s.ShredSubject(t.Context(), tx, tn, subject); !errs.Is(err, errs.CodeInternal) {
			t.Errorf("ShredSubject(len %d) = %v", len(subject), err)
		}
	}
	// The longest allowed subject reaches the database.
	reached := false
	tx = &stubTx{queryRow: func(string) pgx.Row { reached = true; return stubRow{err: pgx.ErrNoRows} }}
	if _, found, err := s.LookupSubjectKey(t.Context(), tx, tn, strings.Repeat("s", keystore.MaxSubjectLength)); err != nil || found || !reached {
		t.Errorf("max-length subject: found=%v err=%v reached=%v", found, err, reached)
	}
}

// TestDatabaseErrorsMapped: a failing database surfaces as an errs code from
// every operation.
//
// Covers: SEC-08
func TestDatabaseErrorsMapped(t *testing.T) {
	t.Parallel()
	ops := []struct {
		name string
		call func(s *keystore.Store, tx keystore.DBTX, tn ids.TenantID) error
	}{
		{"CreateTenantKEK", func(s *keystore.Store, tx keystore.DBTX, tn ids.TenantID) error {
			_, _, err := s.CreateTenantKEK(context.Background(), tx, tn)
			return err
		}},
		{"DestroyTenantKEK", func(s *keystore.Store, tx keystore.DBTX, tn ids.TenantID) error {
			_, err := s.DestroyTenantKEK(context.Background(), tx, tn)
			return err
		}},
		{"LookupSubjectKey", func(s *keystore.Store, tx keystore.DBTX, tn ids.TenantID) error {
			_, _, err := s.LookupSubjectKey(context.Background(), tx, tn, "usr_a")
			return err
		}},
		{"SubjectKey", func(s *keystore.Store, tx keystore.DBTX, tn ids.TenantID) error {
			_, err := s.SubjectKey(context.Background(), tx, tn, "usr_a")
			return err
		}},
		{"DataKey", func(s *keystore.Store, tx keystore.DBTX, tn ids.TenantID) error {
			id, _ := ids.New[ids.KeyKind]()
			_, err := s.DataKey(context.Background(), tx, tn, id)
			return err
		}},
		{"ShredSubject", func(s *keystore.Store, tx keystore.DBTX, tn ids.TenantID) error {
			_, err := s.ShredSubject(context.Background(), tx, tn, "usr_a")
			return err
		}},
	}
	causes := []struct {
		name string
		err  error
		want errs.Code
	}{
		{"generic", errors.New("boom"), errs.CodeInternal},
		{"canceled", context.Canceled, errs.CodeUnavailable},
		{"deadline", context.DeadlineExceeded, errs.CodeDeadlineExceeded},
	}
	for _, op := range ops {
		for _, c := range causes {
			t.Run(op.name+"/"+c.name, func(t *testing.T) {
				t.Parallel()
				s := keystore.New(memKMS(t), keystore.Config{})
				err := op.call(s, failingRows(c.err), freshTenant(t))
				if !errs.Is(err, c.want) {
					t.Errorf("%s = %v, want code %v", op.name, err, c.want)
				}
			})
		}
	}
}

// TestNoRowsResults: "no row" answers are normal results for lookups and
// shredding and the documented errors for creation and destruction.
//
// Covers: SEC-08
func TestNoRowsResults(t *testing.T) {
	t.Parallel()
	s := keystore.New(memKMS(t), keystore.Config{})
	tn := freshTenant(t)
	tx := func() *stubTx { return failingRows(pgx.ErrNoRows) }

	if _, found, err := s.LookupSubjectKey(t.Context(), tx(), tn, "usr_a"); err != nil || found {
		t.Errorf("LookupSubjectKey = found %v, %v; want not found without error", found, err)
	}
	if rows, err := s.ShredSubject(t.Context(), tx(), tn, "usr_a"); err != nil || rows != nil {
		t.Errorf("ShredSubject = %v, %v; want nothing", rows, err)
	}
	if _, err := s.SubjectKey(t.Context(), tx(), tn, "usr_a"); !errs.Is(err, errs.CodeInternal) {
		t.Errorf("SubjectKey without KEK = %v, want internal", err)
	}
	id, _ := ids.New[ids.KeyKind]()
	err := func() error { _, err := s.DataKey(t.Context(), tx(), tn, id); return err }()
	if !errs.Is(err, errs.CodeNotFound) || errors.Is(err, keystore.ErrShredded) {
		t.Errorf("DataKey of an unknown ID = %v, want plain not found", err)
	}
	if _, err := s.DestroyTenantKEK(t.Context(), tx(), tn); !errs.Is(err, errs.CodeNotFound) {
		t.Errorf("DestroyTenantKEK without KEK = %v, want not found", err)
	}
}

// TestKMSFailures: KMS errors are returned (wrapped) and no database write
// follows a failed KMS call.
//
// Covers: SEC-08
func TestKMSFailures(t *testing.T) {
	t.Parallel()
	boom := errors.New("kms unavailable")
	tn := freshTenant(t)

	t.Run("create KEK", func(t *testing.T) {
		t.Parallel()
		s := keystore.New(failKMS{KMS: memKMS(t), createErr: boom}, keystore.Config{})
		tx := failingRows(nil)
		if _, rows, err := s.CreateTenantKEK(t.Context(), tx, tn); !errors.Is(err, boom) || rows != nil {
			t.Errorf("CreateTenantKEK = %v, %v", rows, err)
		}
		if tx.execs != 0 {
			t.Errorf("%d inserts after a failed KMS call", tx.execs)
		}
	})
	t.Run("destroy KEK", func(t *testing.T) {
		t.Parallel()
		s := keystore.New(failKMS{KMS: memKMS(t), destroyErr: boom}, keystore.Config{})
		tx := &stubTx{queryRow: func(string) pgx.Row { return stubRow{val: "kek-ref"} }}
		if rows, err := s.DestroyTenantKEK(t.Context(), tx, tn); !errors.Is(err, boom) || rows != nil {
			t.Errorf("DestroyTenantKEK = %v, %v", rows, err)
		}
	})
	t.Run("new data key", func(t *testing.T) {
		t.Parallel()
		k := memKMS(t)
		ref, err := k.CreateKey(t.Context(), "kek-test", kms.PurposeEncrypt)
		if err != nil {
			t.Fatal(err)
		}
		s := keystore.New(failKMS{KMS: k, encryptErr: boom}, keystore.Config{})
		tx := &stubTx{queryRow: func(sql string) pgx.Row {
			if strings.Contains(sql, "tenant_keks") {
				return stubRow{val: string(ref)}
			}
			return stubRow{err: pgx.ErrNoRows} // no existing data key
		}}
		if _, err := s.SubjectKey(t.Context(), tx, tn, "usr_a"); !errors.Is(err, boom) {
			t.Errorf("SubjectKey = %v, want the KMS error", err)
		}
	})
}
