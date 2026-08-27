-- Queries de Agenda (eventos + confirmações de conflito + feriados municipais)

-- name: ListAgendaEventos :many
SELECT id::text, tipo, titulo, aluno_ou_grupo,
  professor_ou_responsavel_id,
  COALESCE((SELECT name FROM users WHERE id = professor_ou_responsavel_id), '')::text AS professor_ou_responsavel_nome,
  conteudo, jogo, qtd_pessoas, computadores_usados,
  data_inicio::text, hora_inicio::text, hora_fim::text,
  recorrencia, dia_semana, COALESCE(data_fim_recorrencia::text, '')::text AS data_fim_recorrencia,
  status_preparo, notas,
  created_by, COALESCE((SELECT name FROM users WHERE id = created_by), '')::text AS created_by_nome,
  created_at, updated_at
FROM agenda_eventos
ORDER BY data_inicio, hora_inicio;

-- name: GetAgendaEvento :one
SELECT id::text, tipo, titulo, aluno_ou_grupo,
  professor_ou_responsavel_id,
  COALESCE((SELECT name FROM users WHERE id = professor_ou_responsavel_id), '')::text AS professor_ou_responsavel_nome,
  conteudo, jogo, qtd_pessoas, computadores_usados,
  data_inicio::text, hora_inicio::text, hora_fim::text,
  recorrencia, dia_semana, COALESCE(data_fim_recorrencia::text, '')::text AS data_fim_recorrencia,
  status_preparo, notas,
  created_by, COALESCE((SELECT name FROM users WHERE id = created_by), '')::text AS created_by_nome,
  created_at, updated_at
FROM agenda_eventos WHERE id = $1::uuid;

-- name: InsertAgendaEvento :one
INSERT INTO agenda_eventos (
  tipo, titulo, aluno_ou_grupo, professor_ou_responsavel_id, conteudo, jogo,
  qtd_pessoas, computadores_usados, data_inicio, hora_inicio, hora_fim,
  recorrencia, dia_semana, data_fim_recorrencia, status_preparo, notas, created_by
) VALUES (
  sqlc.arg(tipo), sqlc.arg(titulo), sqlc.arg(aluno_ou_grupo), sqlc.arg(professor_ou_responsavel_id),
  sqlc.arg(conteudo), sqlc.arg(jogo), sqlc.arg(qtd_pessoas), sqlc.arg(computadores_usados),
  sqlc.arg(data_inicio)::date, sqlc.arg(hora_inicio)::time, sqlc.arg(hora_fim)::time,
  sqlc.arg(recorrencia), sqlc.arg(dia_semana), sqlc.arg(data_fim_recorrencia)::date,
  sqlc.arg(status_preparo), sqlc.arg(notas), sqlc.arg(created_by)
)
RETURNING id::text, tipo, titulo, aluno_ou_grupo,
  professor_ou_responsavel_id,
  COALESCE((SELECT name FROM users WHERE id = professor_ou_responsavel_id), '')::text AS professor_ou_responsavel_nome,
  conteudo, jogo, qtd_pessoas, computadores_usados,
  data_inicio::text, hora_inicio::text, hora_fim::text,
  recorrencia, dia_semana, COALESCE(data_fim_recorrencia::text, '')::text AS data_fim_recorrencia,
  status_preparo, notas,
  created_by, COALESCE((SELECT name FROM users WHERE id = created_by), '')::text AS created_by_nome,
  created_at, updated_at;

-- name: UpdateAgendaEvento :one
UPDATE agenda_eventos SET
  tipo=sqlc.arg(tipo), titulo=sqlc.arg(titulo), aluno_ou_grupo=sqlc.arg(aluno_ou_grupo),
  professor_ou_responsavel_id=sqlc.arg(professor_ou_responsavel_id), conteudo=sqlc.arg(conteudo), jogo=sqlc.arg(jogo),
  qtd_pessoas=sqlc.arg(qtd_pessoas), computadores_usados=sqlc.arg(computadores_usados),
  data_inicio=sqlc.arg(data_inicio)::date, hora_inicio=sqlc.arg(hora_inicio)::time, hora_fim=sqlc.arg(hora_fim)::time,
  recorrencia=sqlc.arg(recorrencia), dia_semana=sqlc.arg(dia_semana), data_fim_recorrencia=sqlc.arg(data_fim_recorrencia)::date,
  status_preparo=sqlc.arg(status_preparo), notas=sqlc.arg(notas), updated_at=now()
WHERE id=sqlc.arg(id)::uuid
RETURNING id::text, tipo, titulo, aluno_ou_grupo,
  professor_ou_responsavel_id,
  COALESCE((SELECT name FROM users WHERE id = professor_ou_responsavel_id), '')::text AS professor_ou_responsavel_nome,
  conteudo, jogo, qtd_pessoas, computadores_usados,
  data_inicio::text, hora_inicio::text, hora_fim::text,
  recorrencia, dia_semana, COALESCE(data_fim_recorrencia::text, '')::text AS data_fim_recorrencia,
  status_preparo, notas,
  created_by, COALESCE((SELECT name FROM users WHERE id = created_by), '')::text AS created_by_nome,
  created_at, updated_at;

-- name: DeleteAgendaEvento :execrows
DELETE FROM agenda_eventos WHERE id=$1::uuid;

-- name: InsertAgendaEventoConfirmacao :exec
INSERT INTO agenda_evento_confirmacoes (evento_id, user_id, conflitos_com_ids)
VALUES (sqlc.arg(evento_id)::uuid, sqlc.arg(user_id), sqlc.arg(conflitos_com_ids));

-- name: ListAgendaFeriadosMunicipaisAno :many
SELECT id, data::text, nome, created_at FROM agenda_feriados_municipais
WHERE EXTRACT(YEAR FROM data) = sqlc.arg(ano)::int ORDER BY data;

-- name: ListAgendaFeriadosMunicipais :many
SELECT id, data::text, nome, created_at FROM agenda_feriados_municipais ORDER BY data;

-- name: InsertAgendaFeriadoMunicipal :one
INSERT INTO agenda_feriados_municipais (data, nome) VALUES (sqlc.arg(data)::date, sqlc.arg(nome))
RETURNING id, data::text, nome, created_at;

-- name: DeleteAgendaFeriadoMunicipal :execrows
DELETE FROM agenda_feriados_municipais WHERE id=$1;
