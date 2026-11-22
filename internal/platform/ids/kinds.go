package ids

// Kinds of identifiers. Prefixes must be unique (enforced by tests). Modules
// add their kinds here so collisions are impossible.

// PartnerKind identifies a partner (reseller or MSP) that owns tenants (prefix "ptn").
type PartnerKind struct{}

// Prefix returns "ptn".
func (PartnerKind) Prefix() string { return "ptn" }

// TenantKind identifies a tenant (prefix "ten").
type TenantKind struct{}

// Prefix returns "ten".
func (TenantKind) Prefix() string { return "ten" }

// EnvironmentKind identifies an environment within a tenant (prefix "env").
type EnvironmentKind struct{}

// Prefix returns "env".
func (EnvironmentKind) Prefix() string { return "env" }

// OrganizationKind identifies an organization within a tenant (prefix "org").
type OrganizationKind struct{}

// Prefix returns "org".
func (OrganizationKind) Prefix() string { return "org" }

// UserKind identifies a user (prefix "usr").
type UserKind struct{}

// Prefix returns "usr".
func (UserKind) Prefix() string { return "usr" }

// ApplicationKind identifies an application (OAuth client or SAML service provider) (prefix "app").
type ApplicationKind struct{}

// Prefix returns "app".
func (ApplicationKind) Prefix() string { return "app" }

// ConfigObjectKind identifies a configuration object (prefix "cfg").
type ConfigObjectKind struct{}

// Prefix returns "cfg".
func (ConfigObjectKind) Prefix() string { return "cfg" }

// AuditEventKind identifies an audit event (prefix "aud").
type AuditEventKind struct{}

// Prefix returns "aud".
func (AuditEventKind) Prefix() string { return "aud" }

// CheckpointKind identifies a signed audit checkpoint (prefix "chk").
type CheckpointKind struct{}

// Prefix returns "chk".
func (CheckpointKind) Prefix() string { return "chk" }

// RequestKind identifies an inbound request (prefix "req").
type RequestKind struct{}

// Prefix returns "req".
func (RequestKind) Prefix() string { return "req" }

// KeyKind identifies a cryptographic key (prefix "key").
type KeyKind struct{}

// Prefix returns "key".
func (KeyKind) Prefix() string { return "key" }

// OperationKind identifies a long-running operation (prefix "opr").
type OperationKind struct{}

// Prefix returns "opr".
func (OperationKind) Prefix() string { return "opr" }

// EventKind identifies a domain event (prefix "evt").
type EventKind struct{}

// Prefix returns "evt".
func (EventKind) Prefix() string { return "evt" }

// MessageKind identifies an outbox message (prefix "msg").
type MessageKind struct{}

// Prefix returns "msg".
func (MessageKind) Prefix() string { return "msg" }

// PartnerID identifies a partner (reseller or MSP) that owns tenants.
type PartnerID = ID[PartnerKind]

// TenantID identifies a tenant.
type TenantID = ID[TenantKind]

// EnvironmentID identifies an environment within a tenant.
type EnvironmentID = ID[EnvironmentKind]

// OrganizationID identifies an organization within a tenant.
type OrganizationID = ID[OrganizationKind]

// UserID identifies a user.
type UserID = ID[UserKind]

// ApplicationID identifies an application (OAuth client or SAML service provider).
type ApplicationID = ID[ApplicationKind]

// ConfigObjectID identifies a configuration object.
type ConfigObjectID = ID[ConfigObjectKind]

// AuditEventID identifies an audit event.
type AuditEventID = ID[AuditEventKind]

// CheckpointID identifies a signed audit checkpoint.
type CheckpointID = ID[CheckpointKind]

// RequestID identifies an inbound request.
type RequestID = ID[RequestKind]

// KeyID identifies a cryptographic key.
type KeyID = ID[KeyKind]

// OperationID identifies a long-running operation.
type OperationID = ID[OperationKind]

// EventID identifies a domain event.
type EventID = ID[EventKind]

// MessageID identifies an outbox message.
type MessageID = ID[MessageKind]

// AllKinds lists every kind, for uniqueness checks and documentation.
func AllKinds() []Kind {
	return []Kind{
		PartnerKind{},
		TenantKind{},
		EnvironmentKind{},
		OrganizationKind{},
		UserKind{},
		ApplicationKind{},
		ConfigObjectKind{},
		AuditEventKind{},
		CheckpointKind{},
		RequestKind{},
		KeyKind{},
		OperationKind{},
		EventKind{},
		MessageKind{},
	}
}
