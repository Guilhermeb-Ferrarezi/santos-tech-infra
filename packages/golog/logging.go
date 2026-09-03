// Package golog fornece o middleware de log de acesso HTTP estruturado
// compartilhado por todos os serviços Go do ecossistema Santos Tech.
package golog

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// Middleware de log de acesso HTTP estruturado (JSON → stdout → Loki).
//
// Cada requisição vira uma linha estruturada com:
//   - quem fez ......... ip, ua (user-agent)
//   - request .......... method, path, query, req_body (payload completo)
//   - responde ......... status, bytes, resp_body (payload completo)
//   - status code ...... status
//   - tempo de resposta. dur_ms
//   - horário .......... time (injetado pelo slog)
//   - extras ........... request_id (gerado/propagado), proto, host
//
// Captura de payload: request E response completos. Valores de credenciais
// (senha, token, secret, cartão, cvv…) são redigidos para *** por padrão —
// desligável com LOG_REDACT=0. Uploads (multipart) e tipos binários não são
// capturados (só placeholder). Tudo controlável por env (ver vars abaixo).
//
// Recupera panics (loga stack + 500), preserva streaming (SSE) e upgrade de
// conexão (WebSocket), e rebaixa health-checks para Debug pra não poluir.
//
// Este pacote era um arquivo logging.go idêntico em todos os serviços Go;
// foi extraído para cá. Só o ponto de plugue muda em cada serviço.
// ─────────────────────────────────────────────────────────────────────────────

var (
	// logBodies liga/desliga a captura de payload (request + response).
	logBodies = logEnvBool("LOG_BODIES", true)
	// logRedact redige valores sensíveis (senha, token, cartão…). DESLIGAR
	// (LOG_REDACT=0) faz os payloads irem 100% crus pro log — inclui senhas.
	logRedact = logEnvBool("LOG_REDACT", true)
	// logBodyMaxBytes: quanto de cada payload é logado (o resto é truncado).
	logBodyMaxBytes = logEnvInt("LOG_BODY_MAX_BYTES", 16384)
	// logBodyHardCap: teto de segurança — requests maiores que isso nem são
	// bufferizados (evita OOM com uploads grandes que escapem do filtro de tipo).
	logBodyHardCap = logEnvInt("LOG_BODY_HARD_CAP", 1<<20) // 1 MiB
	// logRepeatWindow agrupa erros 4xx IDÊNTICOS (mesmo método, rota, status e
	// IP) dentro da janela: o primeiro sai na hora, os seguintes são contados e
	// só viram linha quando a janela fecha, com repeated=N.
	//
	// O caso que motivou isto: um PC do laboratório com app desatualizado
	// levando 401 no heartbeat a cada 30s — 120 linhas por hora, 91% de tudo
	// que o serviço logava, todas dizendo a mesma coisa. Silenciar a rota
	// resolveria o volume e esconderia o problema; agrupar mantém o sinal
	// (inclusive quantas vezes aconteceu) e devolve o log a quem procura outra
	// coisa. 0 desliga.
	logRepeatWindow = logEnvDuration("LOG_REPEAT_WINDOW", 5*time.Minute)
)

// repeatState guarda, por assinatura de linha, quando ela foi logada pela
// última vez e quantas repetições foram engolidas desde então.
type repeatState struct {
	lastLoggedAt time.Time
	suppressed   int
}

var (
	repeatMu   sync.Mutex
	repeatSeen = map[string]*repeatState{}
)

// repeatSeenMax limita a memória do agrupador: cada assinatura distinta ocupa
// uma entrada, e a chave inclui o IP — sem teto, uma varredura de rotas de um
// IP aleatório faria o mapa crescer sem parar.
const repeatSeenMax = 4096

// shouldLogRepeat decide se esta linha vai pro log agora e devolve quantas
// repetições foram suprimidas desde a última que saiu.
//
// A primeira ocorrência de uma assinatura sempre passa; as seguintes só passam
// quando a janela fecha — e aí levam junto a contagem do que ficou pra trás,
// pra que "aconteceu 87 vezes" não se perca.
func shouldLogRepeat(key string, now time.Time, window time.Duration) (bool, int) {
	if window <= 0 {
		return true, 0
	}
	repeatMu.Lock()
	defer repeatMu.Unlock()

	if st, ok := repeatSeen[key]; ok {
		if now.Sub(st.lastLoggedAt) < window {
			st.suppressed++
			return false, 0
		}
		n := st.suppressed
		st.lastLoggedAt = now
		st.suppressed = 0
		return true, n
	}

	// Poda antes de inserir: entradas cuja janela já fechou não têm mais o que
	// agrupar. Se ainda assim estiver cheio, o mapa é zerado — perder o estado
	// do agrupamento só faz uma linha extra sair, nunca esconde nada.
	if len(repeatSeen) >= repeatSeenMax {
		for k, st := range repeatSeen {
			if now.Sub(st.lastLoggedAt) >= window {
				delete(repeatSeen, k)
			}
		}
		if len(repeatSeen) >= repeatSeenMax {
			repeatSeen = map[string]*repeatState{}
		}
	}
	repeatSeen[key] = &repeatState{lastLoggedAt: now}
	return true, 0
}

// reqIDCtxKey é a chave do request-id no context (tipo próprio p/ não colidir).
type reqIDCtxKey struct{}

// userIDLogCtxKey guarda um *int64 mutável p/ o authGuard escrever o user_id
// resolvido, que o RequestLogger depois inclui no log de acesso.
type userIDLogCtxKey struct{}

// userNameLogCtxKey guarda um *string mutável p/ o authGuard escrever o nome
// do usuário, que o RequestLogger depois inclui no log de acesso.
type userNameLogCtxKey struct{}

// SetUserID grava o user_id resolvido no contexto da requisição atual para
// que o RequestLogger o inclua no log de acesso. Deve ser chamado pelo authGuard.
func SetUserID(ctx context.Context, uid int64) {
	if p, ok := ctx.Value(userIDLogCtxKey{}).(*int64); ok && p != nil {
		*p = uid
	}
}

// SetUserName grava o nome do usuário no contexto para que o RequestLogger
// o inclua no log de acesso junto com o user_id.
func SetUserName(ctx context.Context, name string) {
	if p, ok := ctx.Value(userNameLogCtxKey{}).(*string); ok && p != nil {
		*p = name
	}
}

// RequestIDFromContext devolve o request-id desta requisição ("" se ausente).
// Útil pra anexar o mesmo id em logs de negócio dentro dos handlers.
func RequestIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(reqIDCtxKey{}).(string); ok {
		return v
	}
	return ""
}

// InitLogging configura o slog default como JSON no stdout (ideal p/ Loki).
// LOG_LEVEL controla o nível: debug | info | warn | error (default info).
// Serviços que já configuram o slog (bot-go, auto-fixer) não precisam chamar.
func InitLogging() {
	level := slog.LevelInfo
	switch strings.ToLower(strings.TrimSpace(os.Getenv("LOG_LEVEL"))) {
	case "debug":
		level = slog.LevelDebug
	case "warn", "warning":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	h := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	slog.SetDefault(slog.New(h))
}

// logResponseWriter embrulha o ResponseWriter pra capturar status, bytes e o
// corpo da resposta, preservando Flush (SSE) e Hijack (WebSocket) do original.
type logResponseWriter struct {
	http.ResponseWriter
	status    int
	bytes     int
	wrote     bool
	body      *bytes.Buffer // nil = não captura (desligado ou tipo não-loggável)
	bodyTrunc bool
}

func (w *logResponseWriter) WriteHeader(code int) {
	if w.wrote {
		return // ignora WriteHeader duplicado (evita "superfluous" no log)
	}
	w.status = code
	w.wrote = true
	// Não captura streaming/binário (SSE, imagens, octet-stream): inviável/inútil.
	if w.body != nil && !isLoggableContentType(w.Header().Get("Content-Type")) {
		w.body = nil
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *logResponseWriter) Write(b []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += n
	if w.body != nil {
		if room := logBodyMaxBytes - w.body.Len(); room > 0 {
			if room < len(b) {
				w.body.Write(b[:room])
				w.bodyTrunc = true
			} else {
				w.body.Write(b)
			}
		} else if len(b) > 0 {
			w.bodyTrunc = true
		}
	}
	return n, err
}

// Flush preserva streaming (SSE / chunked). No-op se o writer não suportar.
func (w *logResponseWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack preserva upgrade de conexão (WebSocket). Erro se o writer não suportar.
func (w *logResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := w.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, http.ErrNotSupported
}

// RequestLogger é o middleware de log de acesso. Deve ser o mais EXTERNO da
// cadeia, pra medir tempo e status finais e recuperar panics de qualquer camada.
func RequestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		// request-id: reaproveita o que a borda mandar (Cloudflare/Traefik) ou gera.
		rid := logFirstNonEmpty(r.Header.Get("X-Request-Id"), r.Header.Get("Cf-Ray"))
		if rid == "" {
			rid = logGenRequestID()
		}
		w.Header().Set("X-Request-Id", rid)
		// Ponteiros mutáveis de user_id e user_name: authGuard escreve, logger lê no defer.
		var loggedUID int64
		var loggedName string
		ctx := context.WithValue(r.Context(), reqIDCtxKey{}, rid)
		ctx = context.WithValue(ctx, userIDLogCtxKey{}, &loggedUID)
		ctx = context.WithValue(ctx, userNameLogCtxKey{}, &loggedName)
		r = r.WithContext(ctx)

		// Captura o corpo do request ANTES de servir (restaura p/ o handler ler).
		reqCT := r.Header.Get("Content-Type")
		var reqBody []byte
		var reqTrunc, reqCaptured bool
		var reqSkip string
		if logBodies {
			reqBody, reqTrunc, reqCaptured, reqSkip = captureRequestBody(r)
		}

		lw := &logResponseWriter{ResponseWriter: w, status: http.StatusOK}
		if logBodies {
			lw.body = &bytes.Buffer{}
		}

		defer func() {
			if rec := recover(); rec != nil {
				if !lw.wrote {
					lw.WriteHeader(http.StatusInternalServerError)
				}
				slog.Error("http panic",
					"request_id", rid,
					"method", r.Method,
					"path", r.URL.Path,
					"ip", logRemoteIP(r),
					"panic", rec,
					"stack", string(debug.Stack()),
				)
				sentryRecover(rec)
			}

			durMs := time.Since(start).Milliseconds()
			level := slog.LevelInfo
			switch {
			case strings.HasSuffix(r.URL.Path, "/health"):
				level = slog.LevelDebug // health-checks (Coolify/uptime) são ruidosos
			case lw.status == http.StatusNotFound && r.URL.Path == "/":
				level = slog.LevelDebug // bots/probes batendo na raiz — noise
			case lw.status >= 500:
				level = slog.LevelError
			case lw.status >= 400:
				level = slog.LevelWarn
			case r.Method == http.MethodOptions || r.Method == http.MethodHead:
				level = slog.LevelDebug // CORS preflight e HEAD: sempre noise
			case r.Method == http.MethodGet && lw.status >= 200 && lw.status < 300 && durMs < 5000:
				level = slog.LevelDebug // GET 2xx rápido: sem valor diagnóstico
			case lw.status >= 200 && lw.status < 300 &&
				(r.URL.Path == "/auth/me" || r.URL.Path == "/auth/refresh" ||
					// Telemetria dos PCs do laboratório: heartbeat (2/min por
					// PC), inventário, ícones e captura de tela. Tudo automático
					// e sem valor diagnóstico quando dá certo — só interessa
					// quando falha, e aí o status já joga pra WARN/ERROR.
					strings.HasPrefix(r.URL.Path, "/public/lab-devices/")):
				level = slog.LevelDebug // polling frequente: só interessa quando falha
			}

			attrs := []slog.Attr{
				slog.String("request_id", rid),
				slog.String("ip", logRemoteIP(r)),
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", lw.status),
				slog.Int("bytes", lw.bytes),
				slog.Int64("dur_ms", durMs),
				slog.String("ua", r.UserAgent()),
				slog.String("proto", r.Proto),
				slog.String("host", r.Host),
			}
			if loggedUID != 0 {
				attrs = append(attrs, slog.Int64("user_id", loggedUID))
			}
			if loggedName != "" {
				attrs = append(attrs, slog.String("user_name", loggedName))
			}
			if r.URL.RawQuery != "" {
				attrs = append(attrs, slog.String("query", redactKV(r.URL.RawQuery)))
			}
			switch {
			case reqCaptured:
				attrs = append(attrs, slog.String("req_body", redactBody(reqCT, reqBody)))
				if reqTrunc {
					attrs = append(attrs, slog.Bool("req_body_truncated", true))
				}
			case reqSkip != "":
				attrs = append(attrs, slog.String("req_body", reqSkip))
			}
			if lw.body != nil && lw.body.Len() > 0 {
				attrs = append(attrs, slog.String("resp_body", redactBody(lw.Header().Get("Content-Type"), lw.body.Bytes())))
				if lw.bodyTrunc {
					attrs = append(attrs, slog.Bool("resp_body_truncated", true))
				}
			}

			// Agrupamento: só 4xx. Erro de cliente é o que repete em laço
			// (credencial velha, integração mal configurada) e a centésima
			// linha não diz nada que a primeira já não tenha dito. 5xx fica
			// fora de propósito — é problema NOSSO, e cada ocorrência importa,
			// inclusive pra perceber que virou rajada.
			if lw.status >= 400 && lw.status < 500 {
				key := r.Method + " " + r.URL.Path + " " + strconv.Itoa(lw.status) + " " + logRemoteIP(r)
				ok, suppressed := shouldLogRepeat(key, time.Now(), logRepeatWindow)
				if !ok {
					return
				}
				if suppressed > 0 {
					attrs = append(attrs, slog.Int("repeated", suppressed))
				}
			}

			slog.LogAttrs(r.Context(), level, "http", attrs...)
		}()

		next.ServeHTTP(lw, r)
	})
}

// captureRequestBody lê o corpo do request p/ log e o RESTAURA intacto pro
// handler. Pula tipos não-loggáveis (multipart/binário) e bodies acima do teto.
func captureRequestBody(r *http.Request) (logged []byte, truncated, captured bool, skip string) {
	if r.Body == nil || r.Body == http.NoBody {
		return nil, false, false, ""
	}
	if !isLoggableContentType(r.Header.Get("Content-Type")) {
		return nil, false, false, "<skipped: " + r.Header.Get("Content-Type") + ">"
	}
	if r.ContentLength > int64(logBodyHardCap) {
		return nil, false, false, "<skipped: " + strconv.FormatInt(r.ContentLength, 10) + " bytes>"
	}
	full, err := io.ReadAll(io.LimitReader(r.Body, int64(logBodyHardCap)+1))
	_ = r.Body.Close()
	// Sempre restaura o body completo pro handler, aconteça o que acontecer.
	r.Body = io.NopCloser(bytes.NewReader(full))
	if err != nil {
		return nil, false, false, "<read error>"
	}
	if len(full) > logBodyHardCap {
		return nil, false, false, "<skipped: >" + strconv.Itoa(logBodyHardCap) + " bytes>"
	}
	if len(full) == 0 {
		return nil, false, false, ""
	}
	logged = full
	if len(logged) > logBodyMaxBytes {
		logged = logged[:logBodyMaxBytes]
		truncated = true
	}
	return logged, truncated, true, ""
}

// isLoggableContentType: só capturamos texto/JSON/form. Binário e streaming não.
func isLoggableContentType(ct string) bool {
	ct = strings.ToLower(strings.TrimSpace(ct))
	if ct == "" {
		return true // sem content-type: provavelmente texto curto
	}
	switch {
	case strings.HasPrefix(ct, "multipart/"),
		strings.HasPrefix(ct, "application/octet-stream"),
		strings.HasPrefix(ct, "image/"),
		strings.HasPrefix(ct, "video/"),
		strings.HasPrefix(ct, "audio/"),
		strings.HasPrefix(ct, "application/pdf"),
		strings.HasPrefix(ct, "application/zip"),
		strings.HasPrefix(ct, "text/event-stream"):
		return false
	}
	return true
}

// ── redação de valores sensíveis ─────────────────────────────────────────────

var sensitiveKeySubstrings = []string{
	"password", "passwd", "senha", "secret", "token", "authorization",
	"apikey", "api_key", "api-key", "accesskey", "access_key", "accesstoken",
	"refresh", "client_secret", "clientsecret",
	"cvv", "cvc", "cardnumber", "card_number", "cardnum", "pan",
	"privatekey", "private_key", "otp", "totp", "mfa",
}

func isSensitiveKey(k string) bool {
	lk := strings.ToLower(k)
	for _, s := range sensitiveKeySubstrings {
		if strings.Contains(lk, s) {
			return true
		}
	}
	return false
}

// redactBody redige um payload. Tenta JSON (redação por chave, recursiva);
// se não for JSON, cai p/ regex de pares chave=valor / "chave":"valor".
func redactBody(ct string, data []byte) string {
	if !logRedact {
		return string(data)
	}
	var v any
	if json.Unmarshal(data, &v) == nil {
		redactValue(v)
		if out, err := json.Marshal(v); err == nil {
			return string(out)
		}
	}
	return redactKV(string(data))
}

func redactValue(v any) {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			if isSensitiveKey(k) {
				t[k] = "***"
			} else {
				redactValue(val)
			}
		}
	case []any:
		for _, e := range t {
			redactValue(e)
		}
	}
}

// RedactValue redige, recursivamente e IN PLACE, qualquer chave de mapa cujo
// nome contenha um termo sensível (mesma lista usada na redação de corpo HTTP
// deste pacote — ver sensitiveKeySubstrings). Ao contrário de redactBody, NÃO
// é controlada por LOG_REDACT: existe para chamadores que persistem dados
// estruturados fora do log de acesso (ex.: transcript de conversas do agent-go
// salvo no Postgres) e que precisam redigir segredos incondicionalmente, não
// só quando o operador está depurando com LOG_REDACT=0. Aceita o resultado de
// json.Unmarshal (map[string]any / []any); tipos escalares são no-op.
func RedactValue(v any) {
	redactValue(v)
}

// redactKVRe casa pares chave=valor (form/query) e "chave":"valor" (JSON cru)
// cujo nome de chave contém um termo sensível.
var redactKVRe = regexp.MustCompile(
	`(?i)("?[\w.\-\[\]]*(?:password|passwd|senha|secret|token|authorization|api[_-]?key|client[_-]?secret|access[_-]?token|refresh[_-]?token|cvv|cvc|card[_-]?number|pan|private[_-]?key|otp|totp|mfa)[\w.\-\[\]]*"?\s*[:=]\s*)"?([^"&,}\s][^"&,}]*)`)

func redactKV(s string) string {
	return redactKVRe.ReplaceAllString(s, `${1}***`)
}

// ── helpers ──────────────────────────────────────────────────────────────────

// logRemoteIP extrai o IP do cliente para LOG. Confia em CF-Connecting-IP e
// X-Forwarded-For (a borda é Cloudflare + Traefik). Como é só observabilidade
// (não rate-limit), não exige allowlist de proxy — diferente de clientIP().
func logRemoteIP(r *http.Request) string {
	if ip := strings.TrimSpace(r.Header.Get("Cf-Connecting-Ip")); ip != "" {
		return ip
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i >= 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func logFirstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// logGenRequestID gera um id curto e único (12 bytes aleatórios em hex).
func logGenRequestID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "req-" + time.Now().Format("20060102150405.000000")
	}
	return hex.EncodeToString(b[:])
}

func logEnvBool(k string, def bool) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(k))) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	return def
}

func logEnvInt(k string, def int) int {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// logEnvDuration lê uma duração no formato do time.ParseDuration ("30s", "5m").
// Valor inválido cai no default — configuração errada não pode derrubar o log.
func logEnvDuration(k string, def time.Duration) time.Duration {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d >= 0 {
			return d
		}
	}
	return def
}
