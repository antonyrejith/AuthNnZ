// Package scope carries the request's tenant, actor and request metadata in
// the context (ADR-0002). Code that touches tenant data gets the tenant from
// here; a missing scope is an error, never a default.
package scope

import (
	"context"
	"log/slog"

	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/ids"
)

type ctxKey int

const (
	tenantKey ctxKey = iota
	actorKey
	requestKey
)

// Tenant identifies the tenant and environment a request works in.
type Tenant struct {
	TenantID      ids.TenantID
	EnvironmentID ids.EnvironmentID
}

// WithTenant returns a context carrying t.
func WithTenant(ctx context.Context, t Tenant) context.Context {
	return context.WithValue(ctx, tenantKey, t)
}

// TenantFrom returns the tenant scope. It fails if none was set.
func TenantFrom(ctx context.Context) (Tenant, error) {
	t, ok := ctx.Value(tenantKey).(Tenant)
	if !ok || t.TenantID.IsZero() {
		return Tenant{}, errs.New(errs.CodeInternal, "no tenant scope in context")
	}
	return t, nil
}

// ActorType classifies who performs an action (audit records, decisions).
type ActorType string

// Actor types.
const (
	ActorUser             ActorType = "user"
	ActorAdmin            ActorType = "admin"
	ActorService          ActorType = "service"
	ActorAPIKey           ActorType = "api_key"
	ActorAgent            ActorType = "agent"
	ActorSystem           ActorType = "system"
	ActorSCIM             ActorType = "scim"
	ActorConnector        ActorType = "connector"
	ActorSupport          ActorType = "support"
	ActorPlatformOperator ActorType = "platform_operator"
)

// Actor is the authenticated party performing the request.
type Actor struct {
	Type           ActorType `json:"type"`
	ID             string    `json:"id"`
	SessionID      string    `json:"session_id,omitempty"`
	AuthACR        string    `json:"acr,omitempty"`
	AuthAMR        []string  `json:"amr,omitempty"`
	OnBehalfOf     string    `json:"on_behalf_of,omitempty"`
	ImpersonatedBy string    `json:"impersonated_by,omitempty"`
}

// WithActor returns a context carrying a.
func WithActor(ctx context.Context, a Actor) context.Context {
	return context.WithValue(ctx, actorKey, a)
}

// ActorFrom returns the actor, if any.
func ActorFrom(ctx context.Context) (Actor, bool) {
	a, ok := ctx.Value(actorKey).(Actor)
	return a, ok
}

// Request describes how a request arrived (channel, client, network).
type Request struct {
	RequestID    string `json:"request_id"`
	Channel      string `json:"channel"` // console, api, cli, terraform, gitops, scim, system, …
	ClientID     string `json:"client_id,omitempty"`
	IP           string `json:"ip,omitempty"`
	UserAgent    string `json:"user_agent,omitempty"`
	Operation    string `json:"operation,omitempty"`
	ChangeReason string `json:"change_reason,omitempty"`
	ChangeTicket string `json:"change_ticket,omitempty"`
}

// LogValue implements slog.LogValuer. It omits IP and UserAgent, which are
// personal data (they belong in audit records, not in logs), and the change
// reason, which is free text.
func (r Request) LogValue() slog.Value {
	attrs := make([]slog.Attr, 0, 5)
	add := func(key, value string) {
		if value != "" {
			attrs = append(attrs, slog.String(key, value))
		}
	}
	add("request_id", r.RequestID)
	add("channel", r.Channel)
	add("client_id", r.ClientID)
	add("operation", r.Operation)
	add("change_ticket", r.ChangeTicket)
	return slog.GroupValue(attrs...)
}

// WithRequest returns a context carrying r.
func WithRequest(ctx context.Context, r Request) context.Context {
	return context.WithValue(ctx, requestKey, r)
}

// RequestFrom returns the request metadata, if any.
func RequestFrom(ctx context.Context) (Request, bool) {
	r, ok := ctx.Value(requestKey).(Request)
	return r, ok
}
