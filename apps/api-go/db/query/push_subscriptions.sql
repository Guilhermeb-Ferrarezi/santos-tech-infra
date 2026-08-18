-- Queries de Web Push (subscriptions de navegador por usuário)

-- name: UpsertPushSubscription :one
INSERT INTO push_subscriptions (user_id, endpoint, p256dh, auth, user_agent)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (endpoint) DO UPDATE
SET user_id    = EXCLUDED.user_id,
    p256dh     = EXCLUDED.p256dh,
    auth       = EXCLUDED.auth,
    user_agent = EXCLUDED.user_agent
RETURNING id, user_id, endpoint, p256dh, auth, user_agent, created_at;

-- name: ListPushSubscriptionsByUser :many
SELECT id, user_id, endpoint, p256dh, auth, user_agent, created_at
FROM push_subscriptions WHERE user_id = $1;

-- name: DeletePushSubscriptionByEndpoint :execrows
DELETE FROM push_subscriptions WHERE endpoint = $1 AND user_id = $2;
