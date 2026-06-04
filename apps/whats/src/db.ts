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
);`

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

export async function linkChat(jid: string, conversationID: string): Promise<void> {
  await pool.query(
    "INSERT INTO whats_chats (jid, conversation_id) VALUES ($1,$2) ON CONFLICT (jid) DO NOTHING",
    [jid, conversationID],
  )
}
