-- Delegated credentials (internal/providers): auth providers, people's
-- connections to them, consent links and authorization states.

-- name: InsertAuthProvider :one
INSERT INTO auth_providers (
    id, slug, name, preset, enabled, client_id, client_secret_cipher, client_secret_hint, token_auth_method, pkce,
    authorization_url, token_url, revocation_url, userinfo_url, default_scopes, scope_param, scope_separator,
    token_response_format, extra_authorize_params, logo_url, created_by, updated_by
) VALUES (
    @id, @slug, @name, @preset, @enabled, @client_id, @client_secret_cipher, @client_secret_hint, @token_auth_method, @pkce,
    @authorization_url, @token_url, @revocation_url, @userinfo_url, @default_scopes, @scope_param, @scope_separator,
    @token_response_format, @extra_authorize_params, @logo_url, @created_by, @created_by
)
RETURNING *;

-- name: GetAuthProvider :one
SELECT * FROM auth_providers WHERE id = $1;

-- name: LockAuthProvider :one
SELECT * FROM auth_providers WHERE id = $1 FOR UPDATE;

-- name: CountAuthProviders :one
SELECT count(*)::bigint FROM auth_providers;

-- name: ListAllAuthProviders :many
SELECT * FROM auth_providers ORDER BY name, id;

-- name: UpdateAuthProvider :one
UPDATE auth_providers
SET name = @name, preset = @preset, enabled = @enabled, client_id = @client_id, client_secret_cipher = @client_secret_cipher,
    client_secret_hint = @client_secret_hint, token_auth_method = @token_auth_method, pkce = @pkce,
    authorization_url = @authorization_url, token_url = @token_url, revocation_url = @revocation_url,
    userinfo_url = @userinfo_url, default_scopes = @default_scopes, scope_param = @scope_param,
    scope_separator = @scope_separator, token_response_format = @token_response_format,
    extra_authorize_params = @extra_authorize_params, logo_url = @logo_url,
    revision = revision + 1, updated_by = @updated_by, updated_at = now()
WHERE id = @id
RETURNING *;

-- name: DeleteAuthProvider :exec
DELETE FROM auth_providers WHERE id = $1;

-- name: AuthProviderStats :many
-- Per provider: the MCP servers that require it, and the connections to it.
SELECT p.id AS provider_id,
       (SELECT count(*) FROM mcp_servers s WHERE s.auth_provider_id = p.id)::bigint AS server_count,
       (SELECT count(*) FROM provider_connections pc WHERE pc.provider_id = p.id)::bigint AS connection_count,
       COALESCE((SELECT COALESCE(NULLIF(u.display_name, ''), u.email::text) FROM users u WHERE u.id = p.created_by), '')::text
           AS created_by_name
FROM auth_providers p
WHERE p.id = ANY(@ids::uuid[]);

-- name: ServersRequiringProvider :many
-- MCP servers that require a provider (as their requirement; a capability
-- override always names its server's provider), at most limit, by name.
SELECT s.id, s.slug, s.name, t.slug AS team_slug
FROM mcp_servers s
JOIN teams t ON t.id = s.team_id
WHERE s.auth_provider_id = @provider_id
ORDER BY s.name, s.id
LIMIT @page_size;

-- name: GetProviderConnection :one
SELECT * FROM provider_connections WHERE user_id = @user_id AND provider_id = @provider_id;

-- name: LockProviderConnection :one
SELECT * FROM provider_connections WHERE user_id = @user_id AND provider_id = @provider_id FOR UPDATE;

-- name: LockProviderConnectionByID :one
SELECT * FROM provider_connections WHERE id = $1 FOR UPDATE;

-- name: InsertProviderConnection :one
INSERT INTO provider_connections (id, user_id, provider_id, status, scopes, account_label, access_token_cipher,
                                  refresh_token_cipher, expires_at)
VALUES (@id, @user_id, @provider_id, 'active', @scopes, @account_label, @access_token_cipher, @refresh_token_cipher, @expires_at)
RETURNING *;

-- name: ReconnectProviderConnection :one
-- New tokens after consent: active again, with the granted scopes.
UPDATE provider_connections
SET status = 'active', scopes = @scopes, account_label = COALESCE(sqlc.narg(account_label)::text, account_label),
    access_token_cipher = @access_token_cipher, refresh_token_cipher = @refresh_token_cipher, expires_at = @expires_at,
    connected_at = now(), refreshed_at = NULL, last_error_class = NULL
WHERE id = @id
RETURNING *;

-- name: SetProviderConnectionRefreshed :one
UPDATE provider_connections
SET access_token_cipher = @access_token_cipher, refresh_token_cipher = @refresh_token_cipher, expires_at = @expires_at,
    scopes = @scopes, refreshed_at = now(), last_error_class = NULL
WHERE id = @id
RETURNING *;

-- name: MarkProviderConnectionReconsent :exec
-- Erases the tokens; the row, scopes and label stay so the person sees what
-- to reconnect.
UPDATE provider_connections
SET status = 'needs_reconsent', access_token_cipher = NULL, refresh_token_cipher = NULL, expires_at = NULL,
    last_error_class = @error_class
WHERE id = @id;

-- name: MarkProviderConnectionsReconsent :execrows
-- Every connection to a provider whose client ID changed: their tokens
-- belong to the old app.
UPDATE provider_connections
SET status = 'needs_reconsent', access_token_cipher = NULL, refresh_token_cipher = NULL, expires_at = NULL,
    last_error_class = 'client_changed'
WHERE provider_id = @provider_id AND status = 'active';

-- name: TouchProviderConnection :exec
UPDATE provider_connections SET last_used_at = now()
WHERE id = $1 AND (last_used_at IS NULL OR last_used_at < now() - interval '1 minute');

-- name: DeleteProviderConnection :exec
DELETE FROM provider_connections WHERE id = $1;

-- name: ListProviderConnectionsForUser :many
-- A person's connections with their providers, by provider name.
SELECT sqlc.embed(pc), p.slug AS provider_slug, p.name AS provider_name, p.preset AS provider_preset,
       p.logo_url AS provider_logo_url
FROM provider_connections pc
JOIN auth_providers p ON p.id = pc.provider_id
WHERE pc.user_id = @user_id
ORDER BY p.name, p.id;

-- name: ListProviderConnectionsForProvider :many
SELECT * FROM provider_connections WHERE provider_id = @provider_id ORDER BY id;

-- name: InsertConnectRequest :one
INSERT INTO provider_connect_requests (
    id_digest, id_cipher, user_id, provider_id, required_scopes, reason, gateway_id, gateway_slug, gateway_name,
    capability_id, capability_kind, exposed_name, capability_title, expires_at
) VALUES (
    @id_digest, @id_cipher, @user_id, @provider_id, @required_scopes, @reason, @gateway_id, @gateway_slug, @gateway_name,
    @capability_id, @capability_kind, @exposed_name, @capability_title, @expires_at
)
RETURNING *;

-- name: FindOpenConnectRequest :one
-- A person's unused request for the same capability, provider and scopes
-- that stays valid for at least valid_until, to answer with the same link.
SELECT * FROM provider_connect_requests
WHERE user_id = @user_id AND provider_id = @provider_id AND gateway_id = @gateway_id AND capability_id = @capability_id
  AND required_scopes = @required_scopes::text[] AND used_at IS NULL AND expires_at > @valid_until
ORDER BY created_at DESC
LIMIT 1;

-- name: GetConnectRequest :one
SELECT * FROM provider_connect_requests WHERE id_digest = $1;

-- name: MarkConnectRequestUsed :execrows
UPDATE provider_connect_requests SET used_at = now() WHERE id_digest = $1 AND used_at IS NULL;

-- name: TrimOpenConnectRequests :execrows
-- Keeps a person's newest keep open requests; older open ones are dropped.
DELETE FROM provider_connect_requests
WHERE id_digest IN (
    SELECT r.id_digest FROM provider_connect_requests r
    WHERE r.user_id = @user_id AND r.used_at IS NULL AND r.expires_at > now()
    ORDER BY r.created_at DESC, r.id_digest
    OFFSET @keep::int
);

-- name: DeleteStaleConnectRequests :execrows
-- Requests that expired before before (they answer 410 until then).
DELETE FROM provider_connect_requests WHERE expires_at < @before;

-- name: InsertProviderAuthState :exec
INSERT INTO provider_auth_states (state_digest, user_id, session_digest, provider_id, scopes, required_scopes,
                                  code_verifier_cipher, request_digest, return_to, expires_at)
VALUES (@state_digest, @user_id, @session_digest, @provider_id, @scopes, @required_scopes, @code_verifier_cipher,
        @request_digest, @return_to, @expires_at);

-- name: TakeProviderAuthState :one
-- A state is single use: it is deleted before anything else is checked.
DELETE FROM provider_auth_states WHERE state_digest = $1 RETURNING *;

-- name: DeleteExpiredProviderAuthStates :execrows
DELETE FROM provider_auth_states WHERE expires_at < now();
