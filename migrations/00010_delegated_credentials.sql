-- Phase 5: delegated credentials (docs/phase5-delegated-credentials.md;
-- decisions 25-30):
--
--   * auth_providers: OAuth apps of third-party services that platform
--     admins configure (generic OAuth 2.0 authorization code, PKCE where the
--     provider supports it). The client secret is sealed with
--     ENCRYPTION_KEY (internal/secrets, bound to the provider's ID) and never
--     returned; client_secret_hint is its last four characters.
--   * mcp_servers.auth_provider_id/auth_scopes: a server's requirement (the
--     provider whose user token the gateway sends with its calls, and the
--     scopes; empty: the provider's default scopes). RESTRICT: a provider a
--     server requires can't be deleted.
--   * mcp_capabilities.requirement_mode/requirement_scopes: a capability's
--     override of its server's requirement (inherit, none, or other scopes
--     of the same provider).
--   * provider_connections: one per person and provider. Tokens sealed
--     (bound to the connection's ID and the token kind); needs_reconsent
--     erases them but keeps the row, scopes and label.
--   * provider_connect_requests: consent links made by calls that need a
--     connection (JSON-RPC -32042). Only a SHA-256 digest of the link's ID
--     is a key; the ID itself is sealed so a repeated call can reuse the
--     same link. Bound to one person, 10 minutes, single use.
--   * provider_auth_states: the state of an authorization in progress
--     (digest of a 256-bit value), bound to the browser session, person,
--     provider and request; the PKCE verifier sealed. Single use, 10 minutes.
--   * mcp_calls: the provider a call's capability required (provider_slug),
--     and the outcome needs_authorization.
--
-- New tables, nullable or defaulted columns and a relaxed check
-- (expand/contract): the previous release keeps working, except that its
-- call log can't show the new outcome.

-- +goose Up
CREATE TABLE auth_providers (
    id                     uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    -- Unique and immutable: sent to MCP servers in MCP-Gateway-Provider and
    -- the identity token's provider claim.
    slug                   text        NOT NULL CONSTRAINT auth_providers_slug_key UNIQUE
        CHECK (slug ~ '^[a-z0-9](?:[a-z0-9-]{0,46}[a-z0-9])?$'),
    name                   text        NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100),
    kind                   text        NOT NULL DEFAULT 'oauth2' CHECK (kind IN ('oauth2')),
    preset                 text        CHECK (preset IN ('github', 'google', 'microsoft', 'atlassian', 'slack', 'gitlab')),
    enabled                boolean     NOT NULL DEFAULT true,
    client_id              text        NOT NULL CHECK (char_length(client_id) BETWEEN 1 AND 500),
    client_secret_cipher   bytea,
    client_secret_hint     text        NOT NULL DEFAULT '' CHECK (char_length(client_secret_hint) <= 4),
    token_auth_method      text        NOT NULL DEFAULT 'client_secret_post'
        CHECK (token_auth_method IN ('client_secret_basic', 'client_secret_post', 'none')),
    pkce                   boolean     NOT NULL DEFAULT true,
    authorization_url      text        NOT NULL CHECK (authorization_url ~ '^https?://' AND char_length(authorization_url) <= 500),
    token_url              text        NOT NULL CHECK (token_url ~ '^https?://' AND char_length(token_url) <= 500),
    revocation_url         text        CHECK (revocation_url ~ '^https?://' AND char_length(revocation_url) <= 500),
    userinfo_url           text        CHECK (userinfo_url ~ '^https?://' AND char_length(userinfo_url) <= 500),
    default_scopes         text[]      NOT NULL DEFAULT '{}' CHECK (cardinality(default_scopes) <= 50),
    scope_param            text        NOT NULL DEFAULT 'scope' CHECK (scope_param ~ '^[A-Za-z0-9_.-]{1,64}$'),
    scope_separator        text        NOT NULL DEFAULT ' ' CHECK (scope_separator IN (' ', ',')),
    token_response_format  text        NOT NULL DEFAULT 'standard' CHECK (token_response_format IN ('standard', 'slack_authed_user')),
    extra_authorize_params jsonb       NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(extra_authorize_params) = 'object'),
    logo_url               text        CHECK (char_length(logo_url) <= 500),
    revision               bigint      NOT NULL DEFAULT 1,
    created_by             uuid        REFERENCES users (id) ON DELETE SET NULL,
    created_at             timestamptz NOT NULL DEFAULT now(),
    updated_by             uuid        REFERENCES users (id) ON DELETE SET NULL,
    updated_at             timestamptz NOT NULL DEFAULT now(),
    -- A confidential client has a secret; a public one (none) uses PKCE.
    CONSTRAINT auth_providers_secret CHECK (token_auth_method = 'none' OR client_secret_cipher IS NOT NULL),
    CONSTRAINT auth_providers_public_pkce CHECK (token_auth_method <> 'none' OR pkce)
);
CREATE INDEX auth_providers_name_idx ON auth_providers (name, id);

ALTER TABLE mcp_servers
    ADD COLUMN auth_provider_id uuid CONSTRAINT mcp_servers_auth_provider_fkey REFERENCES auth_providers (id) ON DELETE RESTRICT,
    ADD COLUMN auth_scopes      text[] NOT NULL DEFAULT '{}' CHECK (cardinality(auth_scopes) <= 50);
ALTER TABLE mcp_servers ADD CONSTRAINT mcp_servers_auth_scopes_need_provider
    CHECK (auth_provider_id IS NOT NULL OR cardinality(auth_scopes) = 0);
CREATE INDEX mcp_servers_auth_provider_idx ON mcp_servers (auth_provider_id) WHERE auth_provider_id IS NOT NULL;

ALTER TABLE mcp_capabilities
    ADD COLUMN requirement_mode   text   NOT NULL DEFAULT 'inherit' CHECK (requirement_mode IN ('inherit', 'none', 'scopes')),
    ADD COLUMN requirement_scopes text[] NOT NULL DEFAULT '{}' CHECK (cardinality(requirement_scopes) <= 50);
ALTER TABLE mcp_capabilities ADD CONSTRAINT mcp_capabilities_requirement_scopes
    CHECK ((requirement_mode = 'scopes') = (cardinality(requirement_scopes) > 0));

CREATE TABLE provider_connections (
    id                   uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id              uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    provider_id          uuid        NOT NULL REFERENCES auth_providers (id) ON DELETE CASCADE,
    status               text        NOT NULL CHECK (status IN ('active', 'needs_reconsent')),
    -- Granted scopes: as the provider reported them, else as asked for.
    scopes               text[]      NOT NULL DEFAULT '{}',
    -- The provider account as its userinfo endpoint named it (informational).
    account_label        text        CHECK (char_length(account_label) <= 200),
    -- Sealed with ENCRYPTION_KEY, bound to the row's ID and the token kind.
    access_token_cipher  bytea,
    refresh_token_cipher bytea,
    expires_at           timestamptz,
    connected_at         timestamptz NOT NULL DEFAULT now(),
    refreshed_at         timestamptz,
    -- Updated at most once a minute.
    last_used_at         timestamptz,
    last_error_class     text        CHECK (char_length(last_error_class) <= 64),
    CONSTRAINT provider_connections_user_provider_key UNIQUE (user_id, provider_id),
    CONSTRAINT provider_connections_tokens CHECK ((status = 'active') = (access_token_cipher IS NOT NULL)),
    CONSTRAINT provider_connections_refresh CHECK (access_token_cipher IS NOT NULL OR refresh_token_cipher IS NULL)
);
CREATE INDEX provider_connections_provider_idx ON provider_connections (provider_id);

CREATE TABLE provider_connect_requests (
    -- base64url SHA-256 of the link's 32-byte random ID.
    id_digest        text        PRIMARY KEY CHECK (id_digest ~ '^[A-Za-z0-9_-]{43}$'),
    -- The ID sealed with ENCRYPTION_KEY (bound to the digest), so a repeated
    -- call can answer with the same link.
    id_cipher        bytea       NOT NULL,
    user_id          uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    provider_id      uuid        NOT NULL REFERENCES auth_providers (id) ON DELETE CASCADE,
    required_scopes  text[]      NOT NULL,
    reason           text        NOT NULL CHECK (reason IN ('not_connected', 'missing_scopes', 'needs_reconsent')),
    -- The gateway and capability as they were (no foreign keys).
    gateway_id       uuid        NOT NULL,
    gateway_slug     text        NOT NULL,
    gateway_name     text        NOT NULL,
    capability_id    uuid        NOT NULL,
    capability_kind  text        NOT NULL,
    exposed_name     text        NOT NULL CHECK (char_length(exposed_name) <= 1100),
    capability_title text        NOT NULL DEFAULT '' CHECK (char_length(capability_title) <= 200),
    created_at       timestamptz NOT NULL DEFAULT now(),
    expires_at       timestamptz NOT NULL,
    used_at          timestamptz
);
CREATE INDEX provider_connect_requests_user_idx ON provider_connect_requests (user_id, created_at);
CREATE INDEX provider_connect_requests_expires_idx ON provider_connect_requests (expires_at);

CREATE TABLE provider_auth_states (
    -- hex SHA-256 of the state (256 random bits).
    state_digest          text        PRIMARY KEY CHECK (state_digest ~ '^[0-9a-f]{64}$'),
    user_id               uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    -- hex SHA-256 of the browser session that started connecting.
    session_digest        text        NOT NULL CHECK (session_digest ~ '^[0-9a-f]{64}$'),
    provider_id           uuid        NOT NULL REFERENCES auth_providers (id) ON DELETE CASCADE,
    -- Everything asked for, and what had to be granted (the request's or the
    -- caller's scopes).
    scopes                text[]      NOT NULL,
    required_scopes       text[]      NOT NULL DEFAULT '{}',
    code_verifier_cipher  bytea,
    request_digest        text,
    return_to             text        CHECK (char_length(return_to) <= 2000),
    created_at            timestamptz NOT NULL DEFAULT now(),
    expires_at            timestamptz NOT NULL
);
CREATE INDEX provider_auth_states_expires_idx ON provider_auth_states (expires_at);

ALTER TABLE mcp_calls ADD COLUMN provider_slug text CHECK (char_length(provider_slug) <= 48);
ALTER TABLE mcp_calls DROP CONSTRAINT mcp_calls_outcome_check;
ALTER TABLE mcp_calls ADD CONSTRAINT mcp_calls_outcome_check
    CHECK (outcome IN ('ok', 'tool_error', 'denied', 'upstream_error', 'timeout', 'too_large', 'bad_request',
                       'needs_authorization'));

-- +goose Down
-- Call records are append-only: rows with the new outcome are kept, and
-- only new rows are checked.
ALTER TABLE mcp_calls DROP CONSTRAINT mcp_calls_outcome_check;
ALTER TABLE mcp_calls ADD CONSTRAINT mcp_calls_outcome_check
    CHECK (outcome IN ('ok', 'tool_error', 'denied', 'upstream_error', 'timeout', 'too_large', 'bad_request')) NOT VALID;
ALTER TABLE mcp_calls DROP COLUMN provider_slug;
DROP TABLE provider_auth_states;
DROP TABLE provider_connect_requests;
DROP TABLE provider_connections;
ALTER TABLE mcp_capabilities DROP CONSTRAINT mcp_capabilities_requirement_scopes;
ALTER TABLE mcp_capabilities DROP COLUMN requirement_mode, DROP COLUMN requirement_scopes;
DROP INDEX mcp_servers_auth_provider_idx;
ALTER TABLE mcp_servers DROP CONSTRAINT mcp_servers_auth_scopes_need_provider;
ALTER TABLE mcp_servers DROP COLUMN auth_provider_id, DROP COLUMN auth_scopes;
DROP TABLE auth_providers;
