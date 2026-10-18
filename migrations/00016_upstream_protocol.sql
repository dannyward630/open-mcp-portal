-- MCP 2026-07-28 as the client of your servers (docs/mcp-protocol.md, "The
-- portal as a client"): the protocol era the portal last negotiated with each
-- server, shown on the server page.
--
--   * protocol_era: 'legacy' (initialize, 2025-11-25 and earlier) or 'modern'
--     (stateless, 2026-07-28 and later).
--   * protocol_version: the version in use (for example 2025-11-25).
--   * protocol_negotiated_at: when a refresh, a test or a health check last
--     succeeded in negotiating it.
--
-- Expand-only: three nullable columns. NULL until the first successful
-- exchange; the previous release ignores them.

-- +goose Up
ALTER TABLE mcp_servers
    ADD COLUMN protocol_era text CHECK (protocol_era IN ('legacy', 'modern')),
    ADD COLUMN protocol_version text CHECK (protocol_version ~ '^\d{4}-\d{2}-\d{2}$'),
    ADD COLUMN protocol_negotiated_at timestamptz;

-- +goose Down
ALTER TABLE mcp_servers
    DROP COLUMN protocol_negotiated_at,
    DROP COLUMN protocol_version,
    DROP COLUMN protocol_era;
