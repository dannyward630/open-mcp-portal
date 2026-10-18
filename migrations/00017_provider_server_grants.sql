-- F-3 (docs/threat-model.md section 8, decision 60): a person's connection
-- to a platform provider (GitHub, ...) is sent to a server only when the
-- person allowed that server to use it, at the origin they allowed:
--
--   * provider_server_grants: one per (connection, server). origin is the
--     server URL's origin (scheme, host, port; registry.OriginOf) when the
--     person consented, scopes what they consented to (what the server's
--     requirement asked for at the time). Deleted with the connection (a
--     disconnect, a provider or user delete) or the server; the registry
--     deletes a server's grants when its URL's origin or its provider
--     changes. A grant for another origin than the server has now is never
--     honoured. There is no backfill: every person consents once per server
--     after the upgrade, on purpose (a backfill would grandfather any
--     malicious server). oauth-mode (server-bound) providers have no grants:
--     their connection is already for one server.
--   * provider_connect_requests.server_id/server_origin: the server (and its
--     origin) a consent link was made for, so a link for one server can't
--     allow another, nor the same server after it moved. A request made
--     before this migration has none and is refused as expired (the person
--     runs the tool again); the columns stay nullable so the previous
--     release can still insert requests.
--   * the request reason server_not_allowed: connected, scopes covered, but
--     this server isn't allowed yet.
--
-- New table and nullable columns, a relaxed check (expand/contract).

-- +goose Up
CREATE TABLE provider_server_grants (
    id            uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    connection_id uuid        NOT NULL REFERENCES provider_connections (id) ON DELETE CASCADE,
    server_id     uuid        NOT NULL REFERENCES mcp_servers (id) ON DELETE CASCADE,
    origin        text        NOT NULL CHECK (char_length(origin) BETWEEN 1 AND 2100),
    scopes        text[]      NOT NULL DEFAULT '{}' CHECK (cardinality(scopes) <= 50),
    consented_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT provider_server_grants_connection_server_key UNIQUE (connection_id, server_id)
);
CREATE INDEX provider_server_grants_server_idx ON provider_server_grants (server_id);

ALTER TABLE provider_connect_requests
    ADD COLUMN server_id     uuid REFERENCES mcp_servers (id) ON DELETE CASCADE,
    ADD COLUMN server_origin text CHECK (char_length(server_origin) <= 2100),
    ADD CONSTRAINT provider_connect_requests_server_origin CHECK ((server_id IS NULL) = (server_origin IS NULL));
CREATE INDEX provider_connect_requests_server_idx ON provider_connect_requests (server_id) WHERE server_id IS NOT NULL;

ALTER TABLE provider_connect_requests DROP CONSTRAINT provider_connect_requests_reason_check;
ALTER TABLE provider_connect_requests ADD CONSTRAINT provider_connect_requests_reason_check
    CHECK (reason IN ('not_connected', 'missing_scopes', 'needs_reconsent', 'server_not_allowed'));

-- +goose Down
-- A request for the new reason has no meaning for the old schema: drop it.
DELETE FROM provider_connect_requests WHERE reason = 'server_not_allowed';
ALTER TABLE provider_connect_requests DROP CONSTRAINT provider_connect_requests_reason_check;
ALTER TABLE provider_connect_requests ADD CONSTRAINT provider_connect_requests_reason_check
    CHECK (reason IN ('not_connected', 'missing_scopes', 'needs_reconsent'));
DROP INDEX provider_connect_requests_server_idx;
ALTER TABLE provider_connect_requests
    DROP CONSTRAINT provider_connect_requests_server_origin,
    DROP COLUMN server_origin,
    DROP COLUMN server_id;
DROP TABLE provider_server_grants;
