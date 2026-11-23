package apimw

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/ogen-go/ogen"

	"github.com/antonyrejith/portun/internal/platform/errs"
)

// Vendor extensions every operation declares (ADR-0009).
const (
	ExtPermission  = "x-required-permission"
	ExtAuditEvent  = "x-audit-event"
	ExtAuditReason = "x-audit-reason"
)

// AuditEventNone is the x-audit-event value of operations that write nothing.
const AuditEventNone = "none"

// auditReasonRequired is the x-audit-reason value that makes Change-Reason
// mandatory.
const auditReasonRequired = "required"

var (
	permissionPattern = regexp.MustCompile(`^[a-z]+(\.[a-z_]+)+$`)
	auditEventPattern = regexp.MustCompile(`^[a-z_]+(\.[a-z_]+)+$`)
)

// OperationInfo is what the specification declares about one operation.
type OperationInfo struct {
	// OperationID is the operationId.
	OperationID string
	// Method is the HTTP method, upper case.
	Method string
	// PathTemplate is the path with its parameters, for example
	// "/platform/v1/tenants/{tenant_id}".
	PathTemplate string
	// Permission is the permission the caller needs (x-required-permission).
	Permission string
	// AuditEvent is the audit event type the operation writes
	// (x-audit-event), or AuditEventNone.
	AuditEvent string
	// ReasonRequired is true for x-audit-reason: required.
	ReasonRequired bool
}

// Operations is the table of a specification's operations by operationId.
type Operations struct {
	byID map[string]OperationInfo
}

// Info returns the operation with the given operationId.
func (o Operations) Info(operationID string) (OperationInfo, bool) {
	info, ok := o.byID[operationID]
	return info, ok
}

// All returns every operation, ordered by operationId.
func (o Operations) All() []OperationInfo {
	out := make([]OperationInfo, 0, len(o.byID))
	for _, info := range o.byID {
		out = append(out, info)
	}
	slices.SortFunc(out, func(a, b OperationInfo) int { return strings.Compare(a.OperationID, b.OperationID) })
	return out
}

// ParseOperations reads the operation table from an OpenAPI document. It
// fails when an operation lacks an operationId, x-required-permission or
// x-audit-event, when a value is malformed, or when operationIds repeat.
func ParseOperations(spec []byte) (Operations, error) {
	doc, err := ogen.Parse(spec)
	if err != nil {
		return Operations{}, errs.Wrap(errs.CodeInternal, err, "api: parse the OpenAPI document")
	}
	table := Operations{byID: map[string]OperationInfo{}}
	for path, item := range doc.Paths {
		if item == nil {
			continue
		}
		methods := map[string]*ogen.Operation{
			"GET": item.Get, "PUT": item.Put, "POST": item.Post, "DELETE": item.Delete, "OPTIONS": item.Options,
			"HEAD": item.Head, "PATCH": item.Patch, "TRACE": item.Trace, "QUERY": item.Query,
		}
		for name, op := range item.AdditionalOperations {
			methods[strings.ToUpper(name)] = op
		}
		for method, op := range methods {
			if op == nil {
				continue
			}
			info, err := operationInfo(method, path, op)
			if err != nil {
				return Operations{}, err
			}
			if _, dup := table.byID[info.OperationID]; dup {
				return Operations{}, errs.New(errs.CodeInternal, fmt.Sprintf("api: operationId %q is used twice", info.OperationID))
			}
			table.byID[info.OperationID] = info
		}
	}
	if len(table.byID) == 0 {
		return Operations{}, errs.New(errs.CodeInternal, "api: the OpenAPI document has no operations")
	}
	return table, nil
}

func operationInfo(method, path string, op *ogen.Operation) (OperationInfo, error) {
	where := method + " " + path
	if op.OperationID == "" {
		return OperationInfo{}, errs.New(errs.CodeInternal, "api: "+where+" has no operationId")
	}
	info := OperationInfo{OperationID: op.OperationID, Method: method, PathTemplate: path}
	var err error
	if info.Permission, err = stringExtension(op, ExtPermission, where, true); err != nil {
		return OperationInfo{}, err
	}
	if !permissionPattern.MatchString(info.Permission) {
		return OperationInfo{}, errs.New(errs.CodeInternal, fmt.Sprintf("api: %s: %s %q is not a permission name", where, ExtPermission, info.Permission))
	}
	if info.AuditEvent, err = stringExtension(op, ExtAuditEvent, where, true); err != nil {
		return OperationInfo{}, err
	}
	if info.AuditEvent != AuditEventNone && !auditEventPattern.MatchString(info.AuditEvent) {
		return OperationInfo{}, errs.New(errs.CodeInternal, fmt.Sprintf("api: %s: %s %q is not an event type or %q", where, ExtAuditEvent, info.AuditEvent, AuditEventNone))
	}
	reason, err := stringExtension(op, ExtAuditReason, where, false)
	if err != nil {
		return OperationInfo{}, err
	}
	switch reason {
	case "":
	case auditReasonRequired:
		info.ReasonRequired = true
	default:
		return OperationInfo{}, errs.New(errs.CodeInternal, fmt.Sprintf("api: %s: %s must be %q", where, ExtAuditReason, auditReasonRequired))
	}
	return info, nil
}

func stringExtension(op *ogen.Operation, name, where string, required bool) (string, error) {
	node, ok := op.Common.Extensions[name]
	if !ok {
		if required {
			return "", errs.New(errs.CodeInternal, fmt.Sprintf("api: %s has no %s", where, name))
		}
		return "", nil
	}
	var v string
	if err := node.Decode(&v); err != nil || strings.TrimSpace(v) == "" {
		return "", errs.New(errs.CodeInternal, fmt.Sprintf("api: %s: %s must be a non-empty string", where, name))
	}
	return v, nil
}
