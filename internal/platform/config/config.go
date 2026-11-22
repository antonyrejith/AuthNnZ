// Package config loads process configuration from environment variables into
// a struct (twelve-factor). It is for bootstrap settings only (addresses,
// credentials, pool sizes); tenant behavior lives in the config module.
//
// Fields are bound with struct tags:
//
//	type Config struct {
//		HTTPAddr string        `env:"HTTP_ADDR" default:":8080"`
//		Timeout  time.Duration `env:"TIMEOUT" default:"5s"`
//		Password secret.String `env:"PASSWORD" required:"true"`
//		DB       DBConfig      `envPrefix:"DB_"`
//		Common                 // embedded: its fields use the same prefix
//		Cache    *Cache        `env:"-"` // explicitly not configuration
//	}
//
// Supported field types: string, bool, all integer kinds, float32/64,
// time.Duration, []string (comma-separated), and any type that implements
// encoding.TextUnmarshaler (such as secret.String).
//
// The loader fails closed. Tags and field types are validated for every
// field before any value is read, so a mistake in the struct is reported even
// when no variable is set: an unsupported type, a default that does not
// parse, required combined with default, env and envPrefix together, an env
// tag on an unexported field, or a struct field with neither env nor
// envPrefix (which would otherwise be silently ignored). Embedded structs,
// exported or not, are walked with the same prefix. Error messages name the
// variable but never include its value, which can be a secret.
package config

import (
	"encoding"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/antonyrejith/portun/internal/platform/errs"
)

// LookupFunc returns the value of a variable and whether it is set.
type LookupFunc func(key string) (string, bool)

// FromEnv loads cfg from the process environment.
func FromEnv(prefix string, cfg any) error {
	return Load(prefix, cfg, os.LookupEnv)
}

// Load fills cfg, which must be a pointer to a struct, using lookup. Every
// problem is reported, not just the first, so a misconfigured deployment can
// be fixed in one pass.
func Load(prefix string, cfg any, lookup LookupFunc) error {
	v := reflect.ValueOf(cfg)
	if v.Kind() != reflect.Pointer || v.IsNil() || v.Elem().Kind() != reflect.Struct {
		return errs.New(errs.CodeInternal, "config: target must be a non-nil pointer to a struct")
	}
	var problems []error
	loadStruct(prefix, v.Elem(), lookup, &problems)
	if len(problems) > 0 {
		return errs.Wrap(errs.CodeInvalidRequest, errors.Join(problems...), "invalid configuration")
	}
	return nil
}

var (
	textUnmarshaler = reflect.TypeFor[encoding.TextUnmarshaler]() //nolint:gochecknoglobals // immutable type handle
	durationType    = reflect.TypeFor[time.Duration]()            //nolint:gochecknoglobals // immutable type handle
)

// loadStruct walks the fields of v and appends every problem.
func loadStruct(prefix string, v reflect.Value, lookup LookupFunc, problems *[]error) {
	t := v.Type()
	for i := range t.NumField() {
		if err := loadOne(prefix, t.Field(i), v.Field(i), lookup, problems); err != nil {
			*problems = append(*problems, err)
		}
	}
}

func loadOne(prefix string, f reflect.StructField, fv reflect.Value, lookup LookupFunc, problems *[]error) error {
	env, hasEnv := f.Tag.Lookup("env")
	nested, hasPrefix := f.Tag.Lookup("envPrefix")
	switch {
	case env == "-":
		return nil // explicitly not configuration
	case hasEnv && hasPrefix:
		return fmt.Errorf("field %s: env and envPrefix are mutually exclusive", f.Name)
	case hasPrefix:
		if !isPlainStruct(f.Type) {
			return fmt.Errorf("field %s: envPrefix requires a struct", f.Name)
		}
		if !f.IsExported() && !f.Anonymous {
			return fmt.Errorf("field %s: envPrefix on an unexported field", f.Name)
		}
		loadStruct(prefix+nested, fv, lookup, problems)
		return nil
	case hasEnv:
		if !f.IsExported() {
			return fmt.Errorf("field %s: env tag on an unexported field", f.Name)
		}
		return loadField(prefix, env, f, fv, lookup)
	case f.Anonymous && f.Type.Kind() == reflect.Pointer && isPlainStruct(f.Type.Elem()):
		return fmt.Errorf("field %s: embedded struct pointers are not supported; embed the struct", f.Name)
	case f.Anonymous && isPlainStruct(f.Type):
		// Embedded structs, exported or not, contribute their fields with
		// the same prefix (the exported fields of an unexported embedded
		// struct are settable).
		loadStruct(prefix, fv, lookup, problems)
		return nil
	case f.IsExported() && isPlainStruct(f.Type):
		return fmt.Errorf("field %s: a struct field needs an envPrefix tag (or env:%q to ignore it)", f.Name, "-")
	default:
		return nil // untagged non-struct fields are not configuration
	}
}

// isPlainStruct reports struct types that are not themselves values (a
// TextUnmarshaler such as time.Time is a value).
func isPlainStruct(t reflect.Type) bool {
	return t.Kind() == reflect.Struct && !reflect.PointerTo(t).Implements(textUnmarshaler)
}

// loadField validates the field's tags and type, then sets it from its
// variable or default.
func loadField(prefix, name string, f reflect.StructField, fv reflect.Value, lookup LookupFunc) error {
	if name == "" {
		return fmt.Errorf("field %s: empty env tag", f.Name)
	}
	key := prefix + name
	if err := supported(f.Type); err != nil {
		return fmt.Errorf("%s (field %s): %w", key, f.Name, err)
	}
	required, err := requiredTag(f)
	if err != nil {
		return fmt.Errorf("%s (field %s): %w", key, f.Name, err)
	}
	def, hasDefault := f.Tag.Lookup("default")
	if required && hasDefault {
		return fmt.Errorf("%s (field %s): required and default are mutually exclusive", key, f.Name)
	}
	if hasDefault {
		// Parse the default into a scratch value, so a bad default fails
		// even when the variable is set.
		if err := setValue(reflect.New(f.Type).Elem(), def); err != nil {
			return fmt.Errorf("%s (field %s): invalid default: %w", key, f.Name, err)
		}
	}
	raw, set := lookup(key)
	if !set || raw == "" {
		raw, set = def, hasDefault
	}
	if !set {
		if required {
			return fmt.Errorf("%s is required", key)
		}
		return nil
	}
	if err := setValue(fv, raw); err != nil {
		return fmt.Errorf("%s: %w", key, err)
	}
	return nil
}

func requiredTag(f reflect.StructField) (bool, error) {
	r, ok := f.Tag.Lookup("required")
	if !ok {
		return false, nil
	}
	b, err := strconv.ParseBool(r)
	if err != nil {
		return false, errors.New(`the required tag must be "true" or "false"`)
	}
	return b, nil
}

// supported reports an error for field types the loader cannot parse.
func supported(t reflect.Type) error {
	if reflect.PointerTo(t).Implements(textUnmarshaler) || t == durationType {
		return nil
	}
	switch t.Kind() { //nolint:exhaustive // every other kind is unsupported
	case reflect.String, reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return nil
	case reflect.Slice:
		if t.Elem().Kind() == reflect.String {
			return nil
		}
	}
	return fmt.Errorf("unsupported field type %s", t)
}

// setValue parses raw into v. Errors never include raw: a configuration
// value can be a secret.
func setValue(v reflect.Value, raw string) error {
	if v.CanAddr() && v.Addr().Type().Implements(textUnmarshaler) {
		u, ok := v.Addr().Interface().(encoding.TextUnmarshaler)
		if !ok {
			return fmt.Errorf("unsupported field type %s", v.Type())
		}
		if err := u.UnmarshalText([]byte(raw)); err != nil {
			// The unmarshaler's message may quote the value; report the type.
			return fmt.Errorf("invalid value for type %s", v.Type())
		}
		return nil
	}
	if v.Type() == durationType {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return errors.New("invalid duration")
		}
		v.SetInt(int64(d))
		return nil
	}
	return setBasic(v, raw)
}

// numErr describes a strconv error by its reason only; NumError.Error
// quotes the input.
func numErr(kind string, err error) error {
	var ne *strconv.NumError
	if errors.As(err, &ne) {
		return fmt.Errorf("invalid %s: %w", kind, ne.Err)
	}
	return fmt.Errorf("invalid %s", kind)
}

func setBasic(v reflect.Value, raw string) error {
	switch v.Kind() { //nolint:exhaustive // unsupported kinds fall through to the error below
	case reflect.String:
		v.SetString(raw)
	case reflect.Bool:
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return numErr("boolean", err)
		}
		v.SetBool(b)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, err := strconv.ParseInt(raw, 10, v.Type().Bits())
		if err != nil {
			return numErr("integer", err)
		}
		v.SetInt(n)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n, err := strconv.ParseUint(raw, 10, v.Type().Bits())
		if err != nil {
			return numErr("unsigned integer", err)
		}
		v.SetUint(n)
	case reflect.Float32, reflect.Float64:
		f, err := strconv.ParseFloat(raw, v.Type().Bits())
		if err != nil {
			return numErr("number", err)
		}
		v.SetFloat(f)
	case reflect.Slice:
		if v.Type().Elem().Kind() != reflect.String {
			return fmt.Errorf("unsupported slice type %s", v.Type())
		}
		v.Set(reflect.ValueOf(splitList(raw)).Convert(v.Type()))
	default:
		return fmt.Errorf("unsupported field type %s", v.Type())
	}
	return nil
}

func splitList(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
