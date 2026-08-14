package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Tools de observabilidade e administração (logs Loki, Sentry, cargos
// personalizados, roteador de chaves de IA, banimento de IP, clients OAuth e
// biblioteca de modelos 3D). Admin-only — a API central decide o papel exigido.
// Algumas tools aqui gravam ou removem credenciais reais (api_router_key_*) ou
// fazem exclusões irreversíveis; a Description de cada uma avisa quando é o caso.

// ── logs (Loki) ─────────────────────────────────────────────────────────────

type logsQueryInput struct {
	Apps        string `json:"apps,omitempty" jsonschema:"filtra por serviço(s), nomes separados por vírgula (ex: api-go,mcp-go); use logs_labels para ver os disponíveis"`
	Level       string `json:"level,omitempty" jsonschema:"nível: debug, info, warn ou error"`
	StatusClass string `json:"statusClass,omitempty" jsonschema:"classe de status HTTP: 2xx, 3xx, 4xx ou 5xx"`
	Status      int    `json:"status,omitempty" jsonschema:"código HTTP exato (ex: 404)"`
	Method      string `json:"method,omitempty" jsonschema:"método HTTP (GET, POST...)"`
	Path        string `json:"path,omitempty" jsonschema:"filtra por path da rota"`
	RequestID   string `json:"requestId,omitempty" jsonschema:"filtra por um request_id específico"`
	User        string `json:"user,omitempty" jsonschema:"filtra por id numérico do usuário, ou substring do nome"`
	Search      string `json:"q,omitempty" jsonschema:"busca texto livre no corpo da linha de log"`
	MinDurMs    int    `json:"minDur,omitempty" jsonschema:"só requisições com duração >= este valor em ms"`
	HTTPOnly    bool   `json:"httpOnly,omitempty" jsonschema:"true = só linhas de requisição HTTP (têm method/status)"`
	Range       string `json:"range,omitempty" jsonschema:"janela de tempo: 15m, 1h (padrão), 6h, 24h ou 7d"`
	Limit       int    `json:"limit,omitempty" jsonschema:"máximo de linhas por página (padrão 50)"`
	Before      string `json:"before,omitempty" jsonschema:"cursor de paginação: página anterior (devolvido pela chamada anterior)"`
	After       string `json:"after,omitempty" jsonschema:"cursor de paginação: próxima página (devolvido pela chamada anterior)"`
}

type logsTopIPsInput struct {
	Range string `json:"range,omitempty" jsonschema:"janela de tempo: 15m, 1h (padrão), 6h, 24h ou 7d"`
	Limit int    `json:"limit,omitempty" jsonschema:"quantidade de IPs no ranking (padrão 20, máx 50)"`
}

// ── sentry ───────────────────────────────────────────────────────────────────

type sentryIssuesListInput struct {
	Project     string `json:"project,omitempty" jsonschema:"slug do projeto/serviço no Sentry (vazio = todos); use sentry_projects_list para ver os disponíveis"`
	Status      string `json:"status,omitempty" jsonschema:"unresolved (padrão), resolved, ignored, ou vazio para todas"`
	Search      string `json:"q,omitempty" jsonschema:"busca texto livre (query do Sentry)"`
	StatsPeriod string `json:"statsPeriod,omitempty" jsonschema:"janela do gráfico de eventos: 24h, 14d (padrão) ou 90d"`
	Limit       int    `json:"limit,omitempty" jsonschema:"máximo de issues (padrão 25, teto 100)"`
}

// sentryIssueGetInput: com id detalha a issue (os filtros de listagem abaixo
// são ignorados); sem id lista, e aí os filtros valem.
type sentryIssueGetInput struct {
	ID string `json:"id,omitempty" jsonschema:"id da issue no Sentry; se informado, os demais campos (filtros de lista) são ignorados"`
	sentryIssuesListInput
}

// ── custom roles ─────────────────────────────────────────────────────────────

type customRoleCreateInput struct {
	Name        string              `json:"name" jsonschema:"nome do cargo"`
	Description *string             `json:"description,omitempty" jsonschema:"descrição opcional do cargo"`
	Permissions map[string][]string `json:"permissions,omitempty" jsonschema:"mapa de permissões (recurso -> lista de ações); se omitido, o cargo nasce SEM NENHUMA permissão"`
}

type customRoleUpdateInput struct {
	ID          string              `json:"id" jsonschema:"id (uuid) do cargo"`
	Name        string              `json:"name" jsonschema:"nome do cargo — OBRIGATÓRIO mesmo em update: a rota substitui o registro inteiro, não faz merge parcial"`
	Description *string             `json:"description,omitempty" jsonschema:"descrição; se omitida, a descrição existente é apagada"`
	Permissions map[string][]string `json:"permissions,omitempty" jsonschema:"mapa de permissões; se omitido, ZERA as permissões existentes do cargo"`
}

type customRoleIDInput struct {
	ID string `json:"id" jsonschema:"id (uuid) do cargo"`
}

type customRoleGetInput struct {
	ID string `json:"id,omitempty" jsonschema:"id (uuid) do cargo; omitido lista todos os cargos personalizados"`
}

// ── api-router (chaves de provedores de IA) ─────────────────────────────────

type apiRouterProviderCreateInput struct {
	Name              string `json:"name" jsonschema:"nome único do provider (ex: openai, anthropic)"`
	BaseURL           string `json:"baseUrl" jsonschema:"URL base do provider de IA"`
	AuthHeader        string `json:"authHeader,omitempty" jsonschema:"nome do header de autenticação enviado nas requisições (padrão: Authorization)"`
	AuthScheme        string `json:"authScheme,omitempty" jsonschema:"prefixo do valor do header, ex: Bearer (vazio = sem prefixo)"`
	UnauthorizedCodes []int  `json:"unauthorizedCodes,omitempty" jsonschema:"códigos HTTP que marcam uma chave como não-autorizada (padrão: [401])"`
	NoCreditCodes     []int  `json:"noCreditCodes,omitempty" jsonschema:"códigos HTTP que marcam uma chave sem créditos (padrão: [402, 429])"`
	TestPath          string `json:"testPath,omitempty" jsonschema:"path usado por api_router_key_test para validar uma chave"`
	TestMethod        string `json:"testMethod,omitempty" jsonschema:"método HTTP do teste (padrão: GET)"`
}

type apiRouterProviderUpdateInput struct {
	ID                string `json:"id" jsonschema:"id do provider"`
	Name              string `json:"name" jsonschema:"nome único do provider — OBRIGATÓRIO mesmo em update: a rota substitui o registro inteiro"`
	BaseURL           string `json:"baseUrl" jsonschema:"URL base do provider de IA — OBRIGATÓRIO mesmo em update"`
	AuthHeader        string `json:"authHeader,omitempty" jsonschema:"nome do header de autenticação (vazio volta ao padrão Authorization)"`
	AuthScheme        string `json:"authScheme,omitempty" jsonschema:"prefixo do valor do header, ex: Bearer"`
	UnauthorizedCodes []int  `json:"unauthorizedCodes,omitempty" jsonschema:"códigos HTTP de chave não-autorizada (vazio volta ao padrão [401])"`
	NoCreditCodes     []int  `json:"noCreditCodes,omitempty" jsonschema:"códigos HTTP de chave sem créditos (vazio volta ao padrão [402, 429])"`
	TestPath          string `json:"testPath,omitempty" jsonschema:"path usado por api_router_key_test"`
	TestMethod        string `json:"testMethod,omitempty" jsonschema:"método HTTP do teste (vazio volta ao padrão GET)"`
}

type apiRouterProviderIDInput struct {
	ID string `json:"id" jsonschema:"id do provider (ver api_router_providers_list)"`
}

type apiRouterKeyCreateInput struct {
	ProviderID string `json:"providerId" jsonschema:"id do provider dono da chave"`
	Label      string `json:"label" jsonschema:"rótulo da chave, só para identificação (ex: conta-principal)"`
	Secret     string `json:"secret" jsonschema:"valor REAL da chave/token de API do provedor — vai cifrado para o banco, mas trafega em texto puro nesta chamada"`
	Priority   int    `json:"priority,omitempty" jsonschema:"prioridade de uso entre chaves do mesmo provider (menor valor = usada primeiro); padrão 0"`
}

type apiRouterKeyUpdateInput struct {
	ProviderID string `json:"providerId" jsonschema:"id do provider"`
	KeyID      string `json:"keyId" jsonschema:"id da chave"`
	Label      string `json:"label" jsonschema:"novo rótulo — OBRIGATÓRIO mesmo em update, a rota sobrescreve label e priority juntos"`
	Priority   int    `json:"priority,omitempty" jsonschema:"nova prioridade — se omitida, vira 0 (a rota não preserva o valor atual)"`
}

type apiRouterKeyIDInput struct {
	ProviderID string `json:"providerId" jsonschema:"id do provider"`
	KeyID      string `json:"keyId" jsonschema:"id da chave"`
}

type apiRouterKeyStatusInput struct {
	ProviderID string `json:"providerId" jsonschema:"id do provider"`
	KeyID      string `json:"keyId" jsonschema:"id da chave"`
	Status     string `json:"status" jsonschema:"active (reativa) ou disabled (desliga manualmente); os status unauthorized/no_credits só o roteador atribui sozinho"`
}

// ── ip bans ──────────────────────────────────────────────────────────────────

type ipBanCreateInput struct {
	IP        string `json:"ip" jsonschema:"endereço IP a banir (IPv4 ou IPv6)"`
	Reason    string `json:"reason,omitempty" jsonschema:"motivo do banimento"`
	ExpiresAt string `json:"expires_at,omitempty" jsonschema:"expiração em RFC3339 (ex: 2026-09-01T00:00:00Z); vazio = ban permanente"`
}

type ipBanIDInput struct {
	ID string `json:"id" jsonschema:"id numérico do ban (ver ip_bans_list)"`
}

// ── oauth clients ────────────────────────────────────────────────────────────

type oauthClientCreateInput struct {
	ClientID     string   `json:"clientId" jsonschema:"identificador único do client (1-64 chars alfanuméricos, _ ou -)"`
	Name         string   `json:"name" jsonschema:"nome de exibição da aplicação"`
	RedirectURIs []string `json:"redirectUris" jsonschema:"URIs de redirect permitidas (http(s) completo ou deep link de app, ex: santostech://auth)"`
}

type oauthClientUpdateInput struct {
	ID           string   `json:"id" jsonschema:"id (uuid) do client"`
	Name         *string  `json:"name,omitempty" jsonschema:"novo nome de exibição"`
	RedirectURIs []string `json:"redirectUris,omitempty" jsonschema:"lista COMPLETA de redirect URIs (substitui a lista inteira; omitir mantém a atual)"`
	IsActive     *bool    `json:"isActive,omitempty" jsonschema:"true reativa, false desativa a aplicação"`
}

type oauthClientIDInput struct {
	ID string `json:"id" jsonschema:"id (uuid) do client"`
}

// ── models3d ─────────────────────────────────────────────────────────────────

// maxModel3DUploadBytes é o teto do endpoint de destino (50MB para modelos 3D,
// bem maior que o de imagens) — não reutiliza maxUploadBytes de tools_upload.go.
const maxModel3DUploadBytes = 50 << 20

// model3DUploadTimeout é maior que defaultCallTimeout porque o corpo pode
// chegar a 50MB — 30s pode não bastar para baixar a URL e depois subir o
// multipart inteiro para a API central.
const model3DUploadTimeout = 120 * time.Second

type model3DUploadInput struct {
	FileURL    string `json:"fileUrl,omitempty" jsonschema:"URL pública (http/https) do arquivo — PREFIRA este campo: o servidor baixa direto"`
	FileBase64 string `json:"fileBase64,omitempty" jsonschema:"alternativa só para arquivos PEQUENOS: conteúdo em base64 (aceita data URI); para qualquer outro use fileUrl"`
	Filename   string `json:"filename" jsonschema:"nome do arquivo COM extensão (glb, gltf, stl, obj, fbx ou blend) — a API usa a extensão para validar o formato, não tem como inferir com segurança a partir da fileUrl"`
	Folder     string `json:"folder,omitempty" jsonschema:"pasta opcional para organizar a biblioteca (máx 60 caracteres)"`
}

type model3DUpdateInput struct {
	ID       string  `json:"id" jsonschema:"id do modelo"`
	Filename *string `json:"filename,omitempty" jsonschema:"novo nome de exibição (não altera a extensão real do arquivo armazenado)"`
	Folder   *string `json:"folder,omitempty" jsonschema:"pasta (máx 60 caracteres); string vazia remove o modelo de qualquer pasta"`
	Pinned   *bool   `json:"pinned,omitempty" jsonschema:"fixar (true) ou desfixar (false) o modelo na biblioteca"`
}

type model3DIDInput struct {
	ID string `json:"id" jsonschema:"id do modelo (ver models3d_list)"`
}

func (s *Server) addAdminTools(srv *mcp.Server) {
	authAdmin := s.cfg.AuthBaseURL + "/auth/admin"
	apiRouterBase := authAdmin + "/api-router"

	// ── logs ───────────────────────────────────────────────────────────────

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "logs_query",
		Description: "Busca logs estruturados no Loki com filtros e paginação por cursor (GET /logs).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in logsQueryInput) (*mcp.CallToolResult, any, error) {
		qs := url.Values{}
		if in.Apps != "" {
			for _, a := range strings.Split(in.Apps, ",") {
				if a = strings.TrimSpace(a); a != "" {
					qs.Add("app", a)
				}
			}
		}
		setIf(qs, "level", in.Level)
		setIf(qs, "status_class", in.StatusClass)
		if in.Status != 0 {
			qs.Set("status", strconv.Itoa(in.Status))
		}
		setIf(qs, "method", in.Method)
		setIf(qs, "path", in.Path)
		setIf(qs, "request_id", in.RequestID)
		setIf(qs, "user", in.User)
		setIf(qs, "q", in.Search)
		if in.MinDurMs != 0 {
			qs.Set("min_dur", strconv.Itoa(in.MinDurMs))
		}
		if in.HTTPOnly {
			qs.Set("http_only", "1")
		}
		setIf(qs, "range", in.Range)
		if in.Limit != 0 {
			qs.Set("limit", strconv.Itoa(in.Limit))
		}
		setIf(qs, "before", in.Before)
		setIf(qs, "after", in.After)
		return s.proxy(ctx, req, "GET", withQuery(s.cfg.AuthBaseURL+"/logs", qs), nil)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "logs_labels",
		Description: "Lista os serviços (label app) disponíveis para filtrar em logs_query (GET /logs/labels).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, any, error) {
		return s.proxy(ctx, req, "GET", s.cfg.AuthBaseURL+"/logs/labels", nil)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "logs_top_ips",
		Description: "Ranking dos IPs com mais requisições no período (GET /logs/top-ips).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in logsTopIPsInput) (*mcp.CallToolResult, any, error) {
		qs := url.Values{}
		setIf(qs, "range", in.Range)
		if in.Limit != 0 {
			qs.Set("limit", strconv.Itoa(in.Limit))
		}
		return s.proxy(ctx, req, "GET", withQuery(s.cfg.AuthBaseURL+"/logs/top-ips", qs), nil)
	})

	// ── sentry ─────────────────────────────────────────────────────────────

	mcp.AddTool(srv, &mcp.Tool{
		Name: "sentry_issue_get",
		Description: "Sem id: lista issues (erros) dos projetos do Sentry do ecossistema (GET /sentry/issues), com filtros. " +
			"Com id: detalha uma issue — metadados, stacktrace do evento mais recente e tags (GET /sentry/issues/{id}).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in sentryIssueGetInput) (*mcp.CallToolResult, any, error) {
		if in.ID != "" {
			return s.proxy(ctx, req, "GET", s.cfg.AuthBaseURL+"/sentry/issues/"+url.PathEscape(in.ID), nil)
		}
		qs := url.Values{}
		setIf(qs, "project", in.Project)
		setIf(qs, "status", in.Status)
		setIf(qs, "q", in.Search)
		setIf(qs, "stats_period", in.StatsPeriod)
		if in.Limit != 0 {
			qs.Set("limit", strconv.Itoa(in.Limit))
		}
		return s.proxy(ctx, req, "GET", withQuery(s.cfg.AuthBaseURL+"/sentry/issues", qs), nil)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "sentry_projects_list",
		Description: "Lista os slugs de projeto conhecidos do Sentry, para popular filtros (GET /sentry/projects).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, any, error) {
		return s.proxy(ctx, req, "GET", s.cfg.AuthBaseURL+"/sentry/projects", nil)
	})

	// ── custom roles ───────────────────────────────────────────────────────

	mcp.AddTool(srv, &mcp.Tool{
		Name: "custom_role_get",
		Description: "Sem id: lista os cargos personalizados do ecossistema (GET /auth/admin/custom-roles). " +
			"Com id: detalha um cargo, incluindo o mapa de permissões (GET /auth/admin/custom-roles/{id}).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in customRoleGetInput) (*mcp.CallToolResult, any, error) {
		if in.ID == "" {
			return s.proxy(ctx, req, "GET", authAdmin+"/custom-roles", nil)
		}
		return s.proxy(ctx, req, "GET", authAdmin+"/custom-roles/"+url.PathEscape(in.ID), nil)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "custom_role_create",
		Description: "Cria um cargo personalizado com um mapa de permissões (POST /auth/admin/custom-roles).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in customRoleCreateInput) (*mcp.CallToolResult, any, error) {
		if strings.TrimSpace(in.Name) == "" {
			return errResult("name é obrigatório"), nil, nil
		}
		body := map[string]any{"name": strings.TrimSpace(in.Name)}
		if in.Description != nil {
			body["description"] = *in.Description
		}
		if in.Permissions != nil {
			body["permissions"] = in.Permissions
		}
		return s.proxy(ctx, req, "POST", authAdmin+"/custom-roles", body)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name: "custom_role_update",
		Description: "Substitui um cargo personalizado (PATCH /auth/admin/custom-roles/{id}). Não é merge parcial: " +
			"name é sempre obrigatório e permissions omitido ZERA as permissões existentes.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in customRoleUpdateInput) (*mcp.CallToolResult, any, error) {
		if strings.TrimSpace(in.Name) == "" {
			return errResult("name é obrigatório"), nil, nil
		}
		body := map[string]any{"name": strings.TrimSpace(in.Name)}
		if in.Description != nil {
			body["description"] = *in.Description
		}
		if in.Permissions != nil {
			body["permissions"] = in.Permissions
		}
		return s.proxy(ctx, req, "PATCH", authAdmin+"/custom-roles/"+url.PathEscape(in.ID), body)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "custom_role_delete",
		Description: "Remove um cargo personalizado (DELETE /auth/admin/custom-roles/{id}). Ação irreversível — usuários com este cargo perdem as permissões associadas.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in customRoleIDInput) (*mcp.CallToolResult, any, error) {
		return s.proxy(ctx, req, "DELETE", authAdmin+"/custom-roles/"+url.PathEscape(in.ID), nil)
	})

	// ── api-router (providers) ────────────────────────────────────────────

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "api_router_providers_list",
		Description: "Lista os providers de IA cadastrados no roteador de chaves, com contagem de chaves por status (GET /auth/admin/api-router/providers).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, any, error) {
		return s.proxy(ctx, req, "GET", apiRouterBase+"/providers", nil)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "api_router_provider_create",
		Description: "Cadastra um provider de IA no roteador de chaves (POST /auth/admin/api-router/providers).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in apiRouterProviderCreateInput) (*mcp.CallToolResult, any, error) {
		if in.Name == "" || in.BaseURL == "" {
			return errResult("name e baseUrl são obrigatórios"), nil, nil
		}
		return s.proxy(ctx, req, "POST", apiRouterBase+"/providers", in)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name: "api_router_provider_update",
		Description: "Substitui um provider de IA (PATCH /auth/admin/api-router/providers/{id}). Não é merge parcial: " +
			"name e baseUrl são sempre obrigatórios; os demais campos omitidos voltam ao padrão.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in apiRouterProviderUpdateInput) (*mcp.CallToolResult, any, error) {
		if in.Name == "" || in.BaseURL == "" {
			return errResult("name e baseUrl são obrigatórios"), nil, nil
		}
		body := map[string]any{
			"name": in.Name, "baseUrl": in.BaseURL, "authHeader": in.AuthHeader, "authScheme": in.AuthScheme,
			"unauthorizedCodes": in.UnauthorizedCodes, "noCreditCodes": in.NoCreditCodes,
			"testPath": in.TestPath, "testMethod": in.TestMethod,
		}
		return s.proxy(ctx, req, "PATCH", apiRouterBase+"/providers/"+url.PathEscape(in.ID), body)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name: "api_router_provider_delete",
		Description: "Remove um provider de IA do roteador (DELETE /auth/admin/api-router/providers/{id}). Ação irreversível — " +
			"confira antes se ele ainda tem chaves cadastradas (api_router_keys_list).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in apiRouterProviderIDInput) (*mcp.CallToolResult, any, error) {
		return s.proxy(ctx, req, "DELETE", apiRouterBase+"/providers/"+url.PathEscape(in.ID), nil)
	})

	// ── api-router (keys) ────────────────────────────────────────────────

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "api_router_keys_list",
		Description: "Lista as chaves de um provider de IA (sem expor o segredo, só a cauda mascarada) (GET /auth/admin/api-router/providers/{id}/keys).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in apiRouterProviderIDInput) (*mcp.CallToolResult, any, error) {
		return s.proxy(ctx, req, "GET", apiRouterBase+"/providers/"+url.PathEscape(in.ID)+"/keys", nil)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name: "api_router_key_create",
		Description: "GRAVA UMA CREDENCIAL REAL: cadastra uma chave/token de API de um provedor de IA (POST " +
			".../providers/{id}/keys). O valor de secret é a credencial de produção — trate como confidencial.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in apiRouterKeyCreateInput) (*mcp.CallToolResult, any, error) {
		if in.Label == "" || in.Secret == "" {
			return errResult("label e secret são obrigatórios"), nil, nil
		}
		body := map[string]any{"label": in.Label, "secret": in.Secret, "priority": in.Priority}
		return s.proxy(ctx, req, "POST", apiRouterBase+"/providers/"+url.PathEscape(in.ProviderID)+"/keys", body)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name: "api_router_key_update",
		Description: "GRAVA CREDENCIAL: atualiza rótulo e prioridade de uma chave de API (PATCH .../keys/{keyId}). Não altera " +
			"o valor secreto. Não é merge parcial: se priority não for enviada, ela é ZERADA.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in apiRouterKeyUpdateInput) (*mcp.CallToolResult, any, error) {
		if in.Label == "" {
			return errResult("label é obrigatório"), nil, nil
		}
		body := map[string]any{"label": in.Label, "priority": in.Priority}
		u := apiRouterBase + "/providers/" + url.PathEscape(in.ProviderID) + "/keys/" + url.PathEscape(in.KeyID)
		return s.proxy(ctx, req, "PATCH", u, body)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name: "api_router_key_delete",
		Description: "Remove uma credencial de API do roteador (DELETE .../keys/{keyId}). Ação IRREVERSÍVEL — se a chave " +
			"ainda estiver em uso, requisições que dependem dela passam a falhar.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in apiRouterKeyIDInput) (*mcp.CallToolResult, any, error) {
		u := apiRouterBase + "/providers/" + url.PathEscape(in.ProviderID) + "/keys/" + url.PathEscape(in.KeyID)
		return s.proxy(ctx, req, "DELETE", u, nil)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "api_router_key_status_set",
		Description: "Ativa ou desativa manualmente uma chave de API (POST .../keys/{keyId}/status).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in apiRouterKeyStatusInput) (*mcp.CallToolResult, any, error) {
		if in.Status != "active" && in.Status != "disabled" {
			return errResult("status deve ser active ou disabled"), nil, nil
		}
		u := apiRouterBase + "/providers/" + url.PathEscape(in.ProviderID) + "/keys/" + url.PathEscape(in.KeyID) + "/status"
		return s.proxy(ctx, req, "POST", u, map[string]any{"status": in.Status})
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name: "api_router_key_test",
		Description: "Dispara uma requisição real contra o provider usando esta chave específica (mesmo que não esteja " +
			"active) e grava o resultado (POST .../keys/{keyId}/test).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in apiRouterKeyIDInput) (*mcp.CallToolResult, any, error) {
		u := apiRouterBase + "/providers/" + url.PathEscape(in.ProviderID) + "/keys/" + url.PathEscape(in.KeyID) + "/test"
		return s.proxy(ctx, req, "POST", u, nil)
	})

	// ── ip bans ──────────────────────────────────────────────────────────

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "ip_bans_list",
		Description: "Lista os banimentos de IP ativos (GET /auth/admin/ip-bans).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, any, error) {
		return s.proxy(ctx, req, "GET", authAdmin+"/ip-bans", nil)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "ip_ban_create",
		Description: "Bane um endereço IP, opcionalmente com expiração (POST /auth/admin/ip-bans). Sincroniza no Redis imediatamente.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in ipBanCreateInput) (*mcp.CallToolResult, any, error) {
		if in.IP == "" {
			return errResult("ip é obrigatório"), nil, nil
		}
		body := map[string]any{"ip": in.IP}
		if in.Reason != "" {
			body["reason"] = in.Reason
		}
		if in.ExpiresAt != "" {
			body["expires_at"] = in.ExpiresAt
		}
		return s.proxy(ctx, req, "POST", authAdmin+"/ip-bans", body)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "ip_ban_delete",
		Description: "Remove um banimento de IP (DELETE /auth/admin/ip-bans/{id}). O IP volta a ter acesso normal imediatamente.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in ipBanIDInput) (*mcp.CallToolResult, any, error) {
		return s.proxy(ctx, req, "DELETE", authAdmin+"/ip-bans/"+url.PathEscape(in.ID), nil)
	})

	// ── oauth clients ────────────────────────────────────────────────────

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "oauth_clients_list",
		Description: "Lista as aplicações OAuth cadastradas para \"Entrar com Santos Tech\" (GET /auth/admin/oauth-clients).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, any, error) {
		return s.proxy(ctx, req, "GET", authAdmin+"/oauth-clients", nil)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "oauth_client_create",
		Description: "Cadastra uma aplicação OAuth (\"Entrar com Santos Tech\") (POST /auth/admin/oauth-clients).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in oauthClientCreateInput) (*mcp.CallToolResult, any, error) {
		if in.ClientID == "" || in.Name == "" || len(in.RedirectURIs) == 0 {
			return errResult("clientId, name e redirectUris são obrigatórios"), nil, nil
		}
		return s.proxy(ctx, req, "POST", authAdmin+"/oauth-clients", in)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "oauth_client_update",
		Description: "Atualiza uma aplicação OAuth (PATCH /auth/admin/oauth-clients/{id}). Só os campos enviados mudam.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in oauthClientUpdateInput) (*mcp.CallToolResult, any, error) {
		body := map[string]any{}
		if in.Name != nil {
			body["name"] = *in.Name
		}
		if in.RedirectURIs != nil {
			body["redirectUris"] = in.RedirectURIs
		}
		if in.IsActive != nil {
			body["isActive"] = *in.IsActive
		}
		if len(body) == 0 {
			return errResult("nenhum campo para atualizar (name, redirectUris, isActive)"), nil, nil
		}
		return s.proxy(ctx, req, "PATCH", authAdmin+"/oauth-clients/"+url.PathEscape(in.ID), body)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name: "oauth_client_delete",
		Description: "Remove uma aplicação OAuth (DELETE /auth/admin/oauth-clients/{id}). Ação irreversível — qualquer " +
			"integração \"Entrar com Santos Tech\" usando este client_id para de funcionar.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in oauthClientIDInput) (*mcp.CallToolResult, any, error) {
		return s.proxy(ctx, req, "DELETE", authAdmin+"/oauth-clients/"+url.PathEscape(in.ID), nil)
	})

	// ── models3d ─────────────────────────────────────────────────────────

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "models3d_list",
		Description: "Lista os modelos 3D da biblioteca (GET /auth/admin/models3d).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, any, error) {
		return s.proxy(ctx, req, "GET", authAdmin+"/models3d", nil)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name: "model3d_upload",
		Description: "Sobe um modelo 3D (glb/gltf/stl/obj/fbx/blend, máx 50MB) para a biblioteca (POST /auth/admin/models3d, multipart). " +
			"Informe fileUrl (preferido — o servidor baixa direto) OU fileBase64 (só para arquivos pequenos), e sempre filename com a extensão correta.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in model3DUploadInput) (*mcp.CallToolResult, any, error) {
		if in.Filename == "" {
			return errResult("filename é obrigatório (com extensão: glb, gltf, stl, obj, fbx ou blend)"), nil, nil
		}
		if in.FileURL != "" && in.FileBase64 != "" {
			return errResult("informe fileUrl OU fileBase64, não os dois"), nil, nil
		}
		var data []byte
		switch {
		case in.FileURL != "":
			d, errMsg := s.fetchFile(ctx, in.FileURL, maxModel3DUploadBytes)
			if errMsg != "" {
				return errResult(errMsg), nil, nil
			}
			data = d
		case in.FileBase64 != "":
			b64 := strings.TrimSpace(in.FileBase64)
			if i := strings.Index(b64, ";base64,"); i >= 0 { // data URI
				b64 = b64[i+len(";base64,"):]
			}
			d, err := base64.StdEncoding.DecodeString(b64)
			if err != nil {
				return errResult("fileBase64 inválido: " + err.Error()), nil, nil
			}
			data = d
		default:
			return errResult("informe fileUrl (preferido) ou fileBase64"), nil, nil
		}
		if len(data) == 0 {
			return errResult("arquivo vazio"), nil, nil
		}
		if len(data) > maxModel3DUploadBytes {
			return errResult("arquivo grande demais (máx 50MB)"), nil, nil
		}

		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		fw, err := mw.CreateFormFile("file", in.Filename)
		if err != nil {
			return errResult("falha ao montar o upload: " + err.Error()), nil, nil
		}
		if _, err := fw.Write(data); err != nil {
			return errResult("falha ao montar o upload: " + err.Error()), nil, nil
		}
		if in.Folder != "" {
			if err := mw.WriteField("folder", in.Folder); err != nil {
				return errResult("falha ao montar o upload: " + err.Error()), nil, nil
			}
		}
		mw.Close()

		ctx, cancel := context.WithTimeout(ctx, model3DUploadTimeout)
		defer cancel()
		u := authAdmin + "/models3d"
		status, raw, err := s.client.doRaw(ctx, "POST", u, authorization(req.Extra), mw.FormDataContentType(), &buf)
		return resultFrom("POST", u, status, raw, err)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "model3d_update",
		Description: "Atualiza nome, pasta e/ou fixação de um modelo 3D (PATCH /auth/admin/models3d/{id}). Só os campos enviados mudam.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in model3DUpdateInput) (*mcp.CallToolResult, any, error) {
		body := map[string]any{}
		if in.Filename != nil {
			body["filename"] = *in.Filename
		}
		if in.Folder != nil {
			body["folder"] = *in.Folder
		}
		if in.Pinned != nil {
			body["pinned"] = *in.Pinned
		}
		if len(body) == 0 {
			return errResult("nenhum campo para atualizar (filename, folder, pinned)"), nil, nil
		}
		return s.proxy(ctx, req, "PATCH", authAdmin+"/models3d/"+url.PathEscape(in.ID), body)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name: "model3d_delete",
		Description: "Remove um modelo 3D da biblioteca, apagando também o arquivo (e miniatura, se houver) no R2 " +
			"(DELETE /auth/admin/models3d/{id}). Ação irreversível.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in model3DIDInput) (*mcp.CallToolResult, any, error) {
		return s.proxy(ctx, req, "DELETE", authAdmin+"/models3d/"+url.PathEscape(in.ID), nil)
	})
}

// setIf grava k=v em qs só quando v não é vazio — evita poluir a query string
// com filtros não usados.
func setIf(qs url.Values, k, v string) {
	if v != "" {
		qs.Set(k, v)
	}
}

// withQuery anexa qs a base só se houver algum parâmetro.
func withQuery(base string, qs url.Values) string {
	if len(qs) == 0 {
		return base
	}
	return base + "?" + qs.Encode()
}

// fetchFile baixa um arquivo da URL do usuário (mesmo cliente anti-SSRF de
// fetchImage, só que com teto parametrizável — model3d permite até 50MB,
// bem acima do teto de 5MB fixo em fetchImage, por isso não a reaproveita).
func (s *Server) fetchFile(ctx context.Context, rawURL string, maxBytes int) ([]byte, string) {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, "fileUrl inválida: use uma URL http(s) completa"
	}
	httpReq, err := http.NewRequestWithContext(ctx, "GET", rawURL, nil)
	if err != nil {
		return nil, "fileUrl inválida: " + err.Error()
	}
	resp, err := s.fetch.Do(httpReq)
	if err != nil {
		return nil, "não consegui baixar o arquivo: " + err.Error()
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Sprintf("não consegui baixar o arquivo: HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, int64(maxBytes)+1))
	if err != nil {
		return nil, "falha lendo o arquivo: " + err.Error()
	}
	if len(data) > maxBytes {
		return nil, fmt.Sprintf("arquivo grande demais (máx %dMB)", maxBytes>>20)
	}
	return data, ""
}
