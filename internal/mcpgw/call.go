package mcpgw

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/ncecere/open-mcp-gateway/internal/calls"
	"github.com/ncecere/open-mcp-gateway/internal/gateways"
	"github.com/ncecere/open-mcp-gateway/internal/identity"
	"github.com/ncecere/open-mcp-gateway/internal/oauth"
	"github.com/ncecere/open-mcp-gateway/internal/providers"
	"github.com/ncecere/open-mcp-gateway/internal/store/dbgen"
	"github.com/ncecere/open-mcp-gateway/internal/upstream"
)

// callParams are tools/call and prompts/get parameters.
type callParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// notFound is what a refused or unknown call sees (MCP: unknown tools are
// invalid params).
func notFound(kind, name string) string {
	if kind == upstream.KindPrompt {
		return fmt.Sprintf("Unknown prompt: %s", clip(name, 200))
	}
	return fmt.Sprintf("Unknown tool: %s", clip(name, 200))
}

// call forwards tools/call or prompts/get after re-checking the
// capability. A refusal never reaches the server.
func (rc *request) call(kind string) {
	var p callParams
	if len(rc.msg.Params) == 0 || json.Unmarshal(rc.msg.Params, &p) != nil || p.Name == "" {
		n := rc.fail(codeInvalidParams, "Invalid parameters: name is required")
		rc.record(calls.Call{Outcome: calls.OutcomeBadRequest, ErrorClass: "invalid_params"}, n)
		return
	}
	args := bytes.TrimSpace(p.Arguments)
	if len(args) > 0 && !bytes.Equal(args, []byte("null")) && args[0] != '{' {
		n := rc.fail(codeInvalidParams, "Invalid parameters: arguments must be an object")
		rc.record(calls.Call{Outcome: calls.OutcomeBadRequest, ErrorClass: "invalid_params", ExposedName: p.Name}, n)
		return
	}
	offers, err := rc.offers(kind)
	if err != nil {
		rc.internal(err)
		return
	}
	var offer *gateways.Offer
	for i := range offers {
		if offers[i].ExposedName == p.Name {
			offer = &offers[i]
		}
	}
	if offer == nil || !offer.Allowed {
		n := rc.fail(codeInvalidParams, notFound(kind, p.Name))
		c := calls.Call{Outcome: calls.OutcomeDenied, ErrorClass: "unknown", ExposedName: p.Name}
		if offer != nil {
			c.ErrorClass, c.Server, c.CapabilityID = offer.Denied, &offer.Server, offer.Capability.ID
			if offer.Requires != nil {
				c.ProviderSlug = offer.Requires.Provider.Slug
			}
		}
		rc.record(c, n)
		return
	}
	rc.forward(kind, *offer, args)
}

// forward sends the call to the server with the caller's signed identity.
func (rc *request) forward(kind string, o gateways.Offer, args json.RawMessage) {
	ctx := rc.r.Context()
	rec := calls.Call{ExposedName: o.ExposedName, Server: &o.Server, CapabilityID: o.Capability.ID}
	cred, ok := rc.delegated(o, &rec)
	if !ok {
		return
	}
	target, err := rc.h.Registry.Target(ctx, o.Server)
	if err != nil {
		rc.upstreamFailed(rec, err)
		return
	}
	who := identityFor{tool: o.ExposedName}
	if kind == upstream.KindPrompt {
		who = identityFor{prompt: o.ExposedName}
	}
	token, err := rc.identityToken(who, o, cred)
	if err != nil {
		rc.internal(err)
		return
	}
	target.IdentityHeader, target.IdentityValue = identity.Header(target.HeaderName, token)
	withCredential(&target, cred)
	client := rc.h.Registry.Client()
	var result any
	isError := false
	switch kind {
	case upstream.KindTool:
		rc.captureCall(o, args, target, token, cred)
		res, _, err := client.CallTool(ctx, target, o.Capability.Name, args)
		if err != nil {
			rc.upstreamFailed(rec, err)
			return
		}
		result, isError = res, res.IsError
	default:
		var sargs map[string]string
		if len(args) > 0 && !bytes.Equal(args, []byte("null")) {
			if err := json.Unmarshal(args, &sargs); err != nil {
				n := rc.fail(codeInvalidParams, "Invalid parameters: prompt arguments must be strings")
				rec.Outcome, rec.ErrorClass = calls.OutcomeBadRequest, "invalid_params"
				rc.record(rec, n)
				return
			}
		}
		rc.captureCall(o, args, target, token, cred)
		res, _, err := client.GetPrompt(ctx, target, o.Capability.Name, sargs)
		if err != nil {
			rc.upstreamFailed(rec, err)
			return
		}
		result = res
	}
	rc.relay(rec, result, isError)
}

// relay answers with a server's result (without upstreamMetaKeys, at most
// MaxResponseBytes) and records the exchange.
func (rc *request) relay(rec calls.Call, result any, isError bool) {
	encoded, err := json.Marshal(result)
	if err == nil {
		encoded, err = scrubMeta(encoded)
	}
	if err != nil || len(encoded) > MaxResponseBytes {
		n := rc.fail(codeInternal, "The server's response is too large")
		rec.Outcome, rec.ErrorClass = calls.OutcomeTooLarge, upstream.ClassTooLarge
		rc.record(rec, n)
		return
	}
	rc.captureResult(encoded)
	n := rc.reply(json.RawMessage(encoded))
	rec.Outcome = calls.OutcomeOK
	if isError {
		rec.Outcome = calls.OutcomeToolError
	}
	rc.record(rec, n)
}

// upstreamMetaKeys are _meta entries about the upstream itself that clients
// of a gateway don't need: the gateway is the server they talk to.
var upstreamMetaKeys = []string{"io.modelcontextprotocol/serverInfo"}

// scrubMeta removes upstreamMetaKeys from a result's _meta (and an empty
// _meta), leaving everything else as the upstream sent it.
func scrubMeta(encoded []byte) ([]byte, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &obj); err != nil {
		return encoded, nil // not an object: nothing to scrub
	}
	raw, ok := obj["_meta"]
	if !ok {
		return encoded, nil
	}
	var meta map[string]json.RawMessage
	if err := json.Unmarshal(raw, &meta); err != nil {
		return encoded, nil
	}
	changed := false
	for _, k := range upstreamMetaKeys {
		if _, ok := meta[k]; ok {
			delete(meta, k)
			changed = true
		}
	}
	if !changed {
		return encoded, nil
	}
	if len(meta) == 0 {
		delete(obj, "_meta")
	} else {
		m, err := json.Marshal(meta)
		if err != nil {
			return nil, err
		}
		obj["_meta"] = m
	}
	return json.Marshal(obj)
}

// upstreamFailed answers a failed exchange with a JSON-RPC error (the
// message is safe to show: internal/upstream redacts secrets).
func (rc *request) upstreamFailed(rec calls.Call, err error) {
	var ue *upstream.Error
	if !errors.As(err, &ue) {
		rc.internal(err)
		return
	}
	code := int64(codeInternal)
	rec.Outcome, rec.ErrorClass = calls.OutcomeUpstreamError, ue.Class
	switch {
	case ue.Class == upstream.ClassTimeout:
		rec.Outcome = calls.OutcomeTimeout
	case ue.Class == upstream.ClassTooLarge:
		rec.Outcome = calls.OutcomeTooLarge
	case ue.RPCCode == codeInvalidParams:
		code, rec.Outcome = codeInvalidParams, calls.OutcomeBadRequest
	case ue.RPCCode == codeResourceNotFound:
		code, rec.Outcome, rec.ErrorClass = codeResourceNotFound, calls.OutcomeBadRequest, upstream.ClassNotFound
	}
	if ue.RPCCode != 0 {
		rc.captureError(code, ue.Message)
	}
	n := rc.fail(code, ue.Message)
	rc.record(rec, n)
}

// identityFor is what a token is for: the exposed tool or prompt name, or
// the exposed resource URI read (exactly one).
type identityFor struct {
	tool, prompt, resource string
}

// identityToken signs who is calling, for the server's URL: a person
// (principal_type user: their ID, email, name and teams) or a service
// account (principal_type service_account: its ID, name and team, no
// email). client is the connected OAuth client's name (with
// client_verified), else the request's clientInfo.name, else the key's name.
func (rc *request) identityToken(what identityFor, o gateways.Offer, cred *providers.Credential) (string, error) {
	p := rc.principal
	c := identity.Claims{Audience: o.Server.URL, Gateway: rc.gateway.Slug, Tool: what.tool, Prompt: what.prompt, Resource: what.resource}
	if cred != nil {
		// Only calls that carry a provider token name the provider.
		c.Provider = cred.Provider.Slug
	}
	if sa := p.account; sa != nil {
		c.PrincipalType, c.Subject, c.Name, c.Teams = identity.PrincipalServiceAccount, sa.Account.ID.String(), sa.Account.Name, []string{sa.Team.Slug}
	} else {
		u := p.user
		teamRows, err := dbgen.New(rc.h.Pool).ListTeamsForUser(rc.r.Context(), u.ID)
		if err != nil {
			return "", err
		}
		slugs := make([]string, len(teamRows))
		for i, t := range teamRows {
			slugs[i] = t.Slug
		}
		c.PrincipalType, c.Subject, c.Email, c.Name, c.Teams = identity.PrincipalUser, u.ID.String(), u.Email, u.DisplayName, slugs
	}
	switch {
	case p.grant != nil:
		// Only a metadata-document client's name is vouched for by its host;
		// a client that registered itself chose its own name.
		verified := p.grant.ClientKind == oauth.KindMetadata
		c.Client, c.ClientVerified = p.grant.ClientName, &verified
	default:
		if c.Client = rc.clientName(); c.Client == "" {
			c.Client = p.keyName
		}
	}
	return rc.h.Identity.Sign(rc.r.Context(), c)
}
