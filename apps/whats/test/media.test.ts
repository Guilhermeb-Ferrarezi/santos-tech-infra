import { test, expect } from "bun:test"
import { classifyMedia, MAX_MEDIA_BYTES } from "../src/media"

test("classifica imagem como anexável pro Claude", () => {
  const c = classifyMedia("image/jpeg", 1000)
  expect(c).toEqual({ kind: "image", ext: "jpg", claudeReadable: true, oversize: false })
})

test("classifica pdf como anexável", () => {
  expect(classifyMedia("application/pdf", 1000)?.claudeReadable).toBe(true)
})

test("áudio do whats (mimetype com codecs) registra mas não anexa", () => {
  const c = classifyMedia("audio/ogg; codecs=opus", 1000)
  expect(c).toEqual({ kind: "audio", ext: "ogg", claudeReadable: false, oversize: false })
})

test("vídeo registra mas não anexa", () => {
  expect(classifyMedia("video/mp4", 1000)?.kind).toBe("video")
})

test("imagem grande demais marca oversize", () => {
  expect(classifyMedia("image/png", MAX_MEDIA_BYTES + 1)?.oversize).toBe(true)
})

test("mimetype desconhecido devolve null", () => {
  expect(classifyMedia("application/zip", 10)).toBeNull()
})
