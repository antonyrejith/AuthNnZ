package audit_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// writePathAllowlist lists the package trees that may open transactions or
// hold a pgx.Tx: the audit module (audit.Tx is the only write path), the
// outbox relay, the database layer and the key store (which works inside a
// caller's audited transaction). Reads elsewhere use db.ReadTenant and
// db.ReadPlatform, which are read-only and hand out only a db.Querier.
var writePathAllowlist = []string{
	"internal/modules/audit",
	"internal/platform/outbox",
	"internal/platform/db",
	"internal/platform/keystore",
}

const (
	pgxPath     = "github.com/jackc/pgx/v5"
	pgxpoolPath = "github.com/jackc/pgx/v5/pgxpool"
)

// writePathViolations reports every non-test Go file under root, outside
// the allowlist, that calls InTenantTx or InPlatformTx, names pgx.Tx, or
// begins a transaction on a pgx connection or pool. testdata, hidden and
// tool directories are skipped.
func writePathViolations(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			name := d.Name()
			if rel != "." && (strings.HasPrefix(name, ".") || name == "testdata" || name == "bin" || name == "tools" || name == "vendor") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || allowed(rel) {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		for _, v := range fileViolations(f) {
			out = append(out, rel+": "+v)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func allowed(rel string) bool {
	for _, p := range writePathAllowlist {
		if rel == p || strings.HasPrefix(rel, p+"/") {
			return true
		}
	}
	return false
}

func fileViolations(f *ast.File) []string {
	names := map[string]string{} // local name -> import path
	for _, imp := range f.Imports {
		p, _ := strconv.Unquote(imp.Path.Value)
		local := p[strings.LastIndex(p, "/")+1:]
		if p == pgxPath {
			local = "pgx"
		}
		if imp.Name != nil {
			local = imp.Name.Name
		}
		names[local] = p
	}
	usesPgx := false
	for _, p := range names {
		if p == pgxPath || p == pgxpoolPath {
			usesPgx = true
		}
	}
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		switch sel.Sel.Name {
		case "InTenantTx", "InPlatformTx":
			out = append(out, "calls "+sel.Sel.Name+" outside audit.Change")
		case "Begin", "BeginTx":
			if usesPgx {
				out = append(out, "begins a database transaction outside audit.Change")
			}
		case "Tx":
			if x, ok := sel.X.(*ast.Ident); ok && names[x.Name] == pgxPath {
				out = append(out, "uses pgx.Tx outside audit.Change")
			}
		}
		return true
	})
	return out
}

// TestNoWritesOutsideAudit is the architecture test of guarantee 2 (plan 13
// §2): no package outside the allowlist opens transactions or holds a
// pgx.Tx, so every write goes through audit.Tx.
//
// Covers: GOV-11
func TestNoWritesOutsideAudit(t *testing.T) {
	t.Parallel()
	if v := writePathViolations(t, filepath.Join("..", "..", "..")); len(v) > 0 {
		t.Errorf("database writes outside audit.Change:\n%s", strings.Join(v, "\n"))
	}
}

// TestArchCheckCatchesFixture proves the check fails on code that bypasses
// the audit path.
//
// Covers: GOV-11
func TestArchCheckCatchesFixture(t *testing.T) {
	t.Parallel()
	v := writePathViolations(t, filepath.Join("testdata", "archfixture"))
	want := []string{"calls InTenantTx", "calls InPlatformTx", "uses pgx.Tx", "begins a database transaction"}
	all := strings.Join(v, "\n")
	for _, w := range want {
		if !strings.Contains(all, w) {
			t.Errorf("fixture violation %q not reported; got:\n%s", w, all)
		}
	}
}
