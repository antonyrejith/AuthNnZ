// Command portunctl is the Portun operator CLI. This build provides the
// audit integrity commands (GOV-18):
//
//	portunctl audit verify  [--tenant ID|platform] [--from N|RFC3339] [--to N|RFC3339]
//	                        [--file export.jsonl | --db-url URL] [--trusted-keys keys.pem]
//	                        [--tsa-roots roots.pem] [--require-tsa] [--json]
//	portunctl audit export  [--tenant ID|platform] [--from N] [--to N] [--db-url URL] [--out file]
//	portunctl audit keys    [--db-url URL]
//
// The database URL may also come from PORTUN_AUDIT_DB_URL (preferred: it
// keeps the password out of the process list); it must connect as
// portun_platform. Database commands record the access in the audit trail
// first (--actor, default $USER; --reason).
//
// Exit status: 0 verified, 1 integrity failures found, 2 usage or runtime
// error.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/antonyrejith/portun/internal/modules/audit"
	"github.com/antonyrejith/portun/internal/platform/secret"
)

const (
	exitOK       = 0
	exitFailures = 1
	exitError    = 2
	dbURLEnv     = "PORTUN_AUDIT_DB_URL"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) < 2 || args[0] != "audit" {
		_, _ = fmt.Fprintln(stderr, "usage: portunctl audit verify|export|keys [flags]")
		return exitError
	}
	var err error
	code := exitOK
	switch args[1] {
	case "verify":
		code, err = verifyCmd(ctx, args[2:], stdout)
	case "export":
		err = exportCmd(ctx, args[2:], stdout)
	case "keys":
		err = keysCmd(ctx, args[2:], stdout)
	default:
		err = fmt.Errorf("unknown command %q", args[1])
	}
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "portunctl:", err)
		return exitError
	}
	return code
}

type common struct {
	tenant, dbURL, actor, reason string
}

func (c *common) register(fs *flag.FlagSet) {
	fs.StringVar(&c.tenant, "tenant", "platform", "chain: a tenant ID or \"platform\"")
	fs.StringVar(&c.dbURL, "db-url", "", "database URL as portun_platform (default $"+dbURLEnv+")")
	fs.StringVar(&c.actor, "actor", os.Getenv("USER"), "operator ID recorded in the audit trail")
	fs.StringVar(&c.reason, "reason", "", "reason recorded in the audit trail")
}

func (c *common) url() (secret.String, error) {
	u := c.dbURL
	if u == "" {
		u = os.Getenv(dbURLEnv)
	}
	if u == "" {
		return secret.String{}, errors.New("a database URL is required (--db-url or $" + dbURLEnv + ")")
	}
	return secret.NewString(u), nil
}

// bound parses a sequence number or an RFC 3339 time.
func bound(s string) (uint64, time.Time, error) {
	if s == "" {
		return 0, time.Time{}, nil
	}
	if n, err := strconv.ParseUint(s, 10, 64); err == nil {
		return n, time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return 0, time.Time{}, fmt.Errorf("%q is neither a sequence number nor an RFC 3339 time", s)
	}
	return 0, t, nil
}

func verifyCmd(ctx context.Context, args []string, stdout io.Writer) (int, error) {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	var c common
	c.register(fs)
	from := fs.String("from", "", "first sequence number or RFC 3339 time")
	to := fs.String("to", "", "last sequence number or RFC 3339 time")
	file := fs.String("file", "", "verify an export file instead of the database")
	keysFile := fs.String("trusted-keys", "", "PEM file of trusted signing keys (default: keys published in the source)")
	rootsFile := fs.String("tsa-roots", "", "PEM file of trusted timestamp authority roots")
	requireTSA := fs.Bool("require-tsa", false, "fail checkpoints without a timestamp token")
	asJSON := fs.Bool("json", false, "print the report as JSON")
	if err := fs.Parse(args); err != nil {
		return exitError, err //nolint:wrapcheck // printed as is
	}
	var opts audit.VerifyOptions
	var err error
	if opts.FromSeq, opts.From, err = bound(*from); err != nil {
		return exitError, err
	}
	if opts.ToSeq, opts.To, err = bound(*to); err != nil {
		return exitError, err
	}
	opts.RequireTSA = *requireTSA
	if *keysFile != "" {
		data, err := os.ReadFile(*keysFile)
		if err != nil {
			return exitError, err //nolint:wrapcheck // printed as is
		}
		if opts.TrustedKeys, err = audit.ParseTrustedKeys(data); err != nil {
			return exitError, err //nolint:wrapcheck // printed as is
		}
	}
	if *rootsFile != "" {
		data, err := os.ReadFile(*rootsFile)
		if err != nil {
			return exitError, err //nolint:wrapcheck // printed as is
		}
		if opts.TSARoots, err = audit.ParseTSARoots(data); err != nil {
			return exitError, err //nolint:wrapcheck // printed as is
		}
	}
	rep, err := verifySource(ctx, &c, *file, opts)
	if err != nil {
		return exitError, err
	}
	if err := printReport(stdout, rep, *asJSON); err != nil {
		return exitError, err
	}
	if !rep.OK() {
		return exitFailures, nil
	}
	return exitOK, nil
}

func verifySource(ctx context.Context, c *common, file string, opts audit.VerifyOptions) (*audit.VerifyReport, error) {
	if file != "" {
		f, err := os.Open(file) //nolint:gosec // G304: the operator names the file to verify
		if err != nil {
			return nil, err //nolint:wrapcheck // printed as is
		}
		defer func() { _ = f.Close() }()
		return audit.VerifyExport(ctx, f, opts) //nolint:wrapcheck // printed as is
	}
	u, err := c.url()
	if err != nil {
		return nil, err
	}
	return audit.VerifyDatabase(ctx, u, c.tenant, audit.Operator{ID: c.actor, Reason: c.reason}, opts) //nolint:wrapcheck // printed as is
}

func printReport(w io.Writer, rep *audit.VerifyReport, asJSON bool) error {
	if asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(rep) //nolint:wrapcheck // stdout
	}
	status := "OK"
	if !rep.OK() {
		status = "FAILED"
	}
	_, err := fmt.Fprintf(w, "chain %s: %s\n  events %d..%d checked: %d\n  checkpoints: %d (timestamps verified %d, not verified %d)\n  retention marks: %d\n  events after the last checkpoint: %d\n",
		rep.Chain, status, rep.FromSeq, rep.ToSeq, rep.Events, rep.Checkpoints, rep.TSAVerified, rep.TSAUnverified, rep.RetentionMarks, rep.Unanchored)
	if err != nil {
		return err //nolint:wrapcheck // stdout
	}
	if rep.KeysFromSource {
		_, _ = fmt.Fprintln(w, "  note: signatures were checked with keys from the source; pass --trusted-keys for authenticity")
	}
	if f := rep.First(); f != nil {
		_, _ = fmt.Fprintf(w, "  first failure: %s at sequence %d: %s\n", f.Kind, f.Sequence, f.Detail)
	}
	for _, f := range rep.Failures {
		_, _ = fmt.Fprintf(w, "  - %s seq=%d checkpoint=%s: %s\n", f.Kind, f.Sequence, f.CheckpointID, f.Detail)
	}
	return nil
}

func exportCmd(ctx context.Context, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("export", flag.ContinueOnError)
	var c common
	c.register(fs)
	from := fs.Uint64("from", 0, "first sequence number (0: from the start)")
	to := fs.Uint64("to", 0, "last sequence number (0: to the head)")
	out := fs.String("out", "", "output file (default stdout)")
	if err := fs.Parse(args); err != nil {
		return err //nolint:wrapcheck // printed as is
	}
	u, err := c.url()
	if err != nil {
		return err
	}
	w := stdout
	if *out != "" {
		f, err := os.OpenFile(*out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err //nolint:wrapcheck // printed as is
		}
		defer func() { _ = f.Close() }()
		w = f
	}
	return audit.ExportDatabase(ctx, u, c.tenant, audit.Operator{ID: c.actor, Reason: c.reason}, *from, *to, w) //nolint:wrapcheck // printed as is
}

func keysCmd(ctx context.Context, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("keys", flag.ContinueOnError)
	var c common
	c.register(fs)
	if err := fs.Parse(args); err != nil {
		return err //nolint:wrapcheck // printed as is
	}
	u, err := c.url()
	if err != nil {
		return err
	}
	pemData, err := audit.SigningKeysPEM(ctx, u)
	if err != nil {
		return err //nolint:wrapcheck // printed as is
	}
	_, err = stdout.Write(pemData)
	return err //nolint:wrapcheck // stdout
}
