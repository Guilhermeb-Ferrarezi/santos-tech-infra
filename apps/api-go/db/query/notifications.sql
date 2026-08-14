-- Queries de notificações (histórico do sino no header do dashboard)

-- name: InsertNotification :one
INSERT INTO notifications (user_id, title, body, url)
VALUES ($1, $2, $3, $4)
RETURNING id, user_id, title, body, url, read_at, created_at;

-- name: ListNotificationsByUser :many
SELECT id, user_id, title, body, url, read_at, created_at
FROM notifications
WHERE user_id = $1
ORDER BY created_at DESC
LIMIT $2;

-- name: CountUnreadNotifications :one
SELECT count(*) FROM notifications WHERE user_id = $1 AND read_at IS NULL;

-- name: MarkNotificationRead :execrows
UPDATE notifications SET read_at = now()
WHERE id = $1 AND user_id = $2 AND read_at IS NULL;

-- name: MarkAllNotificationsRead :execrows
UPDATE notifications SET read_at = now()
WHERE user_id = $1 AND read_at IS NULL;
