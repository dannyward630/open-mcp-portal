-- Phase 7: per-person OAuth to MCP servers that are OAuth resource servers
-- themselves (docs/phase7-upstream-oauth.md; decisions 39-45):
--
--   * mcp_servers.auth_mode: how the gateway authenticates to the server
--     (none, static_header, oauth), backfilled from the static header. In
--     oauth mode each person's own token goes in Authorization: Bearer, so
--     the server can't also have a static Authorization header or a Phase 5
--     requirement (one credential per call).
--   * mcp_servers.oauth_setup_user_id/oauth_setup_at: the person whose
--     sign-in (their connection to the server-bound provider) the gateway
--     reads capabilities with and tests the server with (decision 42).
--   * mcp_servers.send_identity: send the identity JWT (decision 45, default
--     on); off, no identity token is sent to the server at all.
--   * auth_providers kind mcp_server: the OAuth client of one oauth server
--     (server_id), with the discovered metadata (resource, authorization
--     server, issuer, scopes supported), how the client was obtained
--     (registration) and, for platform_provider, the platform provider whose
--     client it uses (link_provider_id; no secret of its own, and client_id
--     only records the linked client's ID at link time: calls always use the
--     linked provider's current client). Deleting the server deletes it, and with it every connection.
--   * provider_auth_states: the server an authorize-setup is for, and the
--     resource the flow started with (the callback refuses a changed one).
--   * mcp_health_checks.sign_in_required: a scheduled check of an oauth
--     server that got the expected 401 challenge (healthy, "requires sign-in").
--
-- Expand-only: new nullable or defaulted columns and relaxed checks; the
-- previous release keeps working (it sees mcp_server providers as providers
-- nobody requires).

-- +goose Up
ALTER TABLE mcp_servers
    ADD COLUMN auth_mode           text        NOT NULL DEFAULT 'none' CHECK (auth_mode IN ('none', 'static_header', 'oauth')),
    ADD COLUMN oauth_setup_user_id uuid        REFERENCES users (id) ON DELETE SET NULL,
    ADD COLUMN oauth_setup_at      timestamptz,
    ADD COLUMN send_identity       boolean     NOT NULL DEFAULT true;
UPDATE mcp_servers SET auth_mode = 'static_header' WHERE auth_header_name IS NOT NULL;
ALTER TABLE mcp_servers ADD CONSTRAINT mcp_servers_oauth_single_credential
    CHECK (auth_mode <> 'oauth' OR (auth_provider_id IS NULL AND (auth_header_name IS NULL OR lower(auth_header_name) <> 'authorization')));

ALTER TABLE auth_providers DROP CONSTRAINT auth_providers_kind_check;
ALTER TABLE auth_providers ADD CONSTRAINT auth_providers_kind_check CHECK (kind IN ('oauth2', 'mcp_server'));
ALTER TABLE auth_providers
    ADD COLUMN server_id                uuid        CONSTRAINT auth_providers_server_key UNIQUE
        CONSTRAINT auth_providers_server_fkey REFERENCES mcp_servers (id) ON DELETE CASCADE,
    ADD COLUMN link_provider_id         uuid        CONSTRAINT auth_providers_link_fkey REFERENCES auth_providers (id) ON DELETE RESTRICT,
    ADD COLUMN registration             text        CHECK (registration IN ('dcr', 'cimd', 'manual', 'platform_provider')),
    ADD COLUMN resource                 text        CHECK (char_length(resource) <= 500),
    ADD COLUMN resource_metadata_url    text        CHECK (char_length(resource_metadata_url) <= 1000),
    ADD COLUMN authorization_server     text        CHECK (char_length(authorization_server) <= 500),
    ADD COLUMN issuer                   text        CHECK (char_length(issuer) <= 500),
    ADD COLUMN scopes_supported         text[]      NOT NULL DEFAULT '{}' CHECK (cardinality(scopes_supported) <= 200),
    ADD COLUMN send_resource            boolean     NOT NULL DEFAULT true,
    ADD COLUMN iss_param_supported      boolean     NOT NULL DEFAULT false,
    ADD COLUMN client_secret_expires_at timestamptz,
    ADD COLUMN discovered_at            timestamptz;
ALTER TABLE auth_providers ADD CONSTRAINT auth_providers_server_kind CHECK ((kind = 'mcp_server') = (server_id IS NOT NULL));
ALTER TABLE auth_providers ADD CONSTRAINT auth_providers_server_fields
    CHECK (kind = 'mcp_server' OR (registration IS NULL AND link_provider_id IS NULL));
ALTER TABLE auth_providers ADD CONSTRAINT auth_providers_server_metadata
    CHECK (kind <> 'mcp_server' OR (registration IS NOT NULL AND resource IS NOT NULL AND issuer IS NOT NULL));
ALTER TABLE auth_providers ADD CONSTRAINT auth_providers_link
    CHECK ((registration = 'platform_provider') = (link_provider_id IS NOT NULL));
-- A linked server uses the platform provider's client and secret.
ALTER TABLE auth_providers DROP CONSTRAINT auth_providers_secret;
ALTER TABLE auth_providers ADD CONSTRAINT auth_providers_secret
    CHECK (token_auth_method = 'none' OR client_secret_cipher IS NOT NULL OR link_provider_id IS NOT NULL);
CREATE INDEX auth_providers_link_idx ON auth_providers (link_provider_id) WHERE link_provider_id IS NOT NULL;

ALTER TABLE provider_auth_states
    ADD COLUMN setup_server_id uuid REFERENCES mcp_servers (id) ON DELETE CASCADE,
    ADD COLUMN resource        text CHECK (char_length(resource) <= 500);

ALTER TABLE mcp_health_checks ADD COLUMN sign_in_required boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE mcp_health_checks DROP COLUMN sign_in_required;
ALTER TABLE provider_auth_states DROP COLUMN setup_server_id, DROP COLUMN resource;
-- Server-bound providers (and their connections) go with the columns that
-- describe them.
DELETE FROM auth_providers WHERE kind = 'mcp_server';
DROP INDEX auth_providers_link_idx;
ALTER TABLE auth_providers DROP CONSTRAINT auth_providers_secret;
ALTER TABLE auth_providers ADD CONSTRAINT auth_providers_secret CHECK (token_auth_method = 'none' OR client_secret_cipher IS NOT NULL);
ALTER TABLE auth_providers DROP CONSTRAINT auth_providers_link,
    DROP CONSTRAINT auth_providers_server_metadata, DROP CONSTRAINT auth_providers_server_fields,
    DROP CONSTRAINT auth_providers_server_kind;
ALTER TABLE auth_providers DROP COLUMN server_id, DROP COLUMN link_provider_id, DROP COLUMN registration, DROP COLUMN resource,
    DROP COLUMN resource_metadata_url, DROP COLUMN authorization_server, DROP COLUMN issuer, DROP COLUMN scopes_supported,
    DROP COLUMN send_resource, DROP COLUMN iss_param_supported, DROP COLUMN client_secret_expires_at, DROP COLUMN discovered_at;
ALTER TABLE auth_providers DROP CONSTRAINT auth_providers_kind_check;
ALTER TABLE auth_providers ADD CONSTRAINT auth_providers_kind_check CHECK (kind IN ('oauth2'));
ALTER TABLE mcp_servers DROP CONSTRAINT mcp_servers_oauth_single_credential;
ALTER TABLE mcp_servers DROP COLUMN auth_mode, DROP COLUMN oauth_setup_user_id, DROP COLUMN oauth_setup_at, DROP COLUMN send_identity;
