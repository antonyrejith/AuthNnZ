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
//	}
//
// Supported field types: string, bool, all integer kinds, float32/64,
// time.Duration, []string (comma-separated), and any type that implements
// encoding.TextUnmarshaler (such as secret.String).
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

func loadStruct(prefix string, v reflect.Value, lookup LookupFunc, problems *[]error) {
	t := v.Type()
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		fv := v.Field(i)
		if nested, ok := f.Tag.Lookup("envPrefix"); ok {
			if fv.Kind() != reflect.Struct {
				*problems = append(*problems, fmt.Errorf("field %s: envPrefix requires a struct", f.Name))
				continue
			}
			loadStruct(prefix+nested, fv, lookup, problems)
			continue
		}
		if err := loadField(prefix, f, fv, lookup); err != nil {
			*problems = append(*problems, err)
		}
	}
}

// loadField sets one field from its variable or default. Fields without an
// env tag are left alone.
func loadField(prefix string, f reflect.StructField, fv reflect.Value, lookup LookupFunc) error {
	name, ok := f.Tag.Lookup("env")
	if !ok {
		return nil
	}
	key := prefix + name
	raw, set := lookup(key)
	if !set || raw == "" {
		raw, set = f.Tag.Lookup("default")
	}
	if !set {
		if f.Tag.Get("required") == "true" {
			return fmt.Errorf("%s is required", key)
		}
		return nil
	}
	if err := setValue(fv, raw); err != nil {
		return fmt.Errorf("%s: %w", key, err)
	}
	return nil
}

func setValue(v reflect.Value, raw string) error {
	if v.CanAddr() && v.Addr().Type().Implements(textUnmarshaler) {
		u, _ := v.Addr().Interface().(encoding.TextUnmarshaler)
		if err := u.UnmarshalText([]byte(raw)); err != nil {
			return fmt.Errorf("parse: %w", err)
		}
		return nil
	}
	if v.Type() == durationType {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return fmt.Errorf("parse duration: %w", err)
		}
		v.SetInt(int64(d))
		return nil
	}
	return setBasic(v, raw)
}

func setBasic(v reflect.Value, raw string) error {
	switch v.Kind() { //nolint:exhaustive // unsupported kinds fall through to the error below
	case reflect.String:
		v.SetString(raw)
	case reflect.Bool:
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return fmt.Errorf("parse bool: %w", err)
		}
		v.SetBool(b)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, err := strconv.ParseInt(raw, 10, v.Type().Bits())
		if err != nil {
			return fmt.Errorf("parse integer: %w", err)
		}
		v.SetInt(n)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n, err := strconv.ParseUint(raw, 10, v.Type().Bits())
		if err != nil {
			return fmt.Errorf("parse unsigned integer: %w", err)
		}
		v.SetUint(n)
	case reflect.Float32, reflect.Float64:
		f, err := strconv.ParseFloat(raw, v.Type().Bits())
		if err != nil {
			return fmt.Errorf("parse float: %w", err)
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
