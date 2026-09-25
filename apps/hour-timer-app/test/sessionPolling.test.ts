import { describe, expect, test } from "bun:test";
import { MAX_BACKOFF_MS, POLL_INTERVAL_MS, nextPollDelay, pollHourSession } from "../src/lib/sessionPolling";

// Incidente de 25/09/2026: o app consultava em loop (a cada 5s, janela
// principal + overlay) uma sessão que já não existia. Os 404 somados de todos
// os PCs estouraram o antibot do api-go, que baniu por 24h o IP público da
// escola inteira (NAT). Estes testes fixam: 404 = sessão encerrada → para de
// consultar; qualquer outra falha → tenta de novo com backoff.

type Scheduled = { fn: () => void; ms: number };

function harness(responses: Array<Response | Error>) {
  const scheduled: Scheduled[] = [];
  const calls: string[] = [];
  const events: string[] = [];
  const fetchFn = async (url: string) => {
    calls.push(url);
    const next = responses.shift();
    if (!next) throw new Error("sem resposta configurada");
    if (next instanceof Error) throw next;
    return next;
  };
  const stop = pollHourSession<{ status: string }>("abc", {
    origin: "https://api.test",
    fetchFn: fetchFn as unknown as typeof fetch,
    schedule: (fn, ms) => {
      scheduled.push({ fn, ms });
      return scheduled.length;
    },
    cancelSchedule: () => {},
    onData: (d) => events.push(`data:${d.status}`),
    onGone: () => events.push("gone"),
    onError: () => events.push("error"),
  });
  // roda o próximo tick agendado e espera o fetch assentar
  async function runNext() {
    const s = scheduled.shift();
    if (!s) throw new Error("nada agendado");
    s.fn();
    await flush();
  }
  return { scheduled, calls, events, stop, runNext };
}

const flush = () => new Promise((r) => setTimeout(r, 0));
const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status });

describe("pollHourSession", () => {
  test("200 entrega os dados e agenda o próximo poll no intervalo normal", async () => {
    const h = harness([json({ status: "active" })]);
    await flush();
    expect(h.calls).toEqual(["https://api.test/public/hour-sessions/abc"]);
    expect(h.events).toEqual(["data:active"]);
    expect(h.scheduled.map((s) => s.ms)).toEqual([POLL_INTERVAL_MS]);
  });

  test("404 = sessão encerrada: avisa uma vez e NUNCA mais consulta o token", async () => {
    const h = harness([json({ status: "active" }), json({ code: "HOUR_SESSION_NOT_FOUND" }, 404)]);
    await flush();
    await h.runNext();
    expect(h.events).toEqual(["data:active", "gone"]);
    expect(h.scheduled).toEqual([]);
    expect(h.calls.length).toBe(2);
  });

  test("falha de rede, 429 e 5xx tentam de novo com backoff crescente", async () => {
    const h = harness([
      new Error("offline"),
      json({}, 429),
      json({}, 503),
      json({ status: "active" }),
    ]);
    await flush();
    expect(h.scheduled[0].ms).toBe(nextPollDelay(1));
    await h.runNext();
    expect(h.scheduled[0].ms).toBe(nextPollDelay(2));
    await h.runNext();
    expect(h.scheduled[0].ms).toBe(nextPollDelay(3));
    await h.runNext();
    // sucesso zera o backoff
    expect(h.scheduled[0].ms).toBe(POLL_INTERVAL_MS);
    expect(h.events).toEqual(["error", "error", "error", "data:active"]);
  });

  test("stop() durante o fetch não dispara callback nem agenda nada", async () => {
    const h = harness([json({ status: "active" })]);
    h.stop();
    await flush();
    expect(h.events).toEqual([]);
    expect(h.scheduled).toEqual([]);
  });
});

describe("nextPollDelay", () => {
  test("dobra a cada falha a partir do intervalo normal e satura no teto", () => {
    expect(nextPollDelay(0)).toBe(POLL_INTERVAL_MS);
    expect(nextPollDelay(1)).toBe(POLL_INTERVAL_MS * 2);
    expect(nextPollDelay(2)).toBe(POLL_INTERVAL_MS * 4);
    expect(nextPollDelay(50)).toBe(MAX_BACKOFF_MS);
    for (let i = 0; i < 20; i++) expect(nextPollDelay(i + 1)).toBeGreaterThanOrEqual(nextPollDelay(i));
  });
});
