package bus

import (
	"strings"

	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/ids"
)

// Stream names (ADR-0007).
const (
	StreamAudit  = "AUDIT"  // audit records, subjects audit.<tenant> and audit.platform
	StreamEvents = "EVENTS" // domain events, subjects evt.<module>.<type>
	StreamConfig = "CONFIG" // configuration change notifications, subjects config.<tenant>.<env>
)

// Subject namespaces of the streams, usable as consumer filters.
const (
	AuditSubjects  = "audit.>"
	EventSubjects  = "evt.>"
	ConfigSubjects = "config.>"
)

// AuditPlatformSubject is the reserved subject of the platform audit chain:
// events that belong to no tenant (platform operators, partner management).
// Its second token, "platform", is not a valid tenant TypeID (those always
// contain "_"), so it can never collide with a tenant's subject.
const AuditPlatformSubject = "audit.platform"

const (
	auditPrefix    = "audit"
	eventPrefix    = "evt"
	configPrefix   = "config"
	platformToken  = "platform"
	maxTokenLength = 128
)

// AuditSubject returns the subject of a tenant's audit chain,
// "audit.<tenant TypeID>". The zero tenant is rejected: platform events use
// AuditPlatformSubject.
func AuditSubject(tenant ids.TenantID) (string, error) {
	if tenant.IsZero() {
		return "", errs.New(errs.CodeInvalidRequest, "audit subject: tenant is required (use AuditPlatformSubject for platform events)")
	}
	return auditPrefix + "." + tenant.String(), nil
}

// EventSubject returns "evt.<module>.<eventType>". Both parts must be valid
// subject tokens (see ValidateToken).
func EventSubject(module, eventType string) (string, error) {
	if err := ValidateToken(module); err != nil {
		return "", errs.Wrap(errs.CodeInvalidRequest, err, "event subject: invalid module")
	}
	if err := ValidateToken(eventType); err != nil {
		return "", errs.Wrap(errs.CodeInvalidRequest, err, "event subject: invalid event type")
	}
	return eventPrefix + "." + module + "." + eventType, nil
}

// ConfigSubject returns "config.<tenant TypeID>.<environment TypeID>". Zero
// identifiers are rejected.
func ConfigSubject(tenant ids.TenantID, env ids.EnvironmentID) (string, error) {
	if tenant.IsZero() || env.IsZero() {
		return "", errs.New(errs.CodeInvalidRequest, "config subject: tenant and environment are required")
	}
	return configPrefix + "." + tenant.String() + "." + env.String(), nil
}

// ValidateToken checks one subject token: it must be non-empty, at most 128
// bytes, and consist of printable ASCII other than space, ".", "*" and ">".
func ValidateToken(token string) error {
	if token == "" {
		return errs.New(errs.CodeInvalidRequest, "subject token is empty")
	}
	if len(token) > maxTokenLength {
		return errs.New(errs.CodeInvalidRequest, "subject token is too long")
	}
	for i := range len(token) {
		c := token[i]
		if c <= ' ' || c > '~' || c == '.' || c == '*' || c == '>' {
			return errs.New(errs.CodeInvalidRequest, "subject token contains a reserved, whitespace or non-ASCII character")
		}
	}
	return nil
}

// streamForSubject validates a publish subject against the subject
// namespaces and returns the stream that stores it.
func streamForSubject(subject string) (string, error) {
	tokens := strings.Split(subject, ".")
	for _, t := range tokens {
		if err := ValidateToken(t); err != nil {
			return "", errs.Wrap(errs.CodeInvalidRequest, err, "invalid subject")
		}
	}
	switch tokens[0] {
	case auditPrefix:
		if len(tokens) == 2 && (tokens[1] == platformToken || isTenant(tokens[1])) {
			return StreamAudit, nil
		}
		return "", errs.New(errs.CodeInvalidRequest, "audit subjects are audit.<tenant> or audit.platform")
	case eventPrefix:
		if len(tokens) == 3 {
			return StreamEvents, nil
		}
		return "", errs.New(errs.CodeInvalidRequest, "event subjects are evt.<module>.<type>")
	case configPrefix:
		if len(tokens) == 3 && isTenant(tokens[1]) && isEnvironment(tokens[2]) {
			return StreamConfig, nil
		}
		return "", errs.New(errs.CodeInvalidRequest, "config subjects are config.<tenant>.<environment>")
	default:
		return "", errs.New(errs.CodeInvalidRequest, "subject is outside the bus namespaces (audit, evt, config)")
	}
}

func isTenant(s string) bool {
	id, err := ids.Parse[ids.TenantKind](s)
	return err == nil && !id.IsZero()
}

func isEnvironment(s string) bool {
	id, err := ids.Parse[ids.EnvironmentKind](s)
	return err == nil && !id.IsZero()
}

// streamPrefix returns the first subject token of a stream.
func streamPrefix(stream string) (string, bool) {
	switch stream {
	case StreamAudit:
		return auditPrefix, true
	case StreamEvents:
		return eventPrefix, true
	case StreamConfig:
		return configPrefix, true
	default:
		return "", false
	}
}

// validateFilter checks a consumer filter subject: it must start with the
// stream's prefix token; "*" may replace any later token and ">" may be the
// last token.
func validateFilter(stream, filter string) error {
	prefix, _ := streamPrefix(stream)
	tokens := strings.Split(filter, ".")
	if tokens[0] != prefix || len(tokens) < 2 {
		return errs.New(errs.CodeInvalidRequest, "filter subject must be within the stream's namespace "+prefix+".>")
	}
	for i, t := range tokens[1:] {
		switch {
		case t == "*":
		case t == ">" && i == len(tokens)-2:
		default:
			if err := ValidateToken(t); err != nil {
				return errs.Wrap(errs.CodeInvalidRequest, err, "invalid filter subject")
			}
		}
	}
	return nil
}

// validateName checks a durable consumer name: a valid token that is also
// safe as a file name on the server.
func validateName(name string) error {
	if err := ValidateToken(name); err != nil {
		return err
	}
	if strings.ContainsAny(name, `/\`) {
		return errs.New(errs.CodeInvalidRequest, "name must not contain path separators")
	}
	return nil
}
