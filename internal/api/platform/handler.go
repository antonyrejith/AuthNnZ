package platform

import (
	"context"
	"strconv"
	"strings"

	"github.com/antonyrejith/portun/internal/api/platformgen"
	"github.com/antonyrejith/portun/internal/modules/tenancy"
	"github.com/antonyrejith/portun/internal/platform/db/page"
	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/ids"
	"github.com/antonyrejith/portun/internal/platform/scope"
)

// security accepts the request when the authentication middleware has set an
// actor. Credentials are checked there, before the request body is read; this
// guards against a server that was built without that middleware.
type security struct{}

// HandleOperatorToken implements platformgen.SecurityHandler.
func (security) HandleOperatorToken(ctx context.Context, _ platformgen.OperationName, _ platformgen.OperatorToken) (context.Context, error) {
	if _, ok := scope.ActorFrom(ctx); !ok {
		return ctx, errs.New(errs.CodeUnauthenticated, "A valid bearer token is required.")
	}
	return ctx, nil
}

// handler implements platformgen.Handler with the tenancy facade.
type handler struct {
	tenancy *tenancy.Module
}

var _ platformgen.Handler = (*handler)(nil)

func (h *handler) CreatePartner(ctx context.Context, req *platformgen.CreatePartnerRequest, _ platformgen.CreatePartnerParams) (platformgen.CreatePartnerRes, error) {
	in := tenancy.CreatePartnerInput{Name: req.Name}
	if v, ok := req.ParentID.Get(); ok {
		parent, err := parseID[ids.PartnerKind]("/parent_id", string(v))
		if err != nil {
			return nil, err
		}
		in.ParentID = parent
	}
	p, err := h.tenancy.CreatePartner(ctx, in)
	if err != nil {
		return nil, err //nolint:wrapcheck // *errs.Error rendered by the error handler
	}
	out := partnerOut(p)
	return &out, nil
}

func (h *handler) CreateTenant(ctx context.Context, req *platformgen.CreateTenantRequest, _ platformgen.CreateTenantParams) (platformgen.CreateTenantRes, error) {
	in := tenancy.CreateTenantInput{Slug: req.Slug, Name: req.Name, Region: req.Region, Plan: req.Plan}
	if v, ok := req.PartnerID.Get(); ok {
		partner, err := parseID[ids.PartnerKind]("/partner_id", string(v))
		if err != nil {
			return nil, err
		}
		in.PartnerID = partner
	}
	for _, e := range req.Environments {
		in.Environments = append(in.Environments, tenancy.EnvironmentInput{Name: e.Name, Kind: string(e.Kind)})
	}
	t, envs, err := h.tenancy.CreateTenant(ctx, in)
	if err != nil {
		return nil, err //nolint:wrapcheck // *errs.Error rendered by the error handler
	}
	out := tenantOut(t)
	created := platformgen.TenantCreated{
		ID: out.ID, PartnerID: out.PartnerID, Slug: out.Slug, Name: out.Name, Status: out.Status, Region: out.Region,
		Plan: out.Plan, Version: out.Version, CreatedAt: out.CreatedAt, UpdatedAt: out.UpdatedAt,
		Environments: environmentsOut(envs),
	}
	return &platformgen.TenantCreatedHeaders{
		ETag: etag(t.Version), Location: Prefix + "tenants/" + t.ID.String(), Response: created,
	}, nil
}

func (h *handler) ListTenants(ctx context.Context, params platformgen.ListTenantsParams) (platformgen.ListTenantsRes, error) {
	in := tenancy.ListTenantsInput{
		Status: string(params.Status.Or("")),
		Page:   page.Request{Size: params.PageSize.Or(0), Token: params.PageToken.Or("")},
	}
	if v, ok := params.PartnerID.Get(); ok {
		partner, err := parseID[ids.PartnerKind]("/query/partner_id", string(v))
		if err != nil {
			return nil, err
		}
		in.PartnerID = partner
	}
	rows, next, err := h.tenancy.ListTenants(ctx, in)
	if err != nil {
		return nil, err //nolint:wrapcheck // *errs.Error rendered by the error handler
	}
	items := make([]platformgen.Tenant, 0, len(rows))
	for _, t := range rows {
		items = append(items, tenantOut(t))
	}
	return &platformgen.TenantList{Items: items, NextPageToken: next}, nil
}

func (h *handler) GetTenant(ctx context.Context, params platformgen.GetTenantParams) (platformgen.GetTenantRes, error) {
	id, err := parseID[ids.TenantKind]("/path/tenant_id", string(params.TenantID))
	if err != nil {
		return nil, err
	}
	t, err := h.tenancy.GetTenant(ctx, id)
	if err != nil {
		return nil, err //nolint:wrapcheck // *errs.Error rendered by the error handler
	}
	return tenantHeaders(t), nil
}

func (h *handler) UpdateTenant(ctx context.Context, req *platformgen.TenantPatch, params platformgen.UpdateTenantParams) (platformgen.UpdateTenantRes, error) {
	id, version, err := tenantAndVersion(params.TenantID, params.IfMatch)
	if err != nil {
		return nil, err
	}
	in := tenancy.UpdateTenantInput{}
	if v, ok := req.Name.Get(); ok {
		in.Name = &v
	}
	if v, ok := req.Plan.Get(); ok {
		in.Plan = &v
	}
	t, err := h.tenancy.UpdateTenant(ctx, id, version, in)
	if err != nil {
		return nil, err //nolint:wrapcheck // *errs.Error rendered by the error handler
	}
	return tenantHeaders(t), nil
}

func (h *handler) SuspendTenant(ctx context.Context, params platformgen.SuspendTenantParams) (platformgen.SuspendTenantRes, error) {
	id, version, err := tenantAndVersion(params.TenantID, params.IfMatch)
	if err != nil {
		return nil, err
	}
	t, err := h.tenancy.SuspendTenant(ctx, id, version, changeReason(ctx))
	if err != nil {
		return nil, err //nolint:wrapcheck // *errs.Error rendered by the error handler
	}
	return tenantHeaders(t), nil
}

func (h *handler) ResumeTenant(ctx context.Context, params platformgen.ResumeTenantParams) (platformgen.ResumeTenantRes, error) {
	id, version, err := tenantAndVersion(params.TenantID, params.IfMatch)
	if err != nil {
		return nil, err
	}
	t, err := h.tenancy.ResumeTenant(ctx, id, version, changeReason(ctx))
	if err != nil {
		return nil, err //nolint:wrapcheck // *errs.Error rendered by the error handler
	}
	return tenantHeaders(t), nil
}

func (h *handler) DeleteTenant(ctx context.Context, params platformgen.DeleteTenantParams) (platformgen.DeleteTenantRes, error) {
	id, version, err := tenantAndVersion(params.TenantID, params.IfMatch)
	if err != nil {
		return nil, err
	}
	t, err := h.tenancy.DeleteTenant(ctx, id, version, changeReason(ctx))
	if err != nil {
		return nil, err //nolint:wrapcheck // *errs.Error rendered by the error handler
	}
	out := tenantOut(t)
	return &out, nil
}

func (h *handler) ExportTenant(ctx context.Context, params platformgen.ExportTenantParams) (platformgen.ExportTenantRes, error) {
	id, err := parseID[ids.TenantKind]("/path/tenant_id", string(params.TenantID))
	if err != nil {
		return nil, err
	}
	op, err := h.tenancy.StartExport(ctx, id)
	if err != nil {
		return nil, err //nolint:wrapcheck // *errs.Error rendered by the error handler
	}
	return &platformgen.OperationHeaders{Location: Prefix + "operations/" + op.ID.String(), Response: operationOut(op)}, nil
}

func (h *handler) GetOperation(ctx context.Context, params platformgen.GetOperationParams) (platformgen.GetOperationRes, error) {
	id, err := parseID[ids.OperationKind]("/path/operation_id", string(params.OperationID))
	if err != nil {
		return nil, err
	}
	op, err := h.tenancy.GetOperation(ctx, id)
	if err != nil {
		return nil, err //nolint:wrapcheck // *errs.Error rendered by the error handler
	}
	out := operationOut(op)
	return &out, nil
}

func (h *handler) ListEnvironments(ctx context.Context, params platformgen.ListEnvironmentsParams) (platformgen.ListEnvironmentsRes, error) {
	id, err := parseID[ids.TenantKind]("/path/tenant_id", string(params.TenantID))
	if err != nil {
		return nil, err
	}
	envs, err := h.tenancy.ListEnvironments(ctx, id)
	if err != nil {
		return nil, err //nolint:wrapcheck // *errs.Error rendered by the error handler
	}
	return &platformgen.EnvironmentList{Items: environmentsOut(envs)}, nil
}

func (h *handler) AddHost(ctx context.Context, req *platformgen.AddHostRequest, params platformgen.AddHostParams) (platformgen.AddHostRes, error) {
	tenant, err := parseID[ids.TenantKind]("/path/tenant_id", string(params.TenantID))
	if err != nil {
		return nil, err
	}
	env, err := parseID[ids.EnvironmentKind]("/environment_id", string(req.EnvironmentID))
	if err != nil {
		return nil, err
	}
	if err := h.tenancy.AddHost(ctx, tenant, env, req.Host); err != nil {
		return nil, err //nolint:wrapcheck // *errs.Error rendered by the error handler
	}
	return &platformgen.Host{
		Host:          strings.ToLower(strings.TrimSuffix(req.Host, ".")),
		TenantID:      params.TenantID,
		EnvironmentID: req.EnvironmentID,
	}, nil
}

func (h *handler) RemoveHost(ctx context.Context, params platformgen.RemoveHostParams) (platformgen.RemoveHostRes, error) {
	if err := h.tenancy.RemoveHost(ctx, params.Host); err != nil {
		return nil, err //nolint:wrapcheck // *errs.Error rendered by the error handler
	}
	return &platformgen.RemoveHostNoContent{}, nil
}

// changeReason returns the Change-Reason that the API middleware recorded in
// the request scope (trimmed and checked).
func changeReason(ctx context.Context) string {
	req, _ := scope.RequestFrom(ctx)
	return req.ChangeReason
}

// etag is the strong validator of a version.
func etag(version int64) string { return `"` + strconv.FormatInt(version, 10) + `"` }

// parseIfMatch returns the version named by an If-Match header: one strong
// validator, a quoted decimal such as "3". Weak validators, lists and "*" are
// rejected, as is every other form: the only ETags this API issues are quoted
// versions.
func parseIfMatch(v string) (int64, error) {
	bad := errs.New(errs.CodePreconditionFailed, `If-Match must be the current ETag of the resource, a quoted version such as "3".`)
	if len(v) < 3 || v[0] != '"' || v[len(v)-1] != '"' {
		return 0, bad
	}
	digits := v[1 : len(v)-1]
	for i := range len(digits) {
		if digits[i] < '0' || digits[i] > '9' {
			return 0, bad
		}
	}
	n, err := strconv.ParseInt(digits, 10, 64)
	if err != nil || n < 1 {
		return 0, bad
	}
	return n, nil
}

// tenantAndVersion parses the tenant path parameter and If-Match.
func tenantAndVersion(tenant platformgen.TenantId, ifMatch string) (ids.TenantID, int64, error) {
	id, err := parseID[ids.TenantKind]("/path/tenant_id", string(tenant))
	if err != nil {
		return ids.TenantID{}, 0, err
	}
	version, err := parseIfMatch(ifMatch)
	if err != nil {
		return ids.TenantID{}, 0, err
	}
	return id, version, nil
}

// parseID parses a TypeID that already matched the specification's pattern
// (which cannot check that the suffix decodes).
func parseID[K ids.Kind](pointer, s string) (ids.ID[K], error) {
	id, err := ids.Parse[K](s)
	if err != nil {
		return id, errs.New(errs.CodeValidationFailed, "The request contains invalid fields.").
			WithFields(errs.FieldError{Pointer: pointer, Code: "invalid_format", Detail: "not a valid identifier"})
	}
	return id, nil
}

func optPartnerID(id ids.PartnerID) platformgen.OptPartnerId {
	if id.IsZero() {
		return platformgen.OptPartnerId{}
	}
	return platformgen.NewOptPartnerId(platformgen.PartnerId(id.String()))
}

func partnerOut(p tenancy.Partner) platformgen.Partner {
	return platformgen.Partner{
		ID: platformgen.PartnerId(p.ID.String()), ParentID: optPartnerID(p.ParentID), Name: p.Name,
		Status: platformgen.PartnerStatus(p.Status), Version: p.Version, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
	}
}

func tenantOut(t tenancy.Tenant) platformgen.Tenant {
	return platformgen.Tenant{
		ID: platformgen.TenantId(t.ID.String()), PartnerID: optPartnerID(t.PartnerID), Slug: t.Slug, Name: t.Name,
		Status: platformgen.TenantStatus(t.Status), Region: t.Region, Plan: t.Plan, Version: t.Version,
		CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt,
	}
}

func tenantHeaders(t tenancy.Tenant) *platformgen.TenantHeaders {
	return &platformgen.TenantHeaders{ETag: etag(t.Version), Response: tenantOut(t)}
}

func environmentsOut(in []tenancy.Environment) []platformgen.Environment {
	out := make([]platformgen.Environment, 0, len(in))
	for _, e := range in {
		out = append(out, platformgen.Environment{
			ID: platformgen.EnvironmentId(e.ID.String()), TenantID: platformgen.TenantId(e.TenantID.String()), Name: e.Name,
			Kind: platformgen.EnvironmentKind(e.Kind), Version: e.Version, CreatedAt: e.CreatedAt,
		})
	}
	return out
}

func operationOut(o tenancy.Operation) platformgen.Operation {
	out := platformgen.Operation{
		ID: platformgen.OperationId(o.ID.String()), TenantID: platformgen.TenantId(o.TenantID.String()),
		Kind: platformgen.OperationKind(o.Kind), Status: platformgen.OperationStatus(o.Status),
		CreatedAt: o.CreatedAt, UpdatedAt: o.UpdatedAt,
	}
	if o.ErrorCode != "" {
		out.ErrorCode = platformgen.NewOptString(string(o.ErrorCode))
	}
	return out
}
