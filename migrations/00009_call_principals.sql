-- Phase 4 follow-ups (docs/phase4-client-auth.md "As built"):
--
--   * mcp_calls.oauth_client_kind: for calls of a connected app, how the
--     client identified itself as it was: metadata (a Client ID Metadata
--     Document, its host vouches for it) or registered (it registered
--     itself, so its name is unverified). Older rows have none; the call log
--     then reads it from the grant while that exists.
--   * An index for the team call list, which now also lists the calls of the
--     team's service accounts through any gateway.
--
-- A nullable column and an index only (expand/contract): the previous release
-- keeps working and doesn't write the column.

-- +goose Up
ALTER TABLE mcp_calls
    ADD COLUMN oauth_client_kind text CHECK (oauth_client_kind IN ('metadata', 'registered'));
CREATE INDEX mcp_calls_service_account_team_idx ON mcp_calls (service_account_team_id, id DESC)
    WHERE service_account_team_id IS NOT NULL;

-- +goose Down
DROP INDEX mcp_calls_service_account_team_idx;
ALTER TABLE mcp_calls DROP COLUMN oauth_client_kind;
