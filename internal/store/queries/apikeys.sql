-- Personal API keys (internal/apikeys) and the identity signing keys
-- (internal/identity).

-- name: CountActiveAPIKeys :one
SELECT count(*)::bigint FROM api_keys WHERE user_id = $1 AND revoked_at IS NULL AND expires_at > now();

-- name: InsertAPIKey :one
INSERT INTO api_keys (user_id, name, prefix, digest, expires_at)
VALUES (@user_id, @name, @prefix, @digest, @expires_at)
RETURNING *;

-- name: ListAPIKeys :many
-- A person's keys, newest first: active ones, and those revoked or expired
-- within the last 30 days.
SELECT * FROM api_keys
WHERE user_id = $1
  AND LEAST(COALESCE(revoked_at, 'infinity'::timestamptz), expires_at) > now() - interval '30 days'
ORDER BY created_at DESC, id DESC;

-- name: GetUserAPIKey :one
SELECT * FROM api_keys WHERE id = @id AND user_id = @user_id;

-- name: RevokeAPIKey :one
UPDATE api_keys SET revoked_at = now()
WHERE id = @id AND user_id = @user_id AND revoked_at IS NULL
RETURNING *;

-- name: GetAPIKeyByDigest :one
SELECT sqlc.embed(k), sqlc.embed(u)
FROM api_keys k
JOIN users u ON u.id = k.user_id
WHERE k.digest = $1;

-- name: TouchAPIKey :exec
-- Records a use, at most once a minute.
UPDATE api_keys SET last_used_at = now()
WHERE id = $1 AND (last_used_at IS NULL OR last_used_at < now() - interval '1 minute');

-- name: LockSigningKeys :exec
-- Serialises key creation and rotation across replicas (transaction lock).
SELECT pg_advisory_xact_lock(hashtextextended('omg.signing-keys', 0));

-- name: GetActiveSigningKey :one
SELECT * FROM signing_keys WHERE state = 'active';

-- name: GetNextSigningKey :one
SELECT * FROM signing_keys WHERE state = 'next';

-- name: SigningKeyRotationState :one
-- Where rotation stands, by the database clock: whether the active key has
-- signed for prepare_after_seconds (time to pre-publish its successor) and
-- for rotate_after_seconds (time to replace it), and whether the next key
-- has been published for published_seconds (ready to sign).
SELECT
    EXISTS (SELECT 1 FROM signing_keys WHERE state = 'active'
            AND COALESCE(activated_at, created_at) <= now() - (sqlc.arg(prepare_after_seconds)::bigint * '1 second'::interval)
    )::bool AS prepare_due,
    EXISTS (SELECT 1 FROM signing_keys WHERE state = 'active'
            AND COALESCE(activated_at, created_at) <= now() - (sqlc.arg(rotate_after_seconds)::bigint * '1 second'::interval)
    )::bool AS rotate_due,
    EXISTS (SELECT 1 FROM signing_keys WHERE state = 'next'
            AND created_at <= now() - (sqlc.arg(published_seconds)::bigint * '1 second'::interval)
    )::bool AS next_ready;

-- name: InsertSigningKey :exec
-- state is 'active' (signs at once) or 'next' (published, signs once
-- promoted).
INSERT INTO signing_keys (kid, private_key_cipher, public_jwk, state, activated_at)
VALUES (@kid, @private_key_cipher, @public_jwk, @state, CASE WHEN @state::text = 'active' THEN now() END);

-- name: PromoteNextSigningKey :execrows
-- The next key starts signing.
UPDATE signing_keys SET state = 'active', activated_at = now() WHERE state = 'next';

-- name: RetireActiveSigningKey :execrows
-- The active key stops signing; it stays published for published_seconds.
UPDATE signing_keys
SET state = 'retiring', rotated_at = now(), retire_after = now() + (sqlc.arg(published_seconds)::bigint * '1 second'::interval)
WHERE kid = sqlc.arg(kid) AND state = 'active';

-- name: RetireExpiredSigningKeys :execrows
UPDATE signing_keys
SET state = 'retired', retired_at = now(), private_key_cipher = NULL
WHERE state = 'retiring' AND retire_after <= now();

-- name: ListPublishedSigningKeys :many
-- The JWKS: the key that signs, the one that will, and those retiring.
SELECT * FROM signing_keys WHERE state IN ('next', 'active', 'retiring') ORDER BY created_at DESC, kid;
