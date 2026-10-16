-- Call records of /mcp/{slug} (internal/calls). Metadata only.

-- name: InsertMCPCall :exec
INSERT INTO mcp_calls (
    request_id, method, exposed_name, gateway_id, gateway_slug, gateway_name, gateway_team_id,
    server_id, server_slug, server_name, server_team_id, capability_id, principal_kind, user_id, api_key_id,
    key_name, service_account_id, service_account_name, service_account_team_id, oauth_grant_id, oauth_client_name,
    client_name, outcome, error_class, latency_ms, request_bytes, response_bytes
) VALUES (
    @request_id, @method, @exposed_name, @gateway_id, @gateway_slug, @gateway_name, @gateway_team_id,
    @server_id, @server_slug, @server_name, @server_team_id, @capability_id, @principal_kind, @user_id, @api_key_id,
    @key_name, @service_account_id, @service_account_name, @service_account_team_id, @oauth_grant_id, @oauth_client_name,
    @client_name, @outcome, @error_class, @latency_ms, @request_bytes, @response_bytes
);

-- name: ListMCPCalls :many
-- One page of calls, newest first. team_id: calls through the team's
-- gateways or to its servers. Gateway, server, key and service account
-- names are the current ones while they exist, else the ones recorded.
SELECT c.id, c.occurred_at, c.request_id, c.method, c.exposed_name, c.gateway_id, c.server_id,
       c.principal_kind, c.user_id, c.client_name, c.outcome, c.error_class, c.latency_ms,
       c.request_bytes, c.response_bytes, c.service_account_id, c.oauth_client_name,
       (g.id IS NOT NULL)::bool AS gateway_exists,
       COALESCE(g.slug, c.gateway_slug)::text AS gateway_slug, COALESCE(g.name, c.gateway_name)::text AS gateway_name,
       (s.id IS NOT NULL)::bool AS server_exists,
       COALESCE(s.slug, c.server_slug, '')::text AS server_slug, COALESCE(s.name, c.server_name, '')::text AS server_name,
       COALESCE(u.email::text, '')::text AS user_email,
       COALESCE(u.display_name, sa.name, c.service_account_name, '')::text AS user_display_name,
       COALESCE(k.name, sk.name, c.key_name, '')::text AS api_key_name,
       COALESCE(st.slug, '')::text AS service_account_team_slug
FROM mcp_calls c
LEFT JOIN gateways g ON g.id = c.gateway_id
LEFT JOIN mcp_servers s ON s.id = c.server_id
LEFT JOIN users u ON u.id = c.user_id
LEFT JOIN api_keys k ON k.id = c.api_key_id AND c.principal_kind = 'api_key'
LEFT JOIN service_account_keys sk ON sk.id = c.api_key_id AND c.principal_kind = 'service_account'
LEFT JOIN service_accounts sa ON sa.id = c.service_account_id
LEFT JOIN teams st ON st.id = c.service_account_team_id
WHERE (sqlc.narg(team_id)::uuid IS NULL
       OR c.gateway_team_id = sqlc.narg(team_id)::uuid OR c.server_team_id = sqlc.narg(team_id)::uuid)
  AND (sqlc.narg(gateway_id)::uuid IS NULL OR c.gateway_id = sqlc.narg(gateway_id)::uuid)
  AND (sqlc.narg(server_id)::uuid IS NULL OR c.server_id = sqlc.narg(server_id)::uuid)
  AND (sqlc.narg(user_id)::uuid IS NULL OR c.user_id = sqlc.narg(user_id)::uuid)
  AND (sqlc.narg(service_account_id)::uuid IS NULL OR c.service_account_id = sqlc.narg(service_account_id)::uuid)
  AND (sqlc.narg(outcome)::text IS NULL OR c.outcome = sqlc.narg(outcome)::text)
  AND (sqlc.narg(occurred_from)::timestamptz IS NULL OR c.occurred_at >= sqlc.narg(occurred_from)::timestamptz)
  AND (sqlc.narg(occurred_to)::timestamptz IS NULL OR c.occurred_at < sqlc.narg(occurred_to)::timestamptz)
  AND (sqlc.narg(before_id)::bigint IS NULL OR c.id < sqlc.narg(before_id)::bigint)
ORDER BY c.id DESC
LIMIT @page_size;
