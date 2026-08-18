package main

import (
	"context"
	"net/url"
	"strconv"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Tools de conteúdo da API central: blog (posts, categorias, analytics,
// heatmap), vitrine de links (Linktree institucional), calendário editorial
// (social) e automação de comentário do Instagram.
//
// Uso interno (só nós usamos este MCP): get+list do mesmo recurso e os CRUDs
// de baixo risco (blog_category, link, link_settings, social_post_note,
// social_post_platform, instagram_link — nada que quebre produção sozinho ou
// seja caro de refazer) ficam consolidados numa tool com parâmetro action.
// blog_post e social_post continuam com create/update/delete separados: são o
// conteúdo em si (não metadado de organização), e blog_post_delete exige sudo
// na API por ser mais sensível.

// ── Blog — posts e categorias ───────────────────────────────────────────────

type blogPostsListInput struct {
	Page     int    `json:"page,omitempty" jsonschema:"página (padrão 1)"`
	PageSize int    `json:"pageSize,omitempty" jsonschema:"itens por página (padrão 12, máximo 50)"`
	Category string `json:"category,omitempty" jsonschema:"filtra pelo slug da categoria"`
	Query    string `json:"query,omitempty" jsonschema:"busca em título e resumo"`
	Status   string `json:"status,omitempty" jsonschema:"filtra por status: draft ou published; vazio traz os dois"`
}

type blogPostIDInput struct {
	ID string `json:"id" jsonschema:"id (uuid) do post"`
}

// blogPostGetInput: com id detalha o post (filtros abaixo são ignorados); sem
// id lista, e aí os filtros de blogPostsListInput valem.
type blogPostGetInput struct {
	ID string `json:"id,omitempty" jsonschema:"id (uuid) do post; se informado, os demais campos (filtros de lista) são ignorados"`
	blogPostsListInput
}

type blogPostInput struct {
	Slug          string  `json:"slug" jsonschema:"slug único em minúsculas, números e hífens (ex: como-aprender-python)"`
	Title         string  `json:"title" jsonschema:"título do post"`
	Excerpt       string  `json:"excerpt,omitempty" jsonschema:"resumo curto exibido na listagem"`
	ContentHTML   string  `json:"contentHtml,omitempty" jsonschema:"corpo do post em HTML; a API sanitiza antes de salvar"`
	CoverImageURL *string `json:"coverImageUrl,omitempty" jsonschema:"URL da imagem de capa"`
	CategoryID    string  `json:"categoryId" jsonschema:"id (uuid) da categoria"`
	Status        string  `json:"status,omitempty" jsonschema:"draft ou published; vazio vira draft"`
}

type blogPostUpdateInput struct {
	ID string `json:"id" jsonschema:"id (uuid) do post"`
	blogPostInput
}

// blogCategoryActionInput consolida list/create/update/delete — categoria é
// só slug+nome, sem dado sensível; a própria API já recusa delete de
// categoria em uso e exige admin+sudo nela.
type blogCategoryActionInput struct {
	Action string  `json:"action" jsonschema:"list, create, update ou delete"`
	ID     *string `json:"id,omitempty" jsonschema:"id (uuid) da categoria — obrigatório em update/delete"`
	Slug   *string `json:"slug,omitempty" jsonschema:"slug único em minúsculas, números e hífens — obrigatório em create/update"`
	Name   *string `json:"name,omitempty" jsonschema:"nome de exibição da categoria — obrigatório em create/update"`
}

// ── Blog — métricas e heatmap ───────────────────────────────────────────────

// blogMetricsFilterInput é comum a todos os endpoints de /blog/metrics/*
// (exceto heatmap, que exige postSlug+viewport — ver blogHeatmapInput).
// from/to são obrigatórios (YYYY-MM-DD); "to" é inclusivo na API (ela soma
// 24h internamente), e o intervalo máximo aceito é 366 dias.
type blogMetricsFilterInput struct {
	From      string `json:"from" jsonschema:"data inicial, formato YYYY-MM-DD"`
	To        string `json:"to" jsonschema:"data final (inclusiva), formato YYYY-MM-DD; intervalo máximo de 366 dias"`
	PostSlug  string `json:"postSlug,omitempty" jsonschema:"filtra por um post específico"`
	Referrer  string `json:"referrer,omitempty" jsonschema:"filtra por referrer (drill-down do painel)"`
	UTMSource string `json:"utmSource,omitempty" jsonschema:"filtra por utm_source (drill-down do painel)"`
	Device    string `json:"device,omitempty" jsonschema:"filtra por tipo de dispositivo (drill-down do painel)"`
	Country   string `json:"country,omitempty" jsonschema:"filtra por país, código CF-IPCountry (drill-down do painel)"`
}

func (f blogMetricsFilterInput) values() url.Values {
	v := url.Values{}
	v.Set("from", f.From)
	v.Set("to", f.To)
	if f.PostSlug != "" {
		v.Set("postSlug", f.PostSlug)
	}
	if f.Referrer != "" {
		v.Set("referrer", f.Referrer)
	}
	if f.UTMSource != "" {
		v.Set("utmSource", f.UTMSource)
	}
	if f.Device != "" {
		v.Set("device", f.Device)
	}
	if f.Country != "" {
		v.Set("country", f.Country)
	}
	return v
}

// blogHeatmapInput: postSlug e viewport são obrigatórios aqui (diferente dos
// outros endpoints de métrica) — heatmap de clique/scroll só faz sentido pra
// um post e um bucket de viewport por vez.
type blogHeatmapInput struct {
	From      string `json:"from" jsonschema:"data inicial, formato YYYY-MM-DD"`
	To        string `json:"to" jsonschema:"data final (inclusiva), formato YYYY-MM-DD; intervalo máximo de 366 dias"`
	PostSlug  string `json:"postSlug" jsonschema:"slug do post (obrigatório)"`
	Viewport  string `json:"viewport" jsonschema:"mobile ou desktop (obrigatório)"`
	Referrer  string `json:"referrer,omitempty" jsonschema:"filtra por referrer (drill-down do painel)"`
	UTMSource string `json:"utmSource,omitempty" jsonschema:"filtra por utm_source (drill-down do painel)"`
	Device    string `json:"device,omitempty" jsonschema:"filtra por tipo de dispositivo (drill-down do painel)"`
	Country   string `json:"country,omitempty" jsonschema:"filtra por país, código CF-IPCountry (drill-down do painel)"`
}

func (f blogHeatmapInput) values() url.Values {
	v := url.Values{}
	v.Set("from", f.From)
	v.Set("to", f.To)
	v.Set("postSlug", f.PostSlug)
	v.Set("viewport", f.Viewport)
	if f.Referrer != "" {
		v.Set("referrer", f.Referrer)
	}
	if f.UTMSource != "" {
		v.Set("utmSource", f.UTMSource)
	}
	if f.Device != "" {
		v.Set("device", f.Device)
	}
	if f.Country != "" {
		v.Set("country", f.Country)
	}
	return v
}

type blogHeatmapClicksInput struct {
	blogHeatmapInput
	Cols int `json:"cols,omitempty" jsonschema:"colunas da grade (default calculado pelo dashboard a partir do tamanho do post)"`
	Rows int `json:"rows,omitempty" jsonschema:"linhas da grade (default calculado pelo dashboard a partir do tamanho do post)"`
}

// ── Vitrine de links (Linktree institucional) ───────────────────────────────

// linkActionInput consolida list/create/update/delete — card do Linktree
// institucional, sem dado sensível, errar a ação custa só recriar o card.
type linkActionInput struct {
	Action      string  `json:"action" jsonschema:"list, create, update ou delete"`
	ID          *string `json:"id,omitempty" jsonschema:"id (uuid) do card — obrigatório em update/delete"`
	Title       *string `json:"title,omitempty" jsonschema:"título do card — obrigatório em create/update"`
	Description *string `json:"description,omitempty" jsonschema:"descrição curta do card"`
	ImageURL    *string `json:"imageUrl,omitempty" jsonschema:"URL da imagem do card"`
	URL         *string `json:"url,omitempty" jsonschema:"URL de destino (http:// ou https://) — obrigatório em create/update"`
	Status      *string `json:"status,omitempty" jsonschema:"active ou inactive — obrigatório em create/update"`
	Ordem       *int    `json:"ordem,omitempty" jsonschema:"posição de exibição (menor aparece primeiro) — obrigatório em create/update"`
}

// linkSettingsInput: sem backgroundImageUrl lê as configurações; com o campo
// (mesmo string vazia, que remove o fundo) atualiza.
type linkSettingsInput struct {
	BackgroundImageURL *string `json:"backgroundImageUrl,omitempty" jsonschema:"presente = atualiza (vazio remove o fundo); ausente = só consulta a config atual"`
}

// ── Calendário editorial (social) ───────────────────────────────────────────

type socialPostIDInput struct {
	ID string `json:"id" jsonschema:"id (uuid) do post"`
}

type socialPostGetInput struct {
	ID string `json:"id,omitempty" jsonschema:"id (uuid) do post; omitido lista todos os posts do calendário editorial"`
}

// socialPostInput reflete o corpo completo exigido tanto por POST quanto por
// PUT (não é atualização parcial — a API valida os campos obrigatórios nos
// dois casos, ver handleCreateSocialPost/handleUpdateSocialPost). Vários
// campos de enum aceitam "" como valor válido (ex: programa, funilEtapa,
// receita) — ver validSocial* em apps/api-go/social.go.
type socialPostInput struct {
	Title        string  `json:"title" jsonschema:"título do post"`
	Caption      string  `json:"caption,omitempty" jsonschema:"legenda/copy do post"`
	Platform     string  `json:"platform" jsonschema:"facebook, instagram, tiktok, youtube, threads, google_meu_negocio, blog, twitter_x ou linkedin"`
	Pilar        string  `json:"pilar" jsonschema:"educacional, institucional, captacao, prova_social, bastidores ou tech_mundo_real"`
	Status       string  `json:"status" jsonschema:"ideia, planejado, em_producao, revisao, aprovado, agendado, publicado ou arquivado"`
	ScheduledAt  *string `json:"scheduledAt,omitempty" jsonschema:"data/hora agendada, formato RFC3339 (ex: 2026-08-20T15:00:00Z)"`
	MediaURL     string  `json:"mediaUrl,omitempty" jsonschema:"URL da mídia (imagem/vídeo) final"`
	ReferenceURL string  `json:"referenceUrl,omitempty" jsonschema:"URL de referência/inspiração"`

	Formato            string         `json:"formato" jsonschema:"estatico, carrossel, reel, story, video_longo, short, thumbnail ou card_link"`
	Objetivo           string         `json:"objetivo" jsonschema:"alcance, engajamento, conversao ou autoridade"`
	Programa           string         `json:"programa,omitempty" jsonschema:"create, jr, camps, academies ou vazio (sem programa específico)"`
	Receita            string         `json:"receita,omitempty" jsonschema:"receita criativa usada (ex: hero_numero, versus, checklist); vazio permitido"`
	PlataformasDestino []string       `json:"plataformasDestino,omitempty" jsonschema:"lista de plataformas-destino, mesmos valores de platform"`
	CopyArte           []any          `json:"copyArte,omitempty" jsonschema:"variações de copy/arte (lista de objetos livres definidos pelo dashboard); vazio vira []"`
	Hashtags           []string       `json:"hashtags,omitempty"`
	ConceitoVisual     string         `json:"conceitoVisual,omitempty" jsonschema:"descrição do conceito visual da peça"`
	Paleta             map[string]any `json:"paleta,omitempty" jsonschema:"paleta de cores (objeto livre definido pelo dashboard); vazio vira {}"`
	PromptIA           string         `json:"promptIa,omitempty" jsonschema:"prompt usado para gerar a peça via IA, se houver"`
	Specs              map[string]any `json:"specs,omitempty" jsonschema:"especificações técnicas (dimensões, formato de arquivo etc.); vazio vira {}"`
	MasterURL          string         `json:"masterUrl,omitempty" jsonschema:"URL do arquivo master/editável"`
	Mandatorios        string         `json:"mandatorios,omitempty" jsonschema:"itens obrigatórios da peça (logo, disclaimer etc.)"`
	ResponsavelID      *int64         `json:"responsavelId,omitempty" jsonschema:"id do usuário responsável pela peça"`
	FunilEtapa         string         `json:"funilEtapa,omitempty" jsonschema:"topo, meio, fundo ou vazio (sem função de funil definida)"`
}

type socialPostUpdateInput struct {
	ID string `json:"id" jsonschema:"id (uuid) do post"`
	socialPostInput
}

type socialPostStatusInput struct {
	ID     string `json:"id" jsonschema:"id (uuid) do post"`
	Status string `json:"status" jsonschema:"ideia, planejado, em_producao, revisao, aprovado, agendado, publicado ou arquivado"`
}

// socialPostNoteActionInput consolida list/add — notas não têm update/delete
// na API.
type socialPostNoteActionInput struct {
	Action  string `json:"action" jsonschema:"list ou add"`
	ID      string `json:"id" jsonschema:"id (uuid) do post"`
	Content string `json:"content,omitempty" jsonschema:"texto da nota — obrigatório em add"`
}

// socialPostPlatformActionInput consolida confirm/unconfirm.
type socialPostPlatformActionInput struct {
	Action   string `json:"action" jsonschema:"confirm ou unconfirm"`
	ID       string `json:"id" jsonschema:"id (uuid) do post"`
	Platform string `json:"platform" jsonschema:"facebook, instagram, tiktok, youtube, threads, google_meu_negocio, blog, twitter_x ou linkedin"`
}

// ── Instagram — mídia e mapeamento de comentário ────────────────────────────

// instagramLinkActionInput consolida list/upsert/delete do mapeamento
// publicação→link — tabela pequena, gerida manualmente, sem dado sensível.
type instagramLinkActionInput struct {
	Action  string  `json:"action" jsonschema:"list, upsert ou delete"`
	MediaID *string `json:"mediaId,omitempty" jsonschema:"id da publicação no Instagram — obrigatório em upsert/delete"`
	URL     *string `json:"url,omitempty" jsonschema:"URL enviada por DM a quem comentar (http:// ou https://) — obrigatório em upsert"`
	Note    *string `json:"note,omitempty" jsonschema:"anotação interna sobre o mapeamento"`
	Keyword *string `json:"keyword,omitempty" jsonschema:"se definida, só responde comentários que contenham essa palavra (case-insensitive); vazio/ausente responde qualquer comentário"`
}

func (s *Server) addContentTools(srv *mcp.Server) {
	base := s.cfg.AuthBaseURL

	// ── Blog: posts ────────────────────────────────────────────────────────

	mcp.AddTool(srv, &mcp.Tool{
		Name: "blog_post_get",
		Description: "Sem id: lista posts do blog, todos os status (admin) (GET /blog/posts) — filtre por página, categoria, busca e/ou status. " +
			"Com id: detalha um post específico, qualquer status (GET /blog/posts/{id}).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in blogPostGetInput) (*mcp.CallToolResult, any, error) {
		if in.ID != "" {
			return s.proxy(ctx, req, "GET", base+"/blog/posts/"+url.PathEscape(in.ID), nil)
		}
		v := url.Values{}
		if in.Page > 0 {
			v.Set("page", strconv.Itoa(in.Page))
		}
		if in.PageSize > 0 {
			v.Set("pageSize", strconv.Itoa(in.PageSize))
		}
		if in.Category != "" {
			v.Set("category", in.Category)
		}
		if in.Query != "" {
			v.Set("q", in.Query)
		}
		if in.Status != "" {
			v.Set("status", in.Status)
		}
		u := base + "/blog/posts"
		if len(v) > 0 {
			u += "?" + v.Encode()
		}
		return s.proxy(ctx, req, "GET", u, nil)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "blog_post_create",
		Description: "Cria um post do blog (POST /blog/posts). status vazio vira draft; published define publishedAt automaticamente.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in blogPostInput) (*mcp.CallToolResult, any, error) {
		return s.proxy(ctx, req, "POST", base+"/blog/posts", in)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "blog_post_update",
		Description: "Substitui um post do blog (PATCH /blog/posts/{id}). Apesar do verbo, a API exige o corpo completo (title/slug/categoryId obrigatórios) — não é atualização parcial.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in blogPostUpdateInput) (*mcp.CallToolResult, any, error) {
		return s.proxy(ctx, req, "PATCH", base+"/blog/posts/"+url.PathEscape(in.ID), in.blogPostInput)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "blog_post_delete",
		Description: "Apaga um post do blog (DELETE /blog/posts/{id}). A API exige admin + sudo — sem sudo recente ela devolve SUDO_REQUIRED.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in blogPostIDInput) (*mcp.CallToolResult, any, error) {
		return s.proxy(ctx, req, "DELETE", base+"/blog/posts/"+url.PathEscape(in.ID), nil)
	})

	// ── Blog: categorias ───────────────────────────────────────────────────

	mcp.AddTool(srv, &mcp.Tool{
		Name: "blog_category",
		Description: "Gerencia categorias do blog: action=list (GET /blog/categories), create (POST), update (PATCH, " +
			"corpo completo) ou delete (DELETE — admin+sudo na API, recusa categoria ainda em uso por posts).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in blogCategoryActionInput) (*mcp.CallToolResult, any, error) {
		switch in.Action {
		case "list":
			return s.proxy(ctx, req, "GET", base+"/blog/categories", nil)
		case "create":
			if in.Slug == nil || *in.Slug == "" || in.Name == nil || *in.Name == "" {
				return errResult("slug e name são obrigatórios para create"), nil, nil
			}
			return s.proxy(ctx, req, "POST", base+"/blog/categories", map[string]any{"slug": *in.Slug, "name": *in.Name})
		case "update":
			if in.ID == nil || *in.ID == "" {
				return errResult("id é obrigatório para update"), nil, nil
			}
			if in.Slug == nil || *in.Slug == "" || in.Name == nil || *in.Name == "" {
				return errResult("slug e name são obrigatórios para update"), nil, nil
			}
			return s.proxy(ctx, req, "PATCH", base+"/blog/categories/"+url.PathEscape(*in.ID), map[string]any{"slug": *in.Slug, "name": *in.Name})
		case "delete":
			if in.ID == nil || *in.ID == "" {
				return errResult("id é obrigatório para delete"), nil, nil
			}
			return s.proxy(ctx, req, "DELETE", base+"/blog/categories/"+url.PathEscape(*in.ID), nil)
		default:
			return errResult("action deve ser list, create, update ou delete"), nil, nil
		}
	})

	// ── Blog: heatmap ──────────────────────────────────────────────────────

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "blog_heatmap_clicks",
		Description: "Grade de cliques do heatmap de um post (GET /blog/metrics/heatmap/clicks). postSlug e viewport são obrigatórios.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in blogHeatmapClicksInput) (*mcp.CallToolResult, any, error) {
		v := in.values()
		if in.Cols > 0 {
			v.Set("cols", strconv.Itoa(in.Cols))
		}
		if in.Rows > 0 {
			v.Set("rows", strconv.Itoa(in.Rows))
		}
		return s.proxy(ctx, req, "GET", base+"/blog/metrics/heatmap/clicks?"+v.Encode(), nil)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "blog_heatmap_scroll",
		Description: "Funil de profundidade de scroll de um post (GET /blog/metrics/heatmap/scroll). postSlug e viewport são obrigatórios.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in blogHeatmapInput) (*mcp.CallToolResult, any, error) {
		return s.proxy(ctx, req, "GET", base+"/blog/metrics/heatmap/scroll?"+in.values().Encode(), nil)
	})

	// ── Blog: métricas ─────────────────────────────────────────────────────

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "blog_metrics_overview",
		Description: "Visão geral de métricas do blog no período (GET /blog/metrics/overview). from/to obrigatórios (YYYY-MM-DD).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in blogMetricsFilterInput) (*mcp.CallToolResult, any, error) {
		return s.proxy(ctx, req, "GET", base+"/blog/metrics/overview?"+in.values().Encode(), nil)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "blog_metrics_timeseries",
		Description: "Série temporal de métricas do blog no período (GET /blog/metrics/timeseries). from/to obrigatórios (YYYY-MM-DD).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in blogMetricsFilterInput) (*mcp.CallToolResult, any, error) {
		return s.proxy(ctx, req, "GET", base+"/blog/metrics/timeseries?"+in.values().Encode(), nil)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "blog_metrics_top_posts",
		Description: "Ranking dos posts mais acessados no período (GET /blog/metrics/top-posts). from/to obrigatórios (YYYY-MM-DD).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in blogMetricsFilterInput) (*mcp.CallToolResult, any, error) {
		return s.proxy(ctx, req, "GET", base+"/blog/metrics/top-posts?"+in.values().Encode(), nil)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "blog_metrics_referrers",
		Description: "Ranking de referrers no período (GET /blog/metrics/referrers). from/to obrigatórios (YYYY-MM-DD).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in blogMetricsFilterInput) (*mcp.CallToolResult, any, error) {
		return s.proxy(ctx, req, "GET", base+"/blog/metrics/referrers?"+in.values().Encode(), nil)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "blog_metrics_utm_source",
		Description: "Ranking de utm_source no período (GET /blog/metrics/utm-source). from/to obrigatórios (YYYY-MM-DD).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in blogMetricsFilterInput) (*mcp.CallToolResult, any, error) {
		return s.proxy(ctx, req, "GET", base+"/blog/metrics/utm-source?"+in.values().Encode(), nil)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "blog_metrics_devices",
		Description: "Distribuição por tipo de dispositivo no período (GET /blog/metrics/devices). from/to obrigatórios (YYYY-MM-DD).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in blogMetricsFilterInput) (*mcp.CallToolResult, any, error) {
		return s.proxy(ctx, req, "GET", base+"/blog/metrics/devices?"+in.values().Encode(), nil)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "blog_metrics_countries",
		Description: "Distribuição por país no período (GET /blog/metrics/countries). from/to obrigatórios (YYYY-MM-DD).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in blogMetricsFilterInput) (*mcp.CallToolResult, any, error) {
		return s.proxy(ctx, req, "GET", base+"/blog/metrics/countries?"+in.values().Encode(), nil)
	})

	// ── Vitrine de links (Linktree institucional) ─────────────────────────

	mcp.AddTool(srv, &mcp.Tool{
		Name: "link",
		Description: "Gerencia cards da vitrine de links: action=list (GET /links, inclusive inativos), create (POST), " +
			"update (PUT /links/{id}, corpo completo) ou delete (DELETE /links/{id}).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in linkActionInput) (*mcp.CallToolResult, any, error) {
		switch in.Action {
		case "list":
			return s.proxy(ctx, req, "GET", base+"/links", nil)
		case "create":
			if in.Title == nil || *in.Title == "" || in.URL == nil || *in.URL == "" || in.Status == nil || in.Ordem == nil {
				return errResult("title, url, status e ordem são obrigatórios para create"), nil, nil
			}
			body := map[string]any{"title": *in.Title, "url": *in.URL, "status": *in.Status, "ordem": *in.Ordem}
			if in.Description != nil {
				body["description"] = *in.Description
			}
			if in.ImageURL != nil {
				body["imageUrl"] = *in.ImageURL
			}
			return s.proxy(ctx, req, "POST", base+"/links", body)
		case "update":
			if in.ID == nil || *in.ID == "" {
				return errResult("id é obrigatório para update"), nil, nil
			}
			if in.Title == nil || *in.Title == "" || in.URL == nil || *in.URL == "" || in.Status == nil || in.Ordem == nil {
				return errResult("title, url, status e ordem são obrigatórios para update"), nil, nil
			}
			body := map[string]any{"title": *in.Title, "url": *in.URL, "status": *in.Status, "ordem": *in.Ordem}
			if in.Description != nil {
				body["description"] = *in.Description
			}
			if in.ImageURL != nil {
				body["imageUrl"] = *in.ImageURL
			}
			return s.proxy(ctx, req, "PUT", base+"/links/"+url.PathEscape(*in.ID), body)
		case "delete":
			if in.ID == nil || *in.ID == "" {
				return errResult("id é obrigatório para delete"), nil, nil
			}
			return s.proxy(ctx, req, "DELETE", base+"/links/"+url.PathEscape(*in.ID), nil)
		default:
			return errResult("action deve ser list, create, update ou delete"), nil, nil
		}
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name: "link_settings",
		Description: "Sem backgroundImageUrl: mostra as configurações da página da vitrine de links (GET /links/settings). " +
			"Com backgroundImageUrl (mesmo vazio): atualiza (PUT) — vazio remove o fundo.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in linkSettingsInput) (*mcp.CallToolResult, any, error) {
		if in.BackgroundImageURL == nil {
			return s.proxy(ctx, req, "GET", base+"/links/settings", nil)
		}
		return s.proxy(ctx, req, "PUT", base+"/links/settings", in)
	})

	// ── Calendário editorial (social) ──────────────────────────────────────

	mcp.AddTool(srv, &mcp.Tool{
		Name: "social_post_get",
		Description: "Sem id: lista todos os posts do calendário editorial, sem filtros (GET /social/posts). " +
			"Com id: detalha um post específico, incluindo publishConfirmations (GET /social/posts/{id}).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in socialPostGetInput) (*mcp.CallToolResult, any, error) {
		if in.ID == "" {
			return s.proxy(ctx, req, "GET", base+"/social/posts", nil)
		}
		return s.proxy(ctx, req, "GET", base+"/social/posts/"+url.PathEscape(in.ID), nil)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "social_post_create",
		Description: "Cria um post no calendário editorial (POST /social/posts).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in socialPostInput) (*mcp.CallToolResult, any, error) {
		return s.proxy(ctx, req, "POST", base+"/social/posts", in)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "social_post_update",
		Description: "Substitui um post do calendário editorial (PUT /social/posts/{id}). Corpo completo, mesmos campos de social_post_create.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in socialPostUpdateInput) (*mcp.CallToolResult, any, error) {
		return s.proxy(ctx, req, "PUT", base+"/social/posts/"+url.PathEscape(in.ID), in.socialPostInput)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "social_post_delete",
		Description: "Apaga um post do calendário editorial (DELETE /social/posts/{id}).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in socialPostIDInput) (*mcp.CallToolResult, any, error) {
		return s.proxy(ctx, req, "DELETE", base+"/social/posts/"+url.PathEscape(in.ID), nil)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "social_post_status_update",
		Description: "Move um post do calendário editorial para outro status (PATCH /social/posts/{id}/status). status=revisao dispara email de alerta para aprovação, se configurado.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in socialPostStatusInput) (*mcp.CallToolResult, any, error) {
		if in.Status == "" {
			return errResult("informe status"), nil, nil
		}
		return s.proxy(ctx, req, "PATCH", base+"/social/posts/"+url.PathEscape(in.ID)+"/status", map[string]any{"status": in.Status})
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "social_post_note",
		Description: "Gerencia notas internas de um post do calendário editorial: action=list (GET .../notes) ou add (POST, content).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in socialPostNoteActionInput) (*mcp.CallToolResult, any, error) {
		notesURL := base + "/social/posts/" + url.PathEscape(in.ID) + "/notes"
		switch in.Action {
		case "list":
			return s.proxy(ctx, req, "GET", notesURL, nil)
		case "add":
			if in.Content == "" {
				return errResult("content é obrigatório para add"), nil, nil
			}
			return s.proxy(ctx, req, "POST", notesURL, map[string]any{"content": in.Content})
		default:
			return errResult("action deve ser list ou add"), nil, nil
		}
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "social_post_history",
		Description: "Histórico de mudanças de status de um post do calendário editorial, mais recente primeiro (GET /social/posts/{id}/history).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in socialPostIDInput) (*mcp.CallToolResult, any, error) {
		return s.proxy(ctx, req, "GET", base+"/social/posts/"+url.PathEscape(in.ID)+"/history", nil)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name: "social_post_platform",
		Description: "Confirma ou desfaz a confirmação de entrega de uma plataforma num post do calendário editorial: " +
			"action=confirm (POST) ou unconfirm (DELETE) em /social/posts/{id}/publish-confirmations/{platform}. " +
			"Mudar o status para publicado exige confirmação de todas as plataformas em plataformasDestino.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in socialPostPlatformActionInput) (*mcp.CallToolResult, any, error) {
		u := base + "/social/posts/" + url.PathEscape(in.ID) + "/publish-confirmations/" + url.PathEscape(in.Platform)
		switch in.Action {
		case "confirm":
			return s.proxy(ctx, req, "POST", u, nil)
		case "unconfirm":
			return s.proxy(ctx, req, "DELETE", u, nil)
		default:
			return errResult("action deve ser confirm ou unconfirm"), nil, nil
		}
	})

	// ── Instagram — mídia e mapeamento de comentário ───────────────────────

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "instagram_media_list",
		Description: "Lista publicações recentes da conta do Instagram conectada, para escolher o media_id a mapear (GET /instagram/media). Indisponível se a automação do Instagram não estiver configurada.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, any, error) {
		return s.proxy(ctx, req, "GET", base+"/instagram/media", nil)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name: "instagram_link",
		Description: "Gerencia o mapeamento publicação→link do Instagram (resposta automática de comentário): action=list (GET /instagram/links), " +
			"upsert (PUT /instagram/links/{mediaId}, idempotente — url obrigatória) ou delete (DELETE).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in instagramLinkActionInput) (*mcp.CallToolResult, any, error) {
		switch in.Action {
		case "list":
			return s.proxy(ctx, req, "GET", base+"/instagram/links", nil)
		case "upsert":
			if in.MediaID == nil || *in.MediaID == "" {
				return errResult("mediaId é obrigatório para upsert"), nil, nil
			}
			if in.URL == nil || *in.URL == "" {
				return errResult("url é obrigatória para upsert"), nil, nil
			}
			body := map[string]any{"url": *in.URL}
			if in.Note != nil {
				body["note"] = *in.Note
			}
			if in.Keyword != nil {
				body["keyword"] = *in.Keyword
			}
			return s.proxy(ctx, req, "PUT", base+"/instagram/links/"+url.PathEscape(*in.MediaID), body)
		case "delete":
			if in.MediaID == nil || *in.MediaID == "" {
				return errResult("mediaId é obrigatório para delete"), nil, nil
			}
			return s.proxy(ctx, req, "DELETE", base+"/instagram/links/"+url.PathEscape(*in.MediaID), nil)
		default:
			return errResult("action deve ser list, upsert ou delete"), nil, nil
		}
	})
}
