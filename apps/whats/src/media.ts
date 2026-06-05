import { randomUUID } from "crypto"
import { mkdir, writeFile } from "fs/promises"
import path from "path"
import { config } from "./config"

// Classificação e gravação da mídia recebida. Imagem/PDF vão como anexo pro
// Claude (visão); áudio/vídeo só ficam registrados no visor do dashboard.

export type MediaKind = "image" | "pdf" | "audio" | "video"

export interface MediaClass {
  kind: MediaKind
  ext: string
  claudeReadable: boolean // visão do Claude aceita (imagem/pdf)
  oversize: boolean
}

export const MAX_MEDIA_BYTES = 5 * 1024 * 1024 // limite da visão da API

const TYPES: Record<string, { kind: MediaKind; ext: string; claudeReadable: boolean }> = {
  "image/jpeg": { kind: "image", ext: "jpg", claudeReadable: true },
  "image/png": { kind: "image", ext: "png", claudeReadable: true },
  "image/webp": { kind: "image", ext: "webp", claudeReadable: true },
  "image/gif": { kind: "image", ext: "gif", claudeReadable: true },
  "application/pdf": { kind: "pdf", ext: "pdf", claudeReadable: true },
  "audio/ogg": { kind: "audio", ext: "ogg", claudeReadable: false },
  "audio/mpeg": { kind: "audio", ext: "mp3", claudeReadable: false },
  "video/mp4": { kind: "video", ext: "mp4", claudeReadable: false },
}

// classifyMedia normaliza o mimetype (baileys manda "audio/ogg; codecs=opus") e
// decide o destino. null = tipo que não registramos (ex.: documento não-PDF).
export function classifyMedia(mimetype: string, sizeBytes: number): MediaClass | null {
  const base = (mimetype.split(";")[0] ?? "").trim().toLowerCase()
  const t = TYPES[base]
  if (!t) return null
  return { ...t, oversize: sizeBytes > MAX_MEDIA_BYTES }
}

// saveMedia grava o arquivo no volume e devolve o media_id (nome do arquivo,
// UUID nosso — é o id servido por GET /media/:id).
export async function saveMedia(buf: Buffer, ext: string): Promise<string> {
  await mkdir(config.mediaDir, { recursive: true })
  const id = `${randomUUID()}.${ext}`
  await writeFile(path.join(config.mediaDir, id), buf)
  return id
}
