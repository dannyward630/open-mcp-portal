-- Automation tokens (internal/automation; docs/automation-tokens.md).

-- name: LockPlatformAutomationTokens :exec
-- Serialises creating platform tokens, so the limit of 20 holds.
SELECT pg_advisory_xact_lock(hashtextextended('omp.automation-tokens.platform', 0));

-- name: CountActiveTeamAutomationTokens :one
SELECT count(*) FROM automation_tokens
WHERE team_id = @team_id AND revoked_at IS NULL AND expires_at > now();

-- name: CountActivePlatformAutomationTokens :one
SELECT count(*) FROM automation_tokens
WHERE kind = 'platform_admin' AND revoked_at IS NULL AND expires_at > now();

-- name: InsertAutomationToken :one
-- The expiry is computed from the database clock, so it satisfies the
-- table's check against created_at whatever the application's clock says.
INSERT INTO automation_tokens (kind, team_id, name, prefix, digest, pepper_id, scopes, created_by, created_by_name, expires_at)
VALUES (@kind, @team_id, @name, @prefix, @digest, @pepper_id, @scopes, @created_by, @created_by_name,
        now() + make_interval(days => @days::int))
RETURNING *;

-- name: GetAutomationTokenByDigest :one
-- A token with what authentication needs: its team (slug, status) and
-- whether its creator is still an active platform admin.
SELECT sqlc.embed(t),
       COALESCE(tm.slug, '')::text AS team_slug, COALESCE(tm.name, '')::text AS team_name, COALESCE(tm.status, '')::text AS team_status,
       (u.id IS NOT NULL AND u.status = 'active' AND u.platform_role = 'platform_admin')::bool AS creator_is_admin
FROM automation_tokens t
LEFT JOIN teams tm ON tm.id = t.team_id
LEFT JOIN users u ON u.id = t.created_by
WHERE t.digest = @digest;

-- name: GetAutomationToken :one
SELECT sqlc.embed(t),
       COALESCE(tm.slug, '')::text AS team_slug, COALESCE(tm.name, '')::text AS team_name, COALESCE(tm.status, '')::text AS team_status,
       (u.id IS NOT NULL AND u.status = 'active' AND u.platform_role = 'platform_admin')::bool AS creator_is_admin
FROM automation_tokens t
LEFT JOIN teams tm ON tm.id = t.team_id
LEFT JOIN users u ON u.id = t.created_by
WHERE t.id = @id;

-- name: ListAutomationTokens :many
-- Newest first: the platform's tokens (team_id NULL) or one team's; revoked
-- and expired ones for 30 days after they ended.
SELECT sqlc.embed(t),
       COALESCE(tm.slug, '')::text AS team_slug, COALESCE(tm.name, '')::text AS team_name, COALESCE(tm.status, '')::text AS team_status,
       (u.id IS NOT NULL AND u.status = 'active' AND u.platform_role = 'platform_admin')::bool AS creator_is_admin
FROM automation_tokens t
LEFT JOIN teams tm ON tm.id = t.team_id
LEFT JOIN users u ON u.id = t.created_by
WHERE ((sqlc.narg(team_id)::uuid IS NULL AND t.kind = 'platform_admin') OR t.team_id = sqlc.narg(team_id)::uuid)
  AND (t.revoked_at IS NULL OR t.revoked_at > now() - interval '30 days')
  AND t.expires_at > now() - interval '30 days'
ORDER BY t.created_at DESC, t.id DESC
LIMIT 200;

-- name: RevokeAutomationToken :one
UPDATE automation_tokens SET revoked_at = now(), revoked_by = @revoked_by
WHERE id = @id AND revoked_at IS NULL
RETURNING *;

-- name: TouchAutomationToken :exec
-- At most once a minute.
UPDATE automation_tokens SET last_used_at = now()
WHERE id = @id AND (last_used_at IS NULL OR last_used_at < now() - interval '1 minute');

-- name: RehashAutomationToken :execrows
UPDATE automation_tokens SET digest = @digest, pepper_id = @pepper_id WHERE id = @id AND digest = @old_digest;

-- name: DeleteOldAutomationTokens :execrows
-- Tokens that expired or were revoked more than 30 days ago.
DELETE FROM automation_tokens
WHERE expires_at < now() - interval '30 days' OR revoked_at < now() - interval '30 days';
