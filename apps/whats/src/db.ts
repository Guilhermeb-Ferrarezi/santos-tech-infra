import { Pool } from "pg"
import { config } from "./config"

export const pool = new Pool({ connectionString: config.databaseURL })

const SCHEMA = `
CREATE TABLE IF NOT EXISTS whats_allowlist (
  jid        TEXT PRIMARY KEY,
  label      TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS whats_chats (
  jid             TEXT PRIMARY KEY,
  conversation_id UUID NOT NULL,
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS whats_seen_chats (
  jid     TEXT PRIMARY KEY,
  name    TEXT NOT NULL DEFAULT '',
  preview TEXT NOT NULL DEFAULT '',
  last_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS whats_knowledge (
  id          BIGSERIAL PRIMARY KEY,
  jid         TEXT NOT NULL,
  question    TEXT NOT NULL,
  reason      TEXT NOT NULL DEFAULT '',
  answer      TEXT,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  answered_at TIMESTAMPTZ
);
CREATE TABLE IF NOT EXISTS whats_messages (
  id        BIGSERIAL PRIMARY KEY,
  jid       TEXT NOT NULL,
  direction TEXT NOT NULL CHECK (direction IN ('in','out')),
  text      TEXT NOT NULL,
  ts        TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_whats_messages_jid ON whats_messages(jid, id);`

export async function migrate(): Promise<void> {
  await pool.query(SCHEMA)
}

export async function listAllowlist(): Promise<{ jid: string; label: string }[]> {
  const r = await pool.query("SELECT jid, label FROM whats_allowlist ORDER BY created_at")
  return r.rows
}

export async function addAllow(jid: string, label: string): Promise<void> {
  await pool.query(
    "INSERT INTO whats_allowlist (jid, label) VALUES ($1,$2) ON CONFLICT (jid) DO UPDATE SET label=$2",
    [jid, label],
  )
}

export async function removeAllow(jid: string): Promise<void> {
  await pool.query("DELETE FROM whats_allowlist WHERE jid=$1", [jid])
}

export async function allowlistSet(): Promise<Set<string>> {
  const r = await pool.query("SELECT jid FROM whats_allowlist")
  return new Set(r.rows.map((x) => x.jid as string))
}

// recordSeen registra/atualiza um chat que mandou mensagem — o "radar" que a UI usa
// pra ativar o agente sem precisar digitar JID. Preview curto, só pra reconhecer o chat.
export async function recordSeen(jid: string, name: string, preview: string): Promise<void> {
  await pool.query(
    `INSERT INTO whats_seen_chats (jid, name, preview, last_at) VALUES ($1,$2,$3,now())
     ON CONFLICT (jid) DO UPDATE SET name=CASE WHEN $2<>'' THEN $2 ELSE whats_seen_chats.name END, preview=$3, last_at=now()`,
    [jid, name, preview.slice(0, 120)],
  )
}

// listSeen devolve os chats recentes com a flag de permitido (join com a allowlist).
export async function listSeen(): Promise<{ jid: string; name: string; preview: string; lastAt: string; allowed: boolean }[]> {
  const r = await pool.query(
    `SELECT s.jid, s.name, s.preview, s.last_at, (a.jid IS NOT NULL) AS allowed
     FROM whats_seen_chats s LEFT JOIN whats_allowlist a ON a.jid = s.jid
     ORDER BY s.last_at DESC LIMIT 50`,
  )
  return r.rows.map((x) => ({ jid: x.jid, name: x.name, preview: x.preview, lastAt: x.last_at, allowed: x.allowed }))
}

// saveMessage grava uma mensagem do transcript real (visor de conversas): entrada de
// chat permitido ou resposta enviada. O simulador não passa por aqui.
export async function saveMessage(jid: string, direction: "in" | "out", text: string): Promise<void> {
  await pool.query("INSERT INTO whats_messages (jid, direction, text) VALUES ($1,$2,$3)", [jid, direction, text])
}

export async function listMessages(
  jid: string,
): Promise<{ id: number; direction: "in" | "out"; text: string; ts: string }[]> {
  const r = await pool.query(
    "SELECT id, direction, text, ts FROM whats_messages WHERE jid=$1 ORDER BY id DESC LIMIT 200",
    [jid],
  )
  return r.rows.reverse()
}

// ── Memória auto-aprendida (escalações → FAQ) ────────────────────────────────

// insertPendingKnowledge registra a pergunta escalada (sem resposta ainda).
export async function insertPendingKnowledge(jid: string, question: string, reason: string): Promise<void> {
  await pool.query("INSERT INTO whats_knowledge (jid, question, reason) VALUES ($1,$2,$3)", [jid, question, reason])
}

// answerLatestPending grava a resposta do dono na escalação pendente mais recente
// do chat. Devolve true se havia pendência.
export async function answerLatestPending(jid: string, answer: string): Promise<boolean> {
  const r = await pool.query(
    `UPDATE whats_knowledge SET answer=$2, answered_at=now()
     WHERE id = (SELECT id FROM whats_knowledge WHERE jid=$1 AND answer IS NULL ORDER BY id DESC LIMIT 1)
     RETURNING id`,
    [jid, answer],
  )
  return (r.rowCount ?? 0) > 0
}

// listFacts devolve os fatos respondidos (a memória usada nos turnos).
export async function listFacts(): Promise<{ id: number; question: string; answer: string }[]> {
  const r = await pool.query(
    "SELECT id, question, answer FROM whats_knowledge WHERE answer IS NOT NULL ORDER BY id DESC LIMIT 500",
  )
  return r.rows
}

export async function listKnowledge(): Promise<
  { id: number; jid: string; question: string; answer: string | null; createdAt: string }[]
> {
  const r = await pool.query(
    "SELECT id, jid, question, answer, created_at FROM whats_knowledge ORDER BY id DESC LIMIT 200",
  )
  return r.rows.map((x) => ({ id: x.id, jid: x.jid, question: x.question, answer: x.answer, createdAt: x.created_at }))
}

export async function deleteKnowledge(id: number): Promise<void> {
  await pool.query("DELETE FROM whats_knowledge WHERE id=$1", [id])
}

// userRole devolve o papel do usuário na tabela compartilhada do auth central
// (admin = 3). 0 quando o usuário não existe — fail-closed no guard.
export async function userRole(userID: number): Promise<number> {
  const r = await pool.query("SELECT role FROM users WHERE id=$1", [userID])
  return Number(r.rows[0]?.role ?? 0)
}

export async function conversationFor(jid: string): Promise<string | null> {
  const r = await pool.query("SELECT conversation_id FROM whats_chats WHERE jid=$1", [jid])
  return r.rows[0]?.conversation_id ?? null
}

// unlinkChat desfaz o vínculo chat→conversa (reset do simulador): a próxima mensagem
// cria uma conversa nova no agent-go; a antiga permanece lá (auditoria).
export async function unlinkChat(jid: string): Promise<void> {
  await pool.query("DELETE FROM whats_chats WHERE jid=$1", [jid])
}

export async function linkChat(jid: string, conversationID: string): Promise<void> {
  await pool.query(
    "INSERT INTO whats_chats (jid, conversation_id) VALUES ($1,$2) ON CONFLICT (jid) DO NOTHING",
    [jid, conversationID],
  )
}
