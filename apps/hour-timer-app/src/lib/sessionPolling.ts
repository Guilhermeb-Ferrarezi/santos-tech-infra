// Poll de GET /public/hour-sessions/{token}, compartilhado pela janela
// principal e pelo overlay.
//
// 25/09/2026: as duas janelas consultavam a cada 5s, pra sempre, uma sessão
// que já tinha sido apagada no servidor. Os 404 somados dos PCs do laboratório
// passaram de 30/min no IP público da escola (NAT — todo mundo sai pelo mesmo
// IP) e o antibot do api-go baniu a escola inteira por 24h: dashboard, portal
// e o heartbeat dos próprios PCs passaram a levar 403. Por isso:
//   - 404 = sessão encerrada/apagada → avisa (onGone) e PARA de consultar;
//   - qualquer outra falha (rede, 429, 5xx) → tenta de novo com backoff
//     exponencial, pra não martelar o servidor enquanto ele está com problema.

export const POLL_INTERVAL_MS = 5_000;
export const MAX_BACKOFF_MS = 60_000;

// Atraso até o próximo poll depois de N falhas seguidas (0 = sem falha).
export function nextPollDelay(consecutiveFailures: number): number {
  const exp = Math.min(consecutiveFailures, 10); // evita overflow do 2 ** n
  return Math.min(POLL_INTERVAL_MS * 2 ** exp, MAX_BACKOFF_MS);
}

interface PollOptions<T> {
  origin: string;
  onData: (data: T) => void;
  // Sessão não existe mais (404): o poll já parou quando isto é chamado.
  onGone: () => void;
  onError: () => void;
  fetchFn?: typeof fetch;
  schedule?: (fn: () => void, ms: number) => unknown;
  cancelSchedule?: (handle: unknown) => void;
}

// Começa a consultar na hora e devolve a função que para o poll.
export function pollHourSession<T>(token: string, opts: PollOptions<T>): () => void {
  const fetchFn = opts.fetchFn ?? fetch;
  const schedule = opts.schedule ?? ((fn, ms) => setTimeout(fn, ms));
  const cancelSchedule = opts.cancelSchedule ?? ((h) => clearTimeout(h as ReturnType<typeof setTimeout>));
  const url = `${opts.origin}/public/hour-sessions/${token}`;

  let stopped = false;
  let failures = 0;
  let handle: unknown = null;

  async function tick() {
    handle = null;
    let res: Response;
    try {
      res = await fetchFn(url);
    } catch {
      return fail();
    }
    if (stopped) return;
    if (res.status === 404) {
      stopped = true;
      opts.onGone();
      return;
    }
    if (!res.ok) return fail();
    let data: T;
    try {
      data = (await res.json()) as T;
    } catch {
      return fail();
    }
    if (stopped) return;
    failures = 0;
    opts.onData(data);
    handle = schedule(tick, POLL_INTERVAL_MS);
  }

  function fail() {
    if (stopped) return;
    failures++;
    opts.onError();
    handle = schedule(tick, nextPollDelay(failures));
  }

  void tick();
  return () => {
    stopped = true;
    if (handle !== null) cancelSchedule(handle);
  };
}
