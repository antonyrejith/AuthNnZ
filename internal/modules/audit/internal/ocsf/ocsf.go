// Package ocsf maps audit records to the Open Cybersecurity Schema
// Framework (OCSF 1.3) for SIEM export (GOV-01, plan 13 §3.2): each record
// category maps to an OCSF class, the event type's verb to the class
// activity, and the envelope's who/what/where/how to OCSF attributes. The
// original record travels along unmapped under "unmapped.portun".
package ocsf

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/antonyrejith/portun/internal/platform/errs"
)

// Version is the OCSF schema version produced.
const Version = "1.3.0"

// OCSF categories and classes used.
const (
	CategoryIAM         = 3 // Identity & Access Management
	CategoryApplication = 6 // Application Activity

	ClassAuthentication     = 3002
	ClassEntityManagement   = 3004
	ClassAPIActivity        = 6003
	ClassWebResourceAccess  = 6004
	ClassDatastoreActivity  = 6005
	ActivityUnknown         = 0
	ActivityOther           = 99
	StatusSuccess           = 1
	StatusFailure           = 2
	SeverityInformational   = 1
	activityLogon           = 1
	activityLogoff          = 2
	activityAuthTicket      = 3
	activityCreate          = 1
	activityRead            = 2
	activityUpdate          = 3
	activityDelete          = 4
	activityEnable          = 8
	activityDisable         = 9
	activityAccessGrant     = 1
	activityAccessDeny      = 2
	activityAccessRevoke    = 3
	activityDatastoreRead   = 1
	activityDatastoreQuery  = 4
	activityDatastoreDelete = 7
)

// Class is the OCSF classification of a record.
type Class struct {
	CategoryUID int `json:"category_uid"`
	ClassUID    int `json:"class_uid"`
	ActivityID  int `json:"activity_id"`
	TypeUID     int `json:"type_uid"` // class_uid * 100 + activity_id
}

// verb returns the last dot-separated token of an event type.
func verb(eventType string) string {
	if i := strings.LastIndexByte(eventType, '.'); i >= 0 {
		return eventType[i+1:]
	}
	return eventType
}

// Map classifies a record by category, event type and outcome. Unknown
// categories map to API Activity with activity Other.
func Map(category, eventType, outcome string) Class {
	v := verb(eventType)
	var cat, class, act int
	switch category {
	case "configuration", "data", "key_management", "platform":
		cat, class, act = CategoryIAM, ClassEntityManagement, entityActivity(v)
	case "authentication":
		cat, class, act = CategoryIAM, ClassAuthentication, authActivity(eventType, v)
	case "authorization":
		cat, class = CategoryApplication, ClassWebResourceAccess
		act = authzActivity(v, outcome)
	case "sensitive_read":
		cat, class, act = CategoryApplication, ClassAPIActivity, activityRead
	case "audit_access":
		cat, class, act = CategoryApplication, ClassDatastoreActivity, auditAccessActivity(v)
	default:
		cat, class, act = CategoryApplication, ClassAPIActivity, ActivityOther
	}
	return Class{CategoryUID: cat, ClassUID: class, ActivityID: act, TypeUID: class*100 + act}
}

func entityActivity(v string) int {
	switch v {
	case "created", "imported", "provisioned", "enrolled", "issued":
		return activityCreate
	case "read", "viewed", "revealed", "exported":
		return activityRead
	case "updated", "changed", "rotated", "published", "promoted", "rolled_back", "restored", "synced":
		return activityUpdate
	case "deleted", "removed", "destroyed", "deprovisioned":
		return activityDelete
	case "enabled", "activated", "unlocked":
		return activityEnable
	case "disabled", "deactivated", "suspended", "locked":
		return activityDisable
	default:
		return ActivityOther
	}
}

func authActivity(eventType, v string) int {
	switch {
	case strings.Contains(eventType, "logout") || v == "ended" || v == "logged_out" || v == "signed_out":
		return activityLogoff
	case strings.HasPrefix(eventType, "token.") || strings.Contains(eventType, ".token"):
		return activityAuthTicket
	case strings.HasPrefix(eventType, "login") || strings.HasPrefix(eventType, "session.") || v == "logged_in" || v == "signed_in":
		return activityLogon
	default:
		return ActivityOther
	}
}

func authzActivity(v, outcome string) int {
	switch {
	case v == "revoked":
		return activityAccessRevoke
	case v == "denied" || outcome == "failure":
		return activityAccessDeny
	default:
		return activityAccessGrant
	}
}

func auditAccessActivity(v string) int {
	switch v {
	case "searched", "search":
		return activityDatastoreQuery
	case "retention_applied":
		return activityDatastoreDelete
	default:
		return activityDatastoreRead
	}
}

// record is the part of an audit record the mapping reads.
type record struct {
	ID        string    `json:"id"`
	Sequence  uint64    `json:"sequence"`
	Time      time.Time `json:"time"`
	TenantID  string    `json:"tenant_id"`
	Category  string    `json:"category"`
	Type      string    `json:"type"`
	Outcome   string    `json:"outcome"`
	ErrorCode string    `json:"error_code"`
	Actor     struct {
		Type           string `json:"type"`
		ID             string `json:"id"`
		SessionID      string `json:"session_id"`
		OnBehalfOf     string `json:"on_behalf_of"`
		ImpersonatedBy string `json:"impersonated_by"`
	} `json:"actor"`
	Channel struct {
		Type      string `json:"type"`
		IP        string `json:"ip"`
		UserAgent string `json:"user_agent"`
	} `json:"channel"`
	Target *struct {
		Type string `json:"type"`
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"target"`
	Request struct {
		RequestID string `json:"request_id"`
		TraceID   string `json:"trace_id"`
		Operation string `json:"operation"`
	} `json:"request"`
	Origin struct {
		Region  string `json:"region"`
		Cell    string `json:"cell"`
		Service string `json:"service"`
	} `json:"origin"`
}

// Export converts one audit record (its JSON form) to an OCSF event.
func Export(raw []byte) ([]byte, error) {
	var r record
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, errs.Wrap(errs.CodeInvalidRequest, err, "ocsf: malformed audit record")
	}
	if r.Category == "" || r.Type == "" {
		return nil, errs.New(errs.CodeInvalidRequest, "ocsf: the record has no category or type")
	}
	c := Map(r.Category, r.Type, r.Outcome)
	status, statusName := StatusSuccess, "Success"
	if r.Outcome == "failure" {
		status, statusName = StatusFailure, "Failure"
	}
	ev := map[string]any{
		"category_uid": c.CategoryUID,
		"class_uid":    c.ClassUID,
		"activity_id":  c.ActivityID,
		"type_uid":     c.TypeUID,
		"severity_id":  SeverityInformational,
		"status_id":    status,
		"status":       statusName,
		"time":         r.Time.UnixMilli(),
		"message":      r.Type,
		"metadata": map[string]any{
			"version":         Version,
			"uid":             r.ID,
			"sequence":        r.Sequence,
			"tenant_uid":      r.TenantID,
			"event_code":      r.Type,
			"correlation_uid": r.Request.TraceID,
			"product": map[string]any{
				"name": "Portun", "vendor_name": "Portun", "feature": map[string]any{"name": r.Origin.Service},
			},
		},
		"actor": map[string]any{
			"user":       map[string]any{"uid": r.Actor.ID, "type": r.Actor.Type},
			"session":    map[string]any{"uid": r.Actor.SessionID},
			"invoked_by": r.Actor.ImpersonatedBy,
		},
		"api": map[string]any{
			"operation": r.Request.Operation,
			"request":   map[string]any{"uid": r.Request.RequestID},
		},
		"src_endpoint": map[string]any{"ip": r.Channel.IP, "svc_name": r.Channel.Type},
		"http_request": map[string]any{"user_agent": r.Channel.UserAgent},
		"cloud":        map[string]any{"region": r.Origin.Region, "zone": r.Origin.Cell, "provider": "portun"},
		"unmapped":     map[string]any{"portun": json.RawMessage(raw)},
	}
	if r.ErrorCode != "" {
		ev["status_code"] = r.ErrorCode
	}
	if r.Target != nil {
		ev["resources"] = []map[string]any{{"uid": r.Target.ID, "type": r.Target.Type, "name": r.Target.Name}}
	}
	out, err := json.Marshal(ev)
	if err != nil {
		return nil, errs.Wrap(errs.CodeInternal, err, "ocsf: encode")
	}
	return out, nil
}
