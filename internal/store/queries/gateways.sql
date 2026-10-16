-- Gateways, their selected capabilities and grants (internal/gateways).

-- name: InsertGateway :one
INSERT INTO gateways (id, slug, name, description, owner_kind, team_id, enabled, created_by, updated_by)
VALUES (@id, @slug, @name, @description, @owner_kind, sqlc.narg(team_id), @enabled, @created_by, @created_by)
RETURNING *;

-- name: GetGateway :one
SELECT * FROM gateways WHERE id = $1;

-- name: GetGatewayBySlug :one
SELECT * FROM gateways WHERE slug = $1;

-- name: LockGateway :one
SELECT * FROM gateways WHERE id = $1 FOR UPDATE;

-- name: UpdateGateway :one
UPDATE gateways
SET name = @name, description = @description, enabled = @enabled,
    revision = revision + 1, updated_by = @updated_by, updated_at = now()
WHERE id = @id
RETURNING *;

-- name: BumpGatewayRevision :one
UPDATE gateways SET revision = revision + 1, updated_by = @updated_by, updated_at = now()
WHERE id = @id
RETURNING *;

-- name: DeleteGateway :exec
DELETE FROM gateways WHERE id = $1;

-- name: ListGateways :many
-- One page of gateways ordered by (name, id), with a team gateway's team.
-- search is LIKE-escaped by the caller.
SELECT sqlc.embed(g), COALESCE(t.slug, '')::text AS team_slug, COALESCE(t.name, '')::text AS team_name
FROM gateways g
LEFT JOIN teams t ON t.id = g.team_id
WHERE (sqlc.narg(team_id)::uuid IS NULL OR g.team_id = sqlc.narg(team_id)::uuid)
  AND (sqlc.narg(owner_kind)::text IS NULL OR g.owner_kind = sqlc.narg(owner_kind)::text)
  AND (sqlc.narg(search)::text IS NULL
       OR g.name ILIKE '%' || sqlc.narg(search)::text || '%' ESCAPE '\'
       OR g.slug ILIKE '%' || sqlc.narg(search)::text || '%' ESCAPE '\')
  AND (sqlc.narg(after_name)::text IS NULL OR (g.name, g.id) > (sqlc.narg(after_name)::text, sqlc.narg(after_id)::uuid))
ORDER BY g.name, g.id
LIMIT @page_size;

-- name: GetGatewayView :one
SELECT sqlc.embed(g), COALESCE(t.slug, '')::text AS team_slug, COALESCE(t.name, '')::text AS team_name
FROM gateways g
LEFT JOIN teams t ON t.id = g.team_id
WHERE g.id = $1;

-- name: GatewayStats :many
-- Per gateway: selected capabilities, the ones not served right now, and
-- grants. "Served" mirrors gateways.Availability: listed, its server
-- enabled, and either the team gateway's own server or published with the
-- current definition approved.
SELECT g.id AS gateway_id,
       (SELECT count(*) FROM gateway_capabilities gc WHERE gc.gateway_id = g.id)::bigint AS capability_count,
       (SELECT count(*)
        FROM gateway_capabilities gc
        JOIN mcp_capabilities c ON c.id = gc.capability_id
        JOIN mcp_servers s ON s.id = c.server_id
        WHERE gc.gateway_id = g.id
          AND NOT COALESCE(c.gone_at IS NULL AND s.enabled AND c.kind IN ('tool', 'prompt')
                           AND ((g.owner_kind = 'team' AND s.team_id = g.team_id)
                                OR (s.publish_state = 'published' AND c.approved_fingerprint = c.fingerprint)), false)
       )::bigint AS unavailable_count,
       (SELECT count(*) FROM gateway_grants gg WHERE gg.gateway_id = g.id)::bigint AS grant_count
FROM gateways g
WHERE g.id = ANY(@ids::uuid[]);

-- name: ListGatewaySelection :many
-- A gateway's selected capabilities with their servers and the servers'
-- teams, ordered by server and name (tools before prompts).
SELECT sqlc.embed(c), sqlc.embed(s), gc.min_team_role, t.slug AS team_slug, t.name AS team_name
FROM gateway_capabilities gc
JOIN mcp_capabilities c ON c.id = gc.capability_id
JOIN mcp_servers s ON s.id = c.server_id
JOIN teams t ON t.id = s.team_id
WHERE gc.gateway_id = @gateway_id
ORDER BY s.name, s.id, CASE c.kind WHEN 'tool' THEN 0 ELSE 1 END, c.name;

-- name: RemoveGatewayCapabilitiesExcept :exec
DELETE FROM gateway_capabilities
WHERE gateway_id = @gateway_id AND NOT (capability_id = ANY(@keep::uuid[]));

-- name: UpsertGatewayCapability :exec
INSERT INTO gateway_capabilities (gateway_id, capability_id, min_team_role, added_by)
VALUES (@gateway_id, @capability_id, sqlc.narg(min_team_role), @added_by)
ON CONFLICT (gateway_id, capability_id) DO UPDATE SET min_team_role = EXCLUDED.min_team_role;

-- name: ListGatewayOptions :many
-- Tools and prompts a gateway may select: listed, of an enabled server, and
-- either a server of team_id (a team gateway's own team; NULL for a
-- platform gateway) or published with the current definition approved.
-- by_ids narrows to ids; search is LIKE-escaped by the caller.
SELECT c.id, c.kind, c.name, c.title, c.description,
       s.id AS server_id, s.slug AS server_slug, s.name AS server_name, s.team_id AS server_team_id,
       t.slug AS team_slug, t.name AS team_name
FROM mcp_capabilities c
JOIN mcp_servers s ON s.id = c.server_id
JOIN teams t ON t.id = s.team_id
WHERE c.kind IN ('tool', 'prompt') AND c.gone_at IS NULL AND s.enabled
  AND ((sqlc.narg(team_id)::uuid IS NOT NULL AND s.team_id = sqlc.narg(team_id)::uuid)
       OR (s.publish_state = 'published' AND c.approved_fingerprint = c.fingerprint))
  AND (sqlc.narg(kind)::text IS NULL OR c.kind = sqlc.narg(kind)::text)
  AND (NOT @by_ids::bool OR c.id = ANY(@ids::uuid[]))
  AND (sqlc.narg(search)::text IS NULL
       OR c.name ILIKE '%' || sqlc.narg(search)::text || '%' ESCAPE '\'
       OR c.title ILIKE '%' || sqlc.narg(search)::text || '%' ESCAPE '\'
       OR s.name ILIKE '%' || sqlc.narg(search)::text || '%' ESCAPE '\'
       OR s.slug ILIKE '%' || sqlc.narg(search)::text || '%' ESCAPE '\')
ORDER BY s.name, s.id, CASE c.kind WHEN 'tool' THEN 0 ELSE 1 END, c.name
LIMIT @page_size;

-- name: ListGatewayGrants :many
SELECT sqlc.embed(gg),
       COALESCE(t.slug, '')::text AS team_slug, COALESCE(t.name, '')::text AS team_name,
       COALESCE(u.email::text, '')::text AS user_email, COALESCE(u.display_name, '')::text AS user_display_name,
       COALESCE(u.status, '')::text AS user_status,
       COALESCE((SELECT COALESCE(NULLIF(a.display_name, ''), a.email::text) FROM users a WHERE a.id = gg.added_by), '')::text
           AS added_by_name
FROM gateway_grants gg
LEFT JOIN teams t ON t.id = gg.team_id
LEFT JOIN users u ON u.id = gg.user_id
WHERE gg.gateway_id = @gateway_id
ORDER BY gg.added_at, gg.id;

-- name: InsertGatewayGrant :one
INSERT INTO gateway_grants (gateway_id, subject_kind, team_id, user_id, group_name, added_by)
VALUES (@gateway_id, @subject_kind, sqlc.narg(team_id), sqlc.narg(user_id), sqlc.narg(group_name), @added_by)
RETURNING *;

-- name: DeleteGatewayGrant :one
DELETE FROM gateway_grants WHERE id = @id AND gateway_id = @gateway_id
RETURNING *;

-- name: GatewayGrantKindsForUser :many
-- How a person reaches a gateway through its grants: their team's grant,
-- their own, or one of their groups'.
SELECT DISTINCT gg.subject_kind
FROM gateway_grants gg
WHERE gg.gateway_id = @gateway_id
  AND (gg.user_id = @user_id
       OR gg.team_id IN (SELECT m.team_id FROM team_members m WHERE m.user_id = @user_id)
       OR gg.group_name = ANY(@groups::text[]));

-- name: ListGatewaysForUser :many
-- Enabled gateways a person may use: their teams' gateways (with their
-- role) and platform gateways granted to them, their teams or their groups.
SELECT sqlc.embed(g), COALESCE(t.slug, '')::text AS team_slug, COALESCE(t.name, '')::text AS team_name,
       COALESCE(m.role, '')::text AS member_role,
       ARRAY(SELECT DISTINCT gg.subject_kind FROM gateway_grants gg
             WHERE gg.gateway_id = g.id
               AND (gg.user_id = @user_id
                    OR gg.team_id IN (SELECT tm.team_id FROM team_members tm WHERE tm.user_id = @user_id)
                    OR gg.group_name = ANY(@groups::text[])))::text[] AS grant_kinds
FROM gateways g
LEFT JOIN teams t ON t.id = g.team_id
LEFT JOIN team_members m ON g.owner_kind = 'team' AND m.team_id = g.team_id AND m.user_id = @user_id
WHERE g.enabled
  AND ((g.owner_kind = 'team' AND m.user_id IS NOT NULL)
       OR (g.owner_kind = 'platform' AND EXISTS (
            SELECT 1 FROM gateway_grants gg
            WHERE gg.gateway_id = g.id
              AND (gg.user_id = @user_id
                   OR gg.team_id IN (SELECT tm.team_id FROM team_members tm WHERE tm.user_id = @user_id)
                   OR gg.group_name = ANY(@groups::text[])))))
ORDER BY g.name, g.id;
