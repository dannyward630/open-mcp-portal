package mcpgw

import (
	"errors"
	"fmt"
	"strings"

	"github.com/ncecere/open-mcp-gateway/internal/calls"
	"github.com/ncecere/open-mcp-gateway/internal/gateways"
	"github.com/ncecere/open-mcp-gateway/internal/providers"
	"github.com/ncecere/open-mcp-gateway/internal/registry"
	"github.com/ncecere/open-mcp-gateway/internal/upstream"
)

// Delegated credentials at call time (docs/phase5-delegated-credentials.md
// "Runtime", decisions 27-29). After every other check passed, a call of a
// capability with an effective requirement:
//
//  1. from a service account is refused (-32010, data.reason
//     service_account; recorded denied);
//  2. of a turned-off provider is refused (-32010 provider_disabled;
//     recorded denied);
//  3. without an active connection covering the scopes gets a consent link
//     (-32042 URLElicitationRequiredError, the link also in the message;
//     recorded needs_authorization) and nothing is forwarded;
//  4. with a token expiring within a minute refreshes it first; a provider
//     that can't be reached is -32010 provider_unavailable (recorded
//     upstream_error) and nothing is forwarded;
//  5. is forwarded with MCP-Gateway-Provider and MCP-Gateway-Provider-Token
//     and the identity token's provider claim.
//
// Calls without a requirement (or with the override none) never get these.

// Error classes of refused calls (mcp_calls.error_class).
const (
	DeniedServiceAccount   = "service_account"
	DeniedProviderDisabled = "provider_disabled"
	ProviderUnavailable    = "provider_unavailable"
)

func kindNoun(kind string) string {
	switch kind {
	case upstream.KindPrompt:
		return "prompt"
	case upstream.KindResource, upstream.KindResourceTemplate:
		return "resource"
	}
	return "tool"
}

func scopeNote(scopes []string) string {
	if len(scopes) == 0 {
		return ""
	}
	return " (" + strings.Join(scopes, ", ") + ")"
}

// delegated gets the person's provider token for o when it requires one.
// It answers and records the call itself when the call can't go ahead (ok
// false). cred is nil for capabilities without a requirement.
func (rc *request) delegated(o gateways.Offer, rec *calls.Call) (cred *providers.Credential, ok bool) {
	req := o.Requires
	if req == nil {
		return nil, true
	}
	rec.ProviderSlug = req.Provider.Slug
	noun := kindNoun(o.Capability.Kind)
	if rc.principal.account != nil {
		n := rc.failData(codeDenied, fmt.Sprintf("This %s needs a person's %s account; service accounts can't use it", noun, req.Provider.Name),
			map[string]any{"reason": DeniedServiceAccount, "provider": req.Provider.Slug})
		rec.Outcome, rec.ErrorClass = calls.OutcomeDenied, DeniedServiceAccount
		rc.record(*rec, n)
		return nil, false
	}
	if rc.h.Providers == nil {
		rc.providerDisabled(req, rec)
		return nil, false
	}
	c, err := rc.h.Providers.Credential(rc.r.Context(), req, providers.CallContext{
		User: rc.principal.user, Gateway: rc.gateway, CapabilityID: o.Capability.ID, Kind: o.Capability.Kind,
		ExposedName: o.ExposedName, Title: o.Capability.Title,
	})
	var consent *providers.ConsentNeeded
	var down *providers.UnavailableError
	switch {
	case err == nil:
		return &c, true
	case errors.Is(err, providers.ErrProviderDisabled):
		rc.providerDisabled(req, rec)
	case errors.As(err, &consent):
		rc.needsAuthorization(o, consent, rec)
	case errors.As(err, &down):
		n := rc.failData(codeDenied, fmt.Sprintf("%s couldn't be reached to renew your access. Try again shortly.", req.Provider.Name),
			map[string]any{"reason": ProviderUnavailable, "provider": req.Provider.Slug})
		rec.Outcome, rec.ErrorClass = calls.OutcomeUpstreamError, ProviderUnavailable
		rc.record(*rec, n)
	default:
		rc.internal(err)
	}
	return nil, false
}

func (rc *request) providerDisabled(req *registry.Requirement, rec *calls.Call) {
	n := rc.failData(codeDenied, fmt.Sprintf("%s accounts are turned off on this gateway; ask a platform admin", req.Provider.Name),
		map[string]any{"reason": DeniedProviderDisabled, "provider": req.Provider.Slug})
	rec.Outcome, rec.ErrorClass = calls.OutcomeDenied, DeniedProviderDisabled
	rc.record(*rec, n)
}

// needsAuthorization answers URLElicitationRequiredError (MCP 2025-11-25)
// with one URL elicitation. The gateway is stateless and clients only state
// their capabilities on initialize, so every client gets it, and the
// message carries the link too for clients that only show errors. The URL
// holds an opaque ID only: it signs nobody in, and works only in the
// person's own browser session.
func (rc *request) needsAuthorization(o gateways.Offer, c *providers.ConsentNeeded, rec *calls.Call) {
	name, scopes := c.Provider.Name, scopeNote(c.Scopes)
	var msg string
	switch c.Reason {
	case providers.ReasonMissingScopes:
		msg = fmt.Sprintf("%s needs more access to your %s account%s. Open %s to grant it, then try again.", o.ExposedName, name, scopes, c.URL)
	case providers.ReasonNeedsReconsent:
		msg = fmt.Sprintf("%s needs you to reconnect your %s account%s. Open %s to reconnect it, then try again.", o.ExposedName, name, scopes, c.URL)
	default:
		msg = fmt.Sprintf("%s needs your %s account%s. Open %s to connect it, then try again.", o.ExposedName, name, scopes, c.URL)
	}
	data := map[string]any{"elicitations": []map[string]any{{
		"mode": "url", "elicitationId": c.RequestID, "url": c.URL,
		"message": fmt.Sprintf("Connect your %s account%s to use %s in the %s gateway.", name, scopes, o.ExposedName, rc.gateway.Name),
	}}}
	n := rc.failData(codeURLElicitationRequired, msg, data)
	rec.Outcome, rec.ErrorClass = calls.OutcomeNeedsAuthorization, c.Reason
	rc.record(*rec, n)
}

// withCredential adds the person's provider token to a forwarded call.
func withCredential(t *upstream.Target, cred *providers.Credential) {
	if cred != nil {
		t.Provider, t.ProviderToken = cred.Provider.Slug, cred.AccessToken
	}
}
