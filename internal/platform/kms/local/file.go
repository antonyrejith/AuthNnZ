package local

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/antonyrejith/portun/internal/platform/crypto"
	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/kms"
	"github.com/antonyrejith/portun/internal/platform/secret"
)

const (
	fileFormat = 1
	fileMode   = 0o600
	dirMode    = 0o700
)

// checkAAD authenticates a check value sealed under the master key in every
// file, so Open can tell a wrong master key from a damaged file.
var checkAAD = []byte("portun.kms.local.v1\x00check") //nolint:gochecknoglobals // immutable constant bytes

type fileState struct {
	Format int                `json:"format"`
	Check  []byte             `json:"check"`
	Keys   map[string]fileKey `json:"keys"`
}

type fileKey struct {
	Purpose   string   `json:"purpose"`
	Destroyed bool     `json:"destroyed,omitempty"`
	Versions  [][]byte `json:"versions,omitempty"`
}

// Open opens or creates the key file at path, whose material is encrypted
// under masterKey (32 bytes). The parent directory is created with mode 0700
// if missing. An existing file must be a regular file that only its owner can
// access (mode 0600 or stricter) and must have been written with the same
// master key.
func Open(path string, masterKey secret.Bytes) (*KMS, error) {
	master := masterKey.Reveal()
	if len(master) != crypto.KeySize {
		return nil, errs.New(errs.CodeInvalidRequest, fmt.Sprintf("local KMS master key must be %d bytes", crypto.KeySize))
	}
	if path == "" {
		return nil, errs.New(errs.CodeInvalidRequest, "local KMS key file path is empty")
	}
	path = filepath.Clean(path)
	if err := os.MkdirAll(filepath.Dir(path), dirMode); err != nil {
		return nil, errs.Wrap(errs.CodeInternal, err, "create key directory")
	}
	k := &KMS{master: master, path: path, keys: map[string]*key{}}
	removeStaleTemps(path)
	info, err := os.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if err := k.persist(); err != nil {
			return nil, err
		}
		return k, nil
	case err != nil:
		return nil, errs.Wrap(errs.CodeInternal, err, "stat key file")
	case !info.Mode().IsRegular():
		return nil, errs.New(errs.CodeInvalidRequest, "local KMS key file is not a regular file")
	case info.Mode().Perm()&0o077 != 0:
		return nil, errs.New(errs.CodeInvalidRequest,
			fmt.Sprintf("local KMS key file must not be accessible by group or others (mode %#o, want 0600)", info.Mode().Perm()))
	}
	if err := k.load(); err != nil {
		return nil, err
	}
	return k, nil
}

func (k *KMS) load() error {
	data, err := os.ReadFile(k.path)
	if err != nil {
		return errs.Wrap(errs.CodeInternal, err, "read key file")
	}
	var st fileState
	if err := json.Unmarshal(data, &st); err != nil {
		return errs.Wrap(errs.CodeInvalidRequest, err, "local KMS key file is damaged")
	}
	if st.Format != fileFormat {
		return errs.New(errs.CodeInvalidRequest, fmt.Sprintf("local KMS key file has unknown format %d", st.Format))
	}
	if _, err := crypto.Open(k.master, st.Check, checkAAD); err != nil {
		return errs.Wrap(errs.CodeInvalidRequest, err, "local KMS master key does not match the key file")
	}
	for name, fk := range st.Keys {
		kk, err := decodeKey(name, fk)
		if err != nil {
			return err
		}
		k.keys[name] = kk
	}
	return nil
}

func decodeKey(name string, fk fileKey) (*key, error) {
	if err := kms.ValidateName(name); err != nil {
		return nil, errs.Wrap(errs.CodeInvalidRequest, err, "local KMS key file is damaged")
	}
	purpose, err := kms.ParsePurpose(fk.Purpose)
	if err != nil {
		return nil, errs.Wrap(errs.CodeInvalidRequest, err, "local KMS key file is damaged")
	}
	if fk.Destroyed != (len(fk.Versions) == 0) {
		return nil, errs.New(errs.CodeInvalidRequest, "local KMS key file is damaged: key "+name+" has inconsistent versions")
	}
	return &key{purpose: purpose, destroyed: fk.Destroyed, versions: fk.Versions}, nil
}

// persist writes the whole state to the key file. Callers hold k.mu for
// writing. In-memory KMSs do nothing.
func (k *KMS) persist() error {
	if k.path == "" {
		return nil
	}
	check, err := crypto.Seal(k.master, []byte("portun"), checkAAD)
	if err != nil {
		return wrap(err)
	}
	st := fileState{Format: fileFormat, Check: check, Keys: make(map[string]fileKey, len(k.keys))}
	for name, kk := range k.keys {
		st.Keys[name] = fileKey{Purpose: kk.purpose.String(), Destroyed: kk.destroyed, Versions: kk.versions}
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return errs.Wrap(errs.CodeInternal, err, "encode key file")
	}
	return writeAtomic(k.path, data)
}

func tempPattern(path string) string {
	return "." + filepath.Base(path) + ".*.tmp"
}

// writeAtomic replaces path with data: temporary file in the same directory
// (mode 0600), fsync, rename, directory fsync. The replaced file's blocks are
// then overwritten with zeros on a best-effort basis.
func writeAtomic(path string, data []byte) (err error) {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, tempPattern(path))
	if err != nil {
		return errs.Wrap(errs.CodeInternal, err, "create temporary key file")
	}
	tmp := f.Name()
	defer func() {
		if err != nil {
			_ = f.Close()
			_ = os.Remove(tmp)
		}
	}()
	if err = f.Chmod(fileMode); err != nil {
		return errs.Wrap(errs.CodeInternal, err, "set key file mode")
	}
	if _, err = f.Write(data); err != nil {
		return errs.Wrap(errs.CodeInternal, err, "write key file")
	}
	if err = f.Sync(); err != nil {
		return errs.Wrap(errs.CodeInternal, err, "sync key file")
	}
	if err = f.Close(); err != nil {
		return errs.Wrap(errs.CodeInternal, err, "close key file")
	}
	// Keep a handle on the previous file so its blocks can be scrubbed once
	// it is no longer reachable by name.
	old, openErr := os.OpenFile(path, os.O_RDWR, 0) //nolint:gosec // G304: path is the operator-configured key file
	if err = os.Rename(tmp, path); err != nil {
		if openErr == nil {
			_ = old.Close()
		}
		return errs.Wrap(errs.CodeInternal, err, "replace key file")
	}
	if err = syncDir(dir); err != nil {
		if openErr == nil {
			_ = old.Close()
		}
		return err
	}
	if openErr == nil {
		scrub(old)
	}
	return nil
}

func syncDir(dir string) error {
	d, err := os.Open(dir) //nolint:gosec // G304: the key file's own directory
	if err != nil {
		return errs.Wrap(errs.CodeInternal, err, "open key directory")
	}
	defer func() { _ = d.Close() }()
	if err := d.Sync(); err != nil {
		return errs.Wrap(errs.CodeInternal, err, "sync key directory")
	}
	return nil
}

// scrub overwrites an unlinked file with zeros and closes it. It is best
// effort: the new state is already committed, so failures are not reported.
func scrub(f *os.File) {
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return
	}
	zeros := make([]byte, info.Size())
	if _, err := f.WriteAt(zeros, 0); err != nil {
		return
	}
	_ = f.Sync()
}

// removeStaleTemps scrubs and removes temporary files left by a crash during
// a write. Best effort: they contain only encrypted material.
func removeStaleTemps(path string) {
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(path), tempPattern(path)))
	if err != nil {
		return
	}
	for _, m := range matches {
		if f, err := os.OpenFile(m, os.O_RDWR, 0); err == nil { //nolint:gosec // G304: temporary files of the configured key file
			scrub(f)
		}
		_ = os.Remove(m)
	}
}
