-- Queries de Social Posts

-- name: ListSocialPosts :many
SELECT id::text, title, caption, platform, pilar, status,
  scheduled_at, media_url, reference_url,
  formato, objetivo, programa, receita, plataformas_destino, copy_arte, hashtags,
  conceito_visual, paleta, prompt_ia, specs, master_url, mandatorios,
  responsavel_id, COALESCE((SELECT name FROM users WHERE id = responsavel_id), ''),
  created_by, created_at, updated_at
FROM social_posts
ORDER BY COALESCE(scheduled_at, created_at) DESC;

-- name: GetSocialPost :one
SELECT id::text, title, caption, platform, pilar, status,
  scheduled_at, media_url, reference_url,
  formato, objetivo, programa, receita, plataformas_destino, copy_arte, hashtags,
  conceito_visual, paleta, prompt_ia, specs, master_url, mandatorios,
  responsavel_id, COALESCE((SELECT name FROM users WHERE id = responsavel_id), ''),
  created_by, created_at, updated_at
FROM social_posts WHERE id = $1::uuid;

-- name: InsertSocialPost :one
INSERT INTO social_posts (
  title, caption, platform, pilar, status, scheduled_at, media_url, reference_url,
  formato, objetivo, programa, receita, plataformas_destino, copy_arte, hashtags,
  conceito_visual, paleta, prompt_ia, specs, master_url, mandatorios, responsavel_id, created_by
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23)
RETURNING id::text, title, caption, platform, pilar, status,
  scheduled_at, media_url, reference_url,
  formato, objetivo, programa, receita, plataformas_destino, copy_arte, hashtags,
  conceito_visual, paleta, prompt_ia, specs, master_url, mandatorios,
  responsavel_id, COALESCE((SELECT name FROM users WHERE id = responsavel_id), ''),
  created_by, created_at, updated_at;

-- name: UpdateSocialPost :one
UPDATE social_posts SET
  title = $2, caption = $3, platform = $4, pilar = $5, status = $6,
  scheduled_at = $7, media_url = $8, reference_url = $9,
  formato = $10, objetivo = $11, programa = $12, receita = $13,
  plataformas_destino = $14, copy_arte = $15, hashtags = $16,
  conceito_visual = $17, paleta = $18, prompt_ia = $19, specs = $20,
  master_url = $21, mandatorios = $22, responsavel_id = $23, updated_at = now()
WHERE id = $1::uuid
RETURNING id::text, title, caption, platform, pilar, status,
  scheduled_at, media_url, reference_url,
  formato, objetivo, programa, receita, plataformas_destino, copy_arte, hashtags,
  conceito_visual, paleta, prompt_ia, specs, master_url, mandatorios,
  responsavel_id, COALESCE((SELECT name FROM users WHERE id = responsavel_id), ''),
  created_by, created_at, updated_at;

-- name: UpdateSocialPostStatus :one
UPDATE social_posts SET status = $2, updated_at = now()
WHERE id = $1::uuid
RETURNING id::text, title, caption, platform, pilar, status,
  scheduled_at, media_url, reference_url,
  formato, objetivo, programa, receita, plataformas_destino, copy_arte, hashtags,
  conceito_visual, paleta, prompt_ia, specs, master_url, mandatorios,
  responsavel_id, COALESCE((SELECT name FROM users WHERE id = responsavel_id), ''),
  created_by, created_at, updated_at;

-- name: DeleteSocialPost :execrows
DELETE FROM social_posts WHERE id = $1::uuid;

-- name: ListSocialPostNotes :many
SELECT n.id, n.post_id::text, n.author_id, COALESCE(u.name, ''), n.content, n.created_at
FROM social_post_notes n
LEFT JOIN users u ON u.id = n.author_id
WHERE n.post_id = $1::uuid
ORDER BY n.created_at;

-- name: InsertSocialPostNote :one
INSERT INTO social_post_notes (post_id, author_id, content)
VALUES ($1::uuid, $2, $3)
RETURNING id, post_id::text, author_id,
  (SELECT COALESCE(name, '') FROM users WHERE id = author_id),
  content, created_at;

-- name: ListSocialPostStatusHistory :many
SELECT h.id, h.post_id::text, h.changed_by, COALESCE(u.name, ''), h.old_status, h.new_status, h.changed_at
FROM social_post_status_history h
LEFT JOIN users u ON u.id = h.changed_by
WHERE h.post_id = $1::uuid
ORDER BY h.changed_at DESC;

-- name: InsertSocialPostStatusHistory :exec
INSERT INTO social_post_status_history (post_id, changed_by, old_status, new_status)
VALUES ($1::uuid, $2, $3, $4);
