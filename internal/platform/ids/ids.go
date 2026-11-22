// Package ids provides typed, prefixed, time-ordered identifiers.
//
// IDs are UUIDv7 values stored as PostgreSQL uuid and shown externally in the
// TypeID format: "<prefix>_<26 lowercase Crockford base32 characters>", for
// example "ten_01h455vb4pex5vsknk084sn02q". Each kind of ID is a distinct Go
// type, so an ID of one kind cannot be used where another kind is expected
// (ADR-0002).
package ids

import (
	"database/sql/driver"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/antonyrejith/portun/internal/platform/errs"
)

// Kind names one kind of ID by its prefix.
type Kind interface {
	Prefix() string
}

// ID is an identifier of kind K. The zero value is the "no ID" value.
type ID[K Kind] struct {
	u uuid.UUID
}

// New returns a new time-ordered ID.
func New[K Kind]() (ID[K], error) {
	u, err := uuid.NewV7()
	if err != nil {
		return ID[K]{}, errs.Wrap(errs.CodeInternal, err, "generate identifier")
	}
	return ID[K]{u: u}, nil
}

// FromUUID wraps an existing UUID.
func FromUUID[K Kind](u uuid.UUID) ID[K] {
	return ID[K]{u: u}
}

// Parse parses the external form "<prefix>_<suffix>".
func Parse[K Kind](s string) (ID[K], error) {
	var k K
	prefix, suffix, ok := strings.Cut(s, "_")
	if !ok || prefix != k.Prefix() {
		return ID[K]{}, errs.New(errs.CodeInvalidRequest, fmt.Sprintf("identifier must start with %q", k.Prefix()+"_"))
	}
	u, err := decode(suffix)
	if err != nil {
		return ID[K]{}, err
	}
	return ID[K]{u: u}, nil
}

// String returns the external form.
func (id ID[K]) String() string {
	var k K
	return k.Prefix() + "_" + encode(id.u)
}

// UUID returns the underlying UUID.
func (id ID[K]) UUID() uuid.UUID { return id.u }

// IsZero reports whether id is the zero value.
func (id ID[K]) IsZero() bool { return id.u == uuid.Nil }

// MarshalText implements encoding.TextMarshaler (used by JSON).
func (id ID[K]) MarshalText() ([]byte, error) {
	if id.IsZero() {
		return []byte{}, nil
	}
	return []byte(id.String()), nil
}

// UnmarshalText implements encoding.TextUnmarshaler (used by JSON).
func (id *ID[K]) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*id = ID[K]{}
		return nil
	}
	parsed, err := Parse[K](string(b))
	if err != nil {
		return err
	}
	*id = parsed
	return nil
}

// Value implements driver.Valuer: IDs are stored as uuid.
func (id ID[K]) Value() (driver.Value, error) {
	if id.IsZero() {
		return nil, nil //nolint:nilnil // a nil driver.Value is SQL NULL, the documented encoding of "no ID"
	}
	return id.u.String(), nil
}

// Scan implements sql.Scanner for uuid columns.
func (id *ID[K]) Scan(src any) error {
	switch v := src.(type) {
	case nil:
		*id = ID[K]{}
		return nil
	case string:
		u, err := uuid.Parse(v)
		if err != nil {
			return errs.Wrap(errs.CodeInternal, err, "scan identifier")
		}
		*id = ID[K]{u: u}
		return nil
	case []byte:
		if len(v) == 16 {
			u, err := uuid.FromBytes(v)
			if err != nil {
				return errs.Wrap(errs.CodeInternal, err, "scan identifier")
			}
			*id = ID[K]{u: u}
			return nil
		}
		return id.Scan(string(v))
	case [16]byte:
		*id = ID[K]{u: uuid.UUID(v)}
		return nil
	default:
		return errs.New(errs.CodeInternal, fmt.Sprintf("scan identifier: unsupported type %T", src))
	}
}

const alphabet = "0123456789abcdefghjkmnpqrstvwxyz"

// encode writes the 128-bit value as 26 base32 characters. The value is
// treated as a 130-bit number whose two leading bits are zero.
func encode(u uuid.UUID) string {
	var b [26]byte
	for i := range 26 {
		var v byte
		for j := range 5 {
			bit := i*5 + j - 2 // index into the 128 data bits
			if bit >= 0 && u[bit/8]&(0x80>>(bit%8)) != 0 {
				v |= 1 << (4 - j)
			}
		}
		b[i] = alphabet[v]
	}
	return string(b[:])
}

func decode(s string) (uuid.UUID, error) {
	var u uuid.UUID
	if len(s) != 26 {
		return u, errs.New(errs.CodeInvalidRequest, "identifier suffix must be 26 characters")
	}
	for i := range 26 {
		v := strings.IndexByte(alphabet, s[i])
		if v < 0 {
			return uuid.Nil, errs.New(errs.CodeInvalidRequest, "identifier contains an invalid character")
		}
		if i == 0 && v > 7 {
			return uuid.Nil, errs.New(errs.CodeInvalidRequest, "identifier is out of range")
		}
		for j := range 5 {
			bit := i*5 + j - 2
			if bit >= 0 && v&(1<<(4-j)) != 0 {
				u[bit/8] |= 0x80 >> (bit % 8)
			}
		}
	}
	return u, nil
}
