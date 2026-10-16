package httpapi

import (
	"bytes"
	"encoding/json"

	"github.com/ncecere/open-mcp-gateway/internal/httpapi/apitypes"
	"github.com/ncecere/open-mcp-gateway/internal/registry"
	"github.com/ncecere/open-mcp-gateway/internal/store/dbgen"
)

// Conversions of the registry's views to API types. The header value and
// its ciphertext have no field here: only whether one is set, the header's
// name and the value's hint.

func toAPICounts(c registry.Counts) apitypes.CapabilityCounts {
	return apitypes.CapabilityCounts{Tools: c.Tools, Resources: c.Resources, ResourceTemplates: c.ResourceTemplates, Prompts: c.Prompts}
}

// toAPIHealth is the latest check as a status. withMessage is false in the
// catalog, where any signed-in person reads it.
func toAPIHealth(h *dbgen.McpHealthCheck, withMessage bool) apitypes.ServerHealth {
	if h == nil {
		return apitypes.ServerHealth{Status: "unknown"}
	}
	out := apitypes.ServerHealth{Status: "healthy", CheckedAt: &h.CheckedAt, StatusSince: &h.StatusSince, LatencyMs: &h.LatencyMs}
	if !h.Ok {
		out.Status = "failing"
		if h.ErrorClass != nil {
			c := apitypes.McpErrorClass(*h.ErrorClass)
			out.ErrorClass = &c
		}
		if withMessage && h.Message != "" {
			msg := h.Message
			out.Message = &msg
		}
	}
	return out
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func toAPIServer(v registry.ServerView) apitypes.McpServer {
	s := v.Server
	return apitypes.McpServer{
		Id: s.ID, TeamId: s.TeamID, TeamSlug: v.TeamSlug, TeamName: v.TeamName,
		Slug: s.Slug, Name: s.Name, Description: s.Description, Url: s.URL,
		Transport:      apitypes.McpServerTransport(s.Transport),
		AuthHeaderName: s.AuthHeaderName, HasAuth: v.HasAuth(), AuthValueHint: s.AuthValueHint,
		TimeoutSeconds: s.TimeoutSeconds, Enabled: s.Enabled,
		PublishState:           apitypes.PublishState(s.PublishState),
		EffectivePublishPolicy: apitypes.PublishPolicy(v.EffectivePolicy),
		PublishRequestedAt:     s.PublishRequestedAt, PublishRequestNote: s.PublishRequestNote,
		PublishReviewedAt: s.PublishReviewedAt, PublishReviewedByName: nonEmpty(v.ReviewedByName), PublishReviewNote: s.PublishReviewNote,
		Counts: toAPICounts(v.Counts), PendingChanges: v.PendingChanges, Health: toAPIHealth(v.Health, true),
		RefreshedAt: s.RefreshedAt, LastRefreshError: s.LastRefreshError,
		Revision: s.Revision, CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt,
	}
}

// definitionMap decodes a stored definition, keeping numbers exactly.
func definitionMap(raw json.RawMessage) map[string]any {
	out := map[string]any{}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	_ = dec.Decode(&out)
	return out
}

func toAPICapability(c registry.CapabilityView) apitypes.McpCapability {
	cp := c.Capability
	out := apitypes.McpCapability{
		Id: cp.ID, ServerId: cp.ServerID, Kind: apitypes.CapabilityKind(cp.Kind), Name: cp.Name, Title: cp.Title,
		Description: cp.Description, Definition: definitionMap(cp.Definition), Fingerprint: cp.Fingerprint,
		Approved: c.Approved(), Changed: c.Changed(),
		FirstSeenAt: cp.FirstSeenAt, LastSeenAt: cp.LastSeenAt, GoneAt: cp.GoneAt,
	}
	if cp.ApprovedFingerprint != nil {
		out.ApprovedAt, out.ApprovedByName = cp.ApprovedAt, nonEmpty(c.ApprovedByName)
	}
	return out
}

func toAPICapabilities(cs []registry.CapabilityView) []apitypes.McpCapability {
	out := make([]apitypes.McpCapability, len(cs))
	for i, c := range cs {
		out[i] = toAPICapability(c)
	}
	return out
}

func toAPITestResult(r registry.TestResult) apitypes.McpTestResult {
	out := apitypes.McpTestResult{Ok: r.OK, LatencyMs: r.Latency.Milliseconds(),
		ServerName: nonEmpty(r.ServerName), ServerVersion: nonEmpty(r.ServerVersion), ProtocolVersion: nonEmpty(r.ProtocolVersion)}
	if r.OK {
		out.Counts = &apitypes.CapabilityCounts{Tools: int64(r.Counts.Tools), Resources: int64(r.Counts.Resources),
			ResourceTemplates: int64(r.Counts.ResourceTemplates), Prompts: int64(r.Counts.Prompts)}
	}
	if r.Err != nil {
		class := apitypes.McpErrorClass(r.Err.Class)
		out.ErrorClass, out.Message = &class, nonEmpty(r.Err.Message)
		if r.Err.HTTPStatus > 0 {
			st := int32(r.Err.HTTPStatus)
			out.HttpStatus = &st
		}
	}
	return out
}

func toAPIRefresh(r registry.RefreshResult) apitypes.McpRefreshResult {
	return apitypes.McpRefreshResult{Listed: r.Listed, Added: r.Added, Changed: r.Changed, Gone: r.Gone,
		Capabilities: toAPICapabilities(r.Capabilities)}
}

func toAPIHealthCheck(h dbgen.McpHealthCheck) apitypes.McpHealthCheck {
	out := apitypes.McpHealthCheck{CheckedAt: h.CheckedAt, Ok: h.Ok, LatencyMs: h.LatencyMs,
		Trigger: apitypes.McpHealthCheckTrigger(h.Trigger), HttpStatus: h.HttpStatus, Message: nonEmpty(h.Message)}
	if h.ErrorClass != nil {
		c := apitypes.McpErrorClass(*h.ErrorClass)
		out.ErrorClass = &c
	}
	return out
}

func toAPICatalogServer(v registry.ServerView) apitypes.CatalogServer {
	s := v.Server
	out := apitypes.CatalogServer{Id: s.ID, Slug: s.Slug, Name: s.Name, Description: s.Description,
		TeamSlug: v.TeamSlug, TeamName: v.TeamName, Counts: toAPICounts(v.Approved), Health: toAPIHealth(v.Health, false)}
	if s.PublishedAt != nil {
		out.PublishedAt = *s.PublishedAt
	}
	return out
}

func toAPICatalogCapability(c dbgen.McpCapability) apitypes.CatalogCapability {
	return apitypes.CatalogCapability{Id: c.ID, Kind: apitypes.CapabilityKind(c.Kind), Name: c.Name, Title: c.Title,
		Description: c.Description, Definition: definitionMap(c.Definition)}
}

func toAPITeamPublishing(p registry.TeamPublishing) apitypes.TeamPublishing {
	out := apitypes.TeamPublishing{Effective: apitypes.PublishPolicy(p.Effective), DefaultPolicy: apitypes.PublishPolicy(p.Default),
		Revision: p.TeamRevision}
	if p.Override != nil {
		o := apitypes.PublishPolicy(*p.Override)
		out.Override = &o
	}
	return out
}

func toAPISettings(s registry.Settings) apitypes.PublishingSettings {
	return apitypes.PublishingSettings{DefaultPolicy: apitypes.PublishPolicy(s.PublishDefaultPolicy), Revision: s.Revision, UpdatedAt: s.UpdatedAt}
}
