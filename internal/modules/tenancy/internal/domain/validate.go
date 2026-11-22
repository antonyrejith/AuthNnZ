package domain

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/antonyrejith/portun/internal/platform/errs"
)

// Limits of the inputs.
const (
	MaxNameLength      = 200
	MaxCodeLength      = 64 // region and plan
	MaxEnvironmentName = 63
	MaxEnvironments    = 10
	MaxHostLength      = 253
	MaxReasonLength    = 1000
	maxLabelLength     = 63
	minBaseDomain      = 3
)

var (
	slugPattern  = regexp.MustCompile(`^[a-z][a-z0-9-]{1,38}[a-z0-9]$`)
	codePattern  = regexp.MustCompile(`^[a-z0-9-]+$`)
	labelPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)
)

// EnvSpec is a requested environment.
type EnvSpec struct {
	Name string
	Kind string
}

// TenantSpec is the input of a new tenant.
type TenantSpec struct {
	Slug, Name, Region, Plan string
	Environments             []EnvSpec
}

// DefaultEnvironments returns the environments of a tenant created without
// an explicit list: development, staging and production.
func DefaultEnvironments() []EnvSpec {
	return []EnvSpec{
		{Name: KindDevelopment, Kind: KindDevelopment},
		{Name: KindStaging, Kind: KindStaging},
		{Name: KindProduction, Kind: KindProduction},
	}
}

func invalid(fields []errs.FieldError) error {
	if len(fields) == 0 {
		return nil
	}
	return errs.New(errs.CodeValidationFailed, "the request contains invalid fields").WithFields(fields...)
}

func field(pointer, code, detail string) errs.FieldError {
	return errs.FieldError{Pointer: pointer, Code: code, Detail: detail}
}

func checkName(pointer, v string) []errs.FieldError {
	switch {
	case strings.TrimSpace(v) == "":
		return []errs.FieldError{field(pointer, "required", "name must not be empty")}
	case utf8.RuneCountInString(v) > MaxNameLength:
		return []errs.FieldError{field(pointer, "too_long", "name must have at most "+strconv.Itoa(MaxNameLength)+" characters")}
	}
	for _, r := range v {
		if unicode.IsControl(r) {
			return []errs.FieldError{field(pointer, "invalid", "name must not contain control characters")}
		}
	}
	return nil
}

func checkCode(pointer, label, v string) []errs.FieldError {
	switch {
	case v == "":
		return []errs.FieldError{field(pointer, "required", label+" is required")}
	case len(v) > MaxCodeLength:
		return []errs.FieldError{field(pointer, "too_long", label+" must have at most "+strconv.Itoa(MaxCodeLength)+" characters")}
	case !codePattern.MatchString(v):
		return []errs.FieldError{field(pointer, "invalid", label+" may contain only a-z, 0-9 and '-'")}
	}
	return nil
}

// ValidatePartner checks a partner's name.
func ValidatePartner(name string) error {
	return invalid(checkName("/name", name))
}

// ValidateTenant checks the input of a new tenant and returns the
// environments to create (the defaults when none were given).
func ValidateTenant(s TenantSpec) ([]EnvSpec, error) {
	var f []errs.FieldError
	if !slugPattern.MatchString(s.Slug) {
		f = append(f, field("/slug", "invalid", "slug must be 3 to 40 characters: a-z, 0-9 and '-', starting with a letter and not ending with '-'"))
	}
	f = append(f, checkName("/name", s.Name)...)
	f = append(f, checkCode("/region", "region", s.Region)...)
	f = append(f, checkCode("/plan", "plan", s.Plan)...)
	envs := s.Environments
	if len(envs) == 0 {
		envs = DefaultEnvironments()
	}
	if len(envs) > MaxEnvironments {
		f = append(f, field("/environments", "too_many", "at most "+strconv.Itoa(MaxEnvironments)+" environments"))
	}
	seen := map[string]bool{}
	for i, e := range envs {
		f = append(f, checkEnvironment("/environments/"+strconv.Itoa(i), s.Slug, e, seen)...)
	}
	if err := invalid(f); err != nil {
		return nil, err
	}
	return envs, nil
}

func checkEnvironment(p, slug string, e EnvSpec, seen map[string]bool) []errs.FieldError {
	var f []errs.FieldError
	switch {
	case len(e.Name) > MaxEnvironmentName || !labelPattern.MatchString(e.Name):
		f = append(f, field(p+"/name", "invalid", "environment name must be 1 to 63 characters: a-z, 0-9 and '-', not starting or ending with '-'"))
	case seen[e.Name]:
		f = append(f, field(p+"/name", "duplicate", "environment names must be unique"))
	case len(slug)+1+len(e.Name) > maxLabelLength:
		f = append(f, field(p+"/name", "too_long", "slug and environment name together must fit a host name label of 63 characters"))
	}
	seen[e.Name] = true
	switch e.Kind {
	case KindDevelopment, KindStaging, KindProduction:
	default:
		f = append(f, field(p+"/kind", "invalid", "kind must be development, staging or production"))
	}
	return f
}

// ValidateUpdate checks the fields of a tenant update.
func ValidateUpdate(name, plan *string) error {
	var f []errs.FieldError
	if name == nil && plan == nil {
		f = append(f, field("", "required", "name or plan must be given"))
	}
	if name != nil {
		f = append(f, checkName("/name", *name)...)
	}
	if plan != nil {
		f = append(f, checkCode("/plan", "plan", *plan)...)
	}
	return invalid(f)
}

// ValidateReason checks the mandatory reason of a lifecycle change.
func ValidateReason(reason string) error {
	switch {
	case strings.TrimSpace(reason) == "":
		return invalid([]errs.FieldError{field("/reason", "required", "a reason is required")})
	case utf8.RuneCountInString(reason) > MaxReasonLength:
		return invalid([]errs.FieldError{field("/reason", "too_long", "reason must have at most "+strconv.Itoa(MaxReasonLength)+" characters")})
	}
	return nil
}

// CheckPartnerDepth checks that a partner whose parent has the given depth
// (0 for no parent) stays within MaxPartnerDepth.
func CheckPartnerDepth(parentDepth int) error {
	if parentDepth+1 > MaxPartnerDepth {
		return invalid([]errs.FieldError{field("/parent_id", "too_deep", "partners can be nested at most "+strconv.Itoa(MaxPartnerDepth)+" levels")})
	}
	return nil
}

// ValidateBaseDomain checks the domain under which default hosts are
// created.
func ValidateBaseDomain(base string) error {
	if len(base) < minBaseDomain || len(base) > MaxHostLength-maxLabelLength-1 || !validLabels(base) {
		return errs.New(errs.CodeInternal, "tenancy: the base domain must be a lowercase DNS name")
	}
	return nil
}

// DefaultHost returns the default host of an environment:
// <slug>-<environment>.<base domain>.
func DefaultHost(slug, environment, baseDomain string) string {
	return strings.ToLower(slug + "-" + environment + "." + baseDomain)
}

// NormalizeHost lowercases host, strips a port and one trailing dot, and
// checks that the result is a DNS name. It fails with errs.CodeNotFound for
// a name that cannot be a registered host, so lookups of garbage look like
// lookups of unknown hosts.
func NormalizeHost(host string) (string, error) {
	notFound := errs.New(errs.CodeNotFound, "host not found")
	h := strings.ToLower(strings.TrimSpace(host))
	if i := strings.LastIndexByte(h, ':'); i >= 0 {
		if _, err := strconv.ParseUint(h[i+1:], 10, 16); err != nil {
			return "", notFound
		}
		h = h[:i]
	}
	h = strings.TrimSuffix(h, ".")
	if len(h) > MaxHostLength || !validLabels(h) {
		return "", notFound
	}
	return h, nil
}

// ValidateHost normalizes a host to register and rejects an invalid one, or
// one with a port, with errs.CodeValidationFailed.
func ValidateHost(host string) (string, error) {
	h, err := NormalizeHost(host)
	if err != nil || strings.Contains(host, ":") {
		return "", invalid([]errs.FieldError{field("/host", "invalid", "host must be a DNS name without port")})
	}
	return h, nil
}

func validLabels(h string) bool {
	for l := range strings.SplitSeq(h, ".") {
		if len(l) > maxLabelLength || !labelPattern.MatchString(l) {
			return false
		}
	}
	return true
}
