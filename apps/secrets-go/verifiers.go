package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// GenericVerifier despacha a verificação ativa pro provedor certo, cada um
// usando o endpoint oficial de healthcheck/identidade documentado por cada
// serviço (o mesmo tipo de pesquisa que fizemos pra Dotfy e AbacatePay antes
// de implementar — e testado manualmente com chave inventada antes de
// confiar, porque a doc às vezes erra: AbacatePay documenta /v2 mas quem
// responde é /v1; Cloudflare e Vercel usam códigos de status fora do padrão
// 401). Sempre GET/leitura, nunca uma chamada que altera estado.
//
// Semântica de retorno igual em todo lugar: (checked, active, sandbox).
//   - checked=false: não conseguimos confirmar nada (erro de rede, timeout,
//     ou resposta ambígua) — NUNCA deve ser lido como "chave inválida".
//   - checked=true, active=false: o provedor confirmou que a chave está
//     morta (401 documentado como "chave inválida/revogada" em cada um).
//   - checked=true, active=true: o provedor confirmou que a chave funciona.
//   - sandbox=true: além de ativa, é claramente uma credencial de teste (não
//     move dinheiro real nem expõe dado real) — hoje só o Mercado Pago
//     confirma isso pelo corpo da resposta (nickname/email "TESTUSER..."),
//     porque o prefixo do token sozinho (APP_USR-) NÃO é confiável pra essa
//     distinção lá (testado: token com prefixo de produção caiu em conta de
//     teste várias vezes). Pra todo o resto, sandbox=false sempre.
type GenericVerifier struct {
	http *http.Client
}

func NewGenericVerifier() *GenericVerifier {
	return &GenericVerifier{http: &http.Client{Timeout: 10 * time.Second}}
}

// CheckKey escolhe o verificador certo pela family (vem do fingerprint do
// valor ou do nome da keyword — ver fingerprint.go). family desconhecida ou
// sem verificador disponível → sempre checked=false.
func (g *GenericVerifier) CheckKey(ctx context.Context, family, value string) (checked bool, active bool, sandbox bool) {
	switch family {
	case "abacatepay":
		c, a := g.abacatePay(ctx, value)
		return c, a, false
	case "stripe":
		c, a := g.stripe(ctx, value)
		return c, a, false
	case "github":
		c, a := g.github(ctx, value)
		return c, a, false
	case "openai":
		c, a := g.openAI(ctx, value)
		return c, a, false
	case "sendgrid":
		c, a := g.sendGrid(ctx, value)
		return c, a, false
	case "slack":
		c, a := g.slack(ctx, value)
		return c, a, false
	case "anthropic":
		c, a := g.anthropic(ctx, value)
		return c, a, false
	case "cloudflare":
		c, a := g.cloudflare(ctx, value)
		return c, a, false
	case "huggingface":
		c, a := g.huggingFace(ctx, value)
		return c, a, false
	case "digitalocean":
		c, a := g.digitalOcean(ctx, value)
		return c, a, false
	case "vercel":
		c, a := g.vercel(ctx, value)
		return c, a, false
	case "netlify":
		c, a := g.netlify(ctx, value)
		return c, a, false
	case "npm":
		c, a := g.npmRegistry(ctx, value)
		return c, a, false
	case "heroku":
		c, a := g.heroku(ctx, value)
		return c, a, false
	case "discord":
		c, a := g.discord(ctx, value)
		return c, a, false
	case "mailgun":
		c, a := g.mailgun(ctx, value)
		return c, a, false
	case "groq":
		c, a := g.groq(ctx, value)
		return c, a, false
	case "mistral":
		c, a := g.mistral(ctx, value)
		return c, a, false
	case "replicate":
		c, a := g.replicate(ctx, value)
		return c, a, false
	case "elevenlabs":
		c, a := g.elevenLabs(ctx, value)
		return c, a, false
	case "cohere":
		c, a := g.cohere(ctx, value)
		return c, a, false
	case "mercadopago":
		return g.mercadoPago(ctx, value)
	case "asaas":
		c, a := g.asaas(ctx, value)
		return c, a, false
	case "pagarme":
		c, a := g.pagarme(ctx, value)
		return c, a, false
	case "telegram":
		c, a := g.telegram(ctx, value)
		return c, a, false
	case "gemini":
		c, a := g.gemini(ctx, value)
		return c, a, false
	case "square":
		c, a := g.square(ctx, value)
		return c, a, false
	case "clerk":
		c, a := g.clerk(ctx, value)
		return c, a, false
	case "railway":
		c, a := g.railway(ctx, value)
		return c, a, false
	case "firebase":
		c, a := g.firebase(ctx, value)
		return c, a, false
	case "openrouter":
		c, a := g.openRouter(ctx, value)
		return c, a, false
	case "resend":
		c, a := g.resend(ctx, value)
		return c, a, false
	case "notion":
		c, a := g.notion(ctx, value)
		return c, a, false
	case "figma":
		c, a := g.figma(ctx, value)
		return c, a, false
	case "sentry":
		c, a := g.sentry(ctx, value)
		return c, a, false
	case "datadog":
		c, a := g.datadog(ctx, value)
		return c, a, false
	case "circleci":
		c, a := g.circleci(ctx, value)
		return c, a, false
	case "gitlab":
		c, a := g.gitlab(ctx, value)
		return c, a, false
	case "together":
		c, a := g.together(ctx, value)
		return c, a, false
	case "fireworks":
		c, a := g.fireworks(ctx, value)
		return c, a, false
	case "xai":
		c, a := g.xai(ctx, value)
		return c, a, false
	case "deepseek":
		c, a := g.deepseek(ctx, value)
		return c, a, false
	case "stability":
		c, a := g.stability(ctx, value)
		return c, a, false
	case "deepgram":
		c, a := g.deepgram(ctx, value)
		return c, a, false
	case "assemblyai":
		c, a := g.assemblyAI(ctx, value)
		return c, a, false
	default:
		return false, false, false
	}
}

// abacatePay: GET /v1/store/get — a doc oficial mostra "/v2" num trecho mas
// o endpoint que responde de verdade é "/v1" (testado manualmente: /v2 dá
// 400 "Not found" mesmo com header correto). 401=inválida, e
// 403=válida-mas-sem-permissão (a chave pode não ter STORE:READ) também
// tratamos como "ativa".
func (g *GenericVerifier) abacatePay(ctx context.Context, key string) (bool, bool) {
	return g.simpleBearer(ctx, "https://api.abacatepay.com/v1/store/get", key, statusRules{ok200: true, forbidden403: true})
}

// stripe: GET /v1/balance com Basic Auth (chave como usuário, sem senha) —
// é o método que a própria Stripe documenta pra testar uma secret key.
func (g *GenericVerifier) stripe(ctx context.Context, key string) (bool, bool) {
	return g.simpleBasicAuth(ctx, "https://api.stripe.com/v1/balance", key)
}

// github: GET /user — 200 confirma token válido (qualquer escopo já
// autentica esse endpoint), 401 é "Bad credentials" documentado.
func (g *GenericVerifier) github(ctx context.Context, token string) (bool, bool) {
	return g.simpleBearer(ctx, "https://api.github.com/user", token, statusRules{ok200: true})
}

// openAI: GET /v1/models — listar modelos não consome cota, só confirma auth.
func (g *GenericVerifier) openAI(ctx context.Context, key string) (bool, bool) {
	return g.simpleBearer(ctx, "https://api.openai.com/v1/models", key, statusRules{ok200: true})
}

// sendGrid: GET /v3/scopes — endpoint oficial pra descobrir as permissões de
// uma chave, citado na doc deles exatamente como forma de checar uma chave.
func (g *GenericVerifier) sendGrid(ctx context.Context, key string) (bool, bool) {
	return g.simpleBearer(ctx, "https://api.sendgrid.com/v3/scopes", key, statusRules{ok200: true})
}

// slack: POST /api/auth.test — a API do Slack quase sempre responde 200 e
// bota o resultado de verdade no corpo ({"ok": true/false}), então a
// checagem é no JSON, não no status HTTP.
func (g *GenericVerifier) slack(ctx context.Context, token string) (bool, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://slack.com/api/auth.test", nil)
	if err != nil {
		return false, false
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", "secrets-go")
	resp, err := g.http.Do(req)
	if err != nil {
		return false, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, false
	}
	var out struct {
		OK bool `json:"ok"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return false, false
	}
	return true, out.OK
}

// anthropic: GET /v1/models com header x-api-key (não Bearer) + versão
// obrigatória no header.
func (g *GenericVerifier) anthropic(ctx context.Context, key string) (bool, bool) {
	status, err := g.customHeaderGET(ctx, "https://api.anthropic.com/v1/models", map[string]string{
		"x-api-key":         key,
		"anthropic-version": "2023-06-01",
	})
	if err != nil {
		return false, false
	}
	return statusRules{ok200: true}.eval(status)
}

// cloudflare: GET /client/v4/user/tokens/verify — o próprio endpoint se
// chama "verify", feito exatamente pra isso. Testado: token curto demais dá
// 400 (erro de formato, não confirma nada); com formato plausível dá 401
// pra chave inválida — só confiamos em 200/401.
func (g *GenericVerifier) cloudflare(ctx context.Context, token string) (bool, bool) {
	return g.simpleBearer(ctx, "https://api.cloudflare.com/client/v4/user/tokens/verify", token, statusRules{ok200: true})
}

// huggingFace: GET /api/whoami-v2 — endpoint oficial de identidade.
func (g *GenericVerifier) huggingFace(ctx context.Context, token string) (bool, bool) {
	return g.simpleBearer(ctx, "https://huggingface.co/api/whoami-v2", token, statusRules{ok200: true})
}

// digitalOcean: GET /v2/account.
func (g *GenericVerifier) digitalOcean(ctx context.Context, token string) (bool, bool) {
	return g.simpleBearer(ctx, "https://api.digitalocean.com/v2/account", token, statusRules{ok200: true})
}

// vercel: GET /v2/user — a Vercel usa 403 (não 401) pra chave inválida,
// confirmado no corpo: {"error":{"invalidToken":true}}. Só 200/403.
func (g *GenericVerifier) vercel(ctx context.Context, token string) (bool, bool) {
	status, err := g.customHeaderGET(ctx, "https://api.vercel.com/v2/user", map[string]string{
		"Authorization": "Bearer " + token,
	})
	if err != nil {
		return false, false
	}
	switch status {
	case http.StatusOK:
		return true, true
	case http.StatusForbidden:
		return true, false
	default:
		return false, false
	}
}

// netlify: GET /api/v1/user.
func (g *GenericVerifier) netlify(ctx context.Context, token string) (bool, bool) {
	return g.simpleBearer(ctx, "https://api.netlify.com/api/v1/user", token, statusRules{ok200: true})
}

// npmRegistry: GET /-/npm/v1/user — "whoami" oficial do registro do npm.
func (g *GenericVerifier) npmRegistry(ctx context.Context, token string) (bool, bool) {
	return g.simpleBearer(ctx, "https://registry.npmjs.org/-/npm/v1/user", token, statusRules{ok200: true})
}

// heroku: GET /account — exige o Accept versionado da API deles.
func (g *GenericVerifier) heroku(ctx context.Context, token string) (bool, bool) {
	status, err := g.customHeaderGET(ctx, "https://api.heroku.com/account", map[string]string{
		"Authorization": "Bearer " + token,
		"Accept":        "application/vnd.heroku+json; version=3",
	})
	if err != nil {
		return false, false
	}
	return statusRules{ok200: true}.eval(status)
}

// discord: GET /users/@me — o esquema de auth é "Bot <token>", não "Bearer".
func (g *GenericVerifier) discord(ctx context.Context, token string) (bool, bool) {
	status, err := g.customHeaderGET(ctx, "https://discord.com/api/v10/users/@me", map[string]string{
		"Authorization": "Bot " + token,
	})
	if err != nil {
		return false, false
	}
	return statusRules{ok200: true}.eval(status)
}

// mailgun: GET /v3/domains com Basic Auth — usuário literal "api", a chave
// vai como senha (convenção da própria Mailgun).
func (g *GenericVerifier) mailgun(ctx context.Context, key string) (bool, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.mailgun.net/v3/domains", nil)
	if err != nil {
		return false, false
	}
	req.SetBasicAuth("api", key)
	req.Header.Set("User-Agent", "secrets-go")
	resp, err := g.http.Do(req)
	if err != nil {
		return false, false
	}
	defer resp.Body.Close()
	return statusRules{ok200: true}.eval(resp.StatusCode)
}

// groq: GET /openai/v1/models — API compatível com o formato da OpenAI.
func (g *GenericVerifier) groq(ctx context.Context, key string) (bool, bool) {
	return g.simpleBearer(ctx, "https://api.groq.com/openai/v1/models", key, statusRules{ok200: true})
}

// mistral: GET /v1/models.
func (g *GenericVerifier) mistral(ctx context.Context, key string) (bool, bool) {
	return g.simpleBearer(ctx, "https://api.mistral.ai/v1/models", key, statusRules{ok200: true})
}

// replicate: GET /v1/account — o esquema de auth é "Token <token>", não
// "Bearer" (convenção própria deles).
func (g *GenericVerifier) replicate(ctx context.Context, token string) (bool, bool) {
	status, err := g.customHeaderGET(ctx, "https://api.replicate.com/v1/account", map[string]string{
		"Authorization": "Token " + token,
	})
	if err != nil {
		return false, false
	}
	return statusRules{ok200: true}.eval(status)
}

// elevenLabs: GET /v1/user com header próprio "xi-api-key" (não Authorization).
func (g *GenericVerifier) elevenLabs(ctx context.Context, key string) (bool, bool) {
	status, err := g.customHeaderGET(ctx, "https://api.elevenlabs.io/v1/user", map[string]string{
		"xi-api-key": key,
	})
	if err != nil {
		return false, false
	}
	return statusRules{ok200: true}.eval(status)
}

// cohere: GET /v1/models.
func (g *GenericVerifier) cohere(ctx context.Context, key string) (bool, bool) {
	return g.simpleBearer(ctx, "https://api.cohere.ai/v1/models", key, statusRules{ok200: true})
}

// mercadoPago: GET /users/me — testado: token curto demais aciona um bloqueio
// de borda (403 genérico do WAF, não confirma nada), mas com um token de
// formato plausível a resposta de "inválida de verdade" é 401. Só confiamos
// em 200/401 aqui — 403 fica como "não conseguimos confirmar".
//
// O prefixo "APP_USR-" (que normalmente indica produção) NÃO é confiável
// pra saber se é sandbox — testado com várias chaves reais achadas em scan:
// token "APP_USR-..." caiu em conta "TESTUSER..." repetidamente. A única
// forma confiável é olhar o corpo da resposta: contas de teste do Mercado
// Pago sempre têm nickname "TESTUSER..." e email "...@testuser.com".
func (g *GenericVerifier) mercadoPago(ctx context.Context, token string) (checked bool, active bool, sandbox bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.mercadopago.com/users/me", nil)
	if err != nil {
		return false, false, false
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", "secrets-go")
	resp, err := g.http.Do(req)
	if err != nil {
		return false, false, false
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		var out struct {
			Nickname string   `json:"nickname"`
			Email    string   `json:"email"`
			Tags     []string `json:"tags"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return true, true, isMercadoPagoSandboxAccount(out.Nickname, out.Email, out.Tags)
	case http.StatusUnauthorized:
		return true, false, false
	default:
		return false, false, false
	}
}

// isMercadoPagoSandboxAccount reconhece o padrão de conta de teste que o
// próprio Mercado Pago gera automaticamente (extraído do verificador pra dar
// pra testar sem precisar de rede nem de uma credencial real).
func isMercadoPagoSandboxAccount(nickname, email string, tags []string) bool {
	if strings.HasPrefix(nickname, "TESTUSER") {
		return true
	}
	if strings.HasSuffix(strings.ToLower(email), "@testuser.com") {
		return true
	}
	for _, tag := range tags {
		if tag == "test_user" {
			return true
		}
	}
	return false
}

// asaas: GET /v3/myAccount com header próprio "access_token".
func (g *GenericVerifier) asaas(ctx context.Context, key string) (bool, bool) {
	status, err := g.customHeaderGET(ctx, "https://api.asaas.com/v3/myAccount", map[string]string{
		"access_token": key,
	})
	if err != nil {
		return false, false
	}
	return statusRules{ok200: true}.eval(status)
}

// pagarme: GET /core/v5/balance com Basic Auth (chave como usuário, sem
// senha) — mesmo padrão da Stripe, que é o modelo que a API v5 deles seguiu.
func (g *GenericVerifier) pagarme(ctx context.Context, key string) (bool, bool) {
	return g.simpleBasicAuth(ctx, "https://api.pagar.me/core/v5/balance", key)
}

// telegram: GET /bot{token}/getMe — o token vai na URL, não em header. A API
// do Telegram bota o resultado de verdade no corpo ({"ok": true/false}),
// igual ao padrão do Slack, então checamos o JSON, não só o status HTTP.
func (g *GenericVerifier) telegram(ctx context.Context, token string) (bool, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.telegram.org/bot"+token+"/getMe", nil)
	if err != nil {
		return false, false
	}
	req.Header.Set("User-Agent", "secrets-go")
	resp, err := g.http.Do(req)
	if err != nil {
		return false, false
	}
	defer resp.Body.Close()
	var out struct {
		OK bool `json:"ok"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return false, false
	}
	return true, out.OK
}

// gemini: GET /v1beta/models?key=... — a chave vai como query param (não
// header), convenção da Google pras APIs "generativelanguage". 200=válida,
// 400="API key not valid" documentado.
func (g *GenericVerifier) gemini(ctx context.Context, key string) (bool, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://generativelanguage.googleapis.com/v1beta/models?key="+key, nil)
	if err != nil {
		return false, false
	}
	req.Header.Set("User-Agent", "secrets-go")
	resp, err := g.http.Do(req)
	if err != nil {
		return false, false
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return true, true
	case http.StatusBadRequest, http.StatusForbidden:
		return true, false
	default:
		return false, false
	}
}

// square: GET /v2/locations.
func (g *GenericVerifier) square(ctx context.Context, token string) (bool, bool) {
	return g.simpleBearer(ctx, "https://connect.squareup.com/v2/locations", token, statusRules{ok200: true})
}

// clerk: GET /v1/users?limit=1 — endpoint de listagem, mas 1 resultado já
// confirma a secret key sem custo.
func (g *GenericVerifier) clerk(ctx context.Context, key string) (bool, bool) {
	return g.simpleBearer(ctx, "https://api.clerk.com/v1/users?limit=1", key, statusRules{ok200: true})
}

// railway: POST /graphql/v2 (query GraphQL "{ me { id } }") — a API deles é
// só GraphQL, sem REST. Erro de auth vem como 200 + {"errors":[...]} (padrão
// GraphQL de não usar status HTTP pra erro de negócio), então checamos o
// corpo, não só o status.
func (g *GenericVerifier) railway(ctx context.Context, token string) (bool, bool) {
	body := bytes.NewBufferString(`{"query":"{ me { id } }"}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://backboard.railway.app/graphql/v2", body)
	if err != nil {
		return false, false
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "secrets-go")
	resp, err := g.http.Do(req)
	if err != nil {
		return false, false
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return true, false
	}
	if resp.StatusCode != http.StatusOK {
		return false, false
	}
	var out struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return false, false
	}
	return true, len(out.Errors) == 0
}

// firebase: GET /identitytoolkit/v3/relyingparty/getRecaptchaParam?key=... —
// endpoint só-leitura (não cria conta nem gasta cota), usado exatamente pra
// validar API key. 200=válida, 400="API key not valid" documentado.
func (g *GenericVerifier) firebase(ctx context.Context, key string) (bool, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://www.googleapis.com/identitytoolkit/v3/relyingparty/getRecaptchaParam?key="+key, nil)
	if err != nil {
		return false, false
	}
	req.Header.Set("User-Agent", "secrets-go")
	resp, err := g.http.Do(req)
	if err != nil {
		return false, false
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return true, true
	case http.StatusBadRequest:
		return true, false
	default:
		return false, false
	}
}

// openRouter: GET /api/v1/auth/key — endpoint oficial pra checar a própria
// chave (devolve limite/uso quando válida).
func (g *GenericVerifier) openRouter(ctx context.Context, key string) (bool, bool) {
	return g.simpleBearer(ctx, "https://openrouter.ai/api/v1/auth/key", key, statusRules{ok200: true})
}

// resend: GET /domains — chave inválida dá 400 (não 401), testado
// manualmente (corpo: {"statusCode":400,"message":"API key is invalid"}).
func (g *GenericVerifier) resend(ctx context.Context, key string) (bool, bool) {
	status, err := g.bearerGET(ctx, "https://api.resend.com/domains", key)
	if err != nil {
		return false, false
	}
	switch status {
	case http.StatusOK:
		return true, true
	case http.StatusBadRequest:
		return true, false
	default:
		return false, false
	}
}

// notion: GET /v1/users/me — exige o header de versão da API (obrigatório
// em toda chamada, não só nessa).
func (g *GenericVerifier) notion(ctx context.Context, key string) (bool, bool) {
	status, err := g.customHeaderGET(ctx, "https://api.notion.com/v1/users/me", map[string]string{
		"Authorization":  "Bearer " + key,
		"Notion-Version": "2022-06-28",
	})
	if err != nil {
		return false, false
	}
	return statusRules{ok200: true}.eval(status)
}

// figma: GET /v1/me com header próprio "X-Figma-Token" (não Authorization).
// Chave inválida dá 403 (não 401), testado manualmente (corpo:
// {"status":403,"err":"Invalid token"}).
func (g *GenericVerifier) figma(ctx context.Context, token string) (bool, bool) {
	status, err := g.customHeaderGET(ctx, "https://api.figma.com/v1/me", map[string]string{
		"X-Figma-Token": token,
	})
	if err != nil {
		return false, false
	}
	switch status {
	case http.StatusOK:
		return true, true
	case http.StatusForbidden:
		return true, false
	default:
		return false, false
	}
}

// sentry: GET /api/0/organizations/ — lista as orgs que o token enxerga.
func (g *GenericVerifier) sentry(ctx context.Context, token string) (bool, bool) {
	return g.simpleBearer(ctx, "https://sentry.io/api/0/organizations/", token, statusRules{ok200: true})
}

// datadog: GET /api/v1/validate com header próprio "DD-API-KEY". Chave
// inválida dá 403 (não 401) — documentado.
func (g *GenericVerifier) datadog(ctx context.Context, key string) (bool, bool) {
	status, err := g.customHeaderGET(ctx, "https://api.datadoghq.com/api/v1/validate", map[string]string{
		"DD-API-KEY": key,
	})
	if err != nil {
		return false, false
	}
	switch status {
	case http.StatusOK:
		return true, true
	case http.StatusForbidden:
		return true, false
	default:
		return false, false
	}
}

// circleci: GET /api/v2/me com header próprio "Circle-Token".
func (g *GenericVerifier) circleci(ctx context.Context, token string) (bool, bool) {
	status, err := g.customHeaderGET(ctx, "https://circleci.com/api/v2/me", map[string]string{
		"Circle-Token": token,
	})
	if err != nil {
		return false, false
	}
	return statusRules{ok200: true}.eval(status)
}

// gitlab: GET /api/v4/user.
func (g *GenericVerifier) gitlab(ctx context.Context, token string) (bool, bool) {
	return g.simpleBearer(ctx, "https://gitlab.com/api/v4/user", token, statusRules{ok200: true})
}

// together: GET /v1/models — API compatível com o formato da OpenAI.
func (g *GenericVerifier) together(ctx context.Context, key string) (bool, bool) {
	return g.simpleBearer(ctx, "https://api.together.xyz/v1/models", key, statusRules{ok200: true})
}

// fireworks: GET /inference/v1/models — API compatível com o formato da OpenAI.
func (g *GenericVerifier) fireworks(ctx context.Context, key string) (bool, bool) {
	return g.simpleBearer(ctx, "https://api.fireworks.ai/inference/v1/models", key, statusRules{ok200: true})
}

// xai: GET /v1/models (xAI/Grok) — API compatível com o formato da OpenAI,
// mas chave inválida dá 400 (não 401), testado manualmente (corpo:
// {"code":"invalid-argument","error":"Incorrect API key provided..."}).
func (g *GenericVerifier) xai(ctx context.Context, key string) (bool, bool) {
	status, err := g.bearerGET(ctx, "https://api.x.ai/v1/models", key)
	if err != nil {
		return false, false
	}
	switch status {
	case http.StatusOK:
		return true, true
	case http.StatusBadRequest:
		return true, false
	default:
		return false, false
	}
}

// deepseek: GET /user/balance — endpoint oficial deles pra checar
// crédito/validade da chave (não têm um "/models" que autentique sozinho).
func (g *GenericVerifier) deepseek(ctx context.Context, key string) (bool, bool) {
	return g.simpleBearer(ctx, "https://api.deepseek.com/user/balance", key, statusRules{ok200: true})
}

// stability: GET /v1/user/account — endpoint oficial de identidade da conta.
func (g *GenericVerifier) stability(ctx context.Context, key string) (bool, bool) {
	return g.simpleBearer(ctx, "https://api.stability.ai/v1/user/account", key, statusRules{ok200: true})
}

// deepgram: GET /v1/projects — esquema de auth "Token <key>", não "Bearer"
// (convenção própria deles).
func (g *GenericVerifier) deepgram(ctx context.Context, key string) (bool, bool) {
	status, err := g.customHeaderGET(ctx, "https://api.deepgram.com/v1/projects", map[string]string{
		"Authorization": "Token " + key,
	})
	if err != nil {
		return false, false
	}
	return statusRules{ok200: true}.eval(status)
}

// assemblyAI: GET /v2/transcript — o header é a chave crua, sem prefixo
// "Bearer"/"Token" (convenção própria deles).
func (g *GenericVerifier) assemblyAI(ctx context.Context, key string) (bool, bool) {
	status, err := g.customHeaderGET(ctx, "https://api.assemblyai.com/v2/transcript", map[string]string{
		"authorization": key,
	})
	if err != nil {
		return false, false
	}
	return statusRules{ok200: true}.eval(status)
}

// statusRules descreve quais códigos HTTP contam como "chave ativa" além do
// 200 padrão — cada provedor documenta o próprio jeito de sinalizar
// "válida mas sem permissão pra esse recurso específico".
type statusRules struct {
	ok200        bool
	forbidden403 bool
}

func (r statusRules) eval(status int) (bool, bool) {
	switch {
	case status == http.StatusOK && r.ok200:
		return true, true
	case status == http.StatusForbidden && r.forbidden403:
		return true, true
	case status == http.StatusUnauthorized:
		return true, false
	default:
		return false, false
	}
}

func (g *GenericVerifier) simpleBearer(ctx context.Context, url, token string, rules statusRules) (bool, bool) {
	status, err := g.bearerGET(ctx, url, token)
	if err != nil {
		return false, false
	}
	return rules.eval(status)
}

func (g *GenericVerifier) simpleBasicAuth(ctx context.Context, url, key string) (bool, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, false
	}
	req.SetBasicAuth(key, "")
	req.Header.Set("User-Agent", "secrets-go")
	resp, err := g.http.Do(req)
	if err != nil {
		return false, false
	}
	defer resp.Body.Close()
	return statusRules{ok200: true}.eval(resp.StatusCode)
}

func (g *GenericVerifier) bearerGET(ctx context.Context, url, token string) (int, error) {
	return g.customHeaderGET(ctx, url, map[string]string{"Authorization": "Bearer " + token})
}

func (g *GenericVerifier) customHeaderGET(ctx context.Context, url string, headers map[string]string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("User-Agent", "secrets-go")
	resp, err := g.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	return resp.StatusCode, nil
}
