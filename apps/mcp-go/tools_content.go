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

type blogCategoryIDInput struct {
	ID string `json:"id" jsonschema:"id (uuid) da categoria"`
}

type blogCategoryInput struct {
	Slug string `json:"slug" jsonschema:"slug único em minúsculas, números e hífens"`
	Name string `json:"name" jsonschema:"nome de exibição da categoria"`
}

type blogCategoryUpdateInput struct {
	ID string `json:"id" jsonschema:"id (uuid) da categoria"`
	blogCategoryInput
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

type linkIDInput struct {
	ID string `json:"id" jsonschema:"id (uuid) do card"`
}

type linkItemInput struct {
	Title       string  `json:"title" jsonschema:"título do card"`
	Description string  `json:"description,omitempty" jsonschema:"descrição curta do card"`
	ImageURL    *string `json:"imageUrl,omitempty" jsonschema:"URL da imagem do card"`
	URL         string  `json:"url" jsonschema:"URL de destino (http:// ou https://)"`
	Status      string  `json:"status" jsonschema:"active ou inactive"`
	Ordem       int     `json:"ordem" jsonschema:"posição de exibição (menor aparece primeiro)"`
}

type linkUpdateInput struct {
	ID string `json:"id" jsonschema:"id (uuid) do card"`
	linkItemInput
}

type linkSettingsInput struct {
	BackgroundImageURL *string `json:"backgroundImageUrl,omitempty" jsonschema:"URL da imagem de fundo da página; omitido/vazio remove o fundo"`
}

// ── Calendário editorial (social) ───────────────────────────────────────────

type socialPostIDInput struct {
	ID string `json:"id" jsonschema:"id (uuid) do post"`
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

type socialPostNoteAddInput struct {
	ID      string `json:"id" jsonschema:"id (uuid) do post"`
	Content string `json:"content" jsonschema:"texto da nota"`
}

type socialPostPlatformConfirmInput struct {
	ID       string `json:"id" jsonschema:"id (uuid) do post"`
	Platform string `json:"platform" jsonschema:"facebook, instagram, tiktok, youtube, threads, google_meu_negocio, blog, twitter_x ou linkedin"`
}

// ── Instagram — mídia e mapeamento de comentário ────────────────────────────

type instagramMediaIDInput struct {
	MediaID string `json:"mediaId" jsonschema:"id da publicação no Instagram"`
}

type instagramLinkUpsertInput struct {
	MediaID string `json:"mediaId" jsonschema:"id da publicação no Instagram"`
	URL     string `json:"url" jsonschema:"URL enviada por DM a quem comentar (http:// ou https://)"`
	Note    string `json:"note,omitempty" jsonschema:"anotação interna sobre o mapeamento"`
	Keyword string `json:"keyword,omitempty" jsonschema:"se definida, só responde comentários que contenham essa palavra (case-insensitive); vazio responde qualquer comentário"`
}

func (s *Server) addContentTools(srv *mcp.Server) {
	base := s.cfg.AuthBaseURL

	// ── Blog: posts ────────────────────────────────────────────────────────

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "blog_posts_list",
		Description: "Lista posts do blog, todos os status (admin) (GET /blog/posts). Filtre por página, categoria, busca e/ou status.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in blogPostsListInput) (*mcp.CallToolResult, any, error) {
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
		Name:        "blog_post_get",
		Description: "Detalha um post do blog pelo id, qualquer status (GET /blog/posts/{id}).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in blogPostIDInput) (*mcp.CallToolResult, any, error) {
		return s.proxy(ctx, req, "GET", base+"/blog/posts/"+url.PathEscape(in.ID), nil)
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
		Name:        "blog_categories_list",
		Description: "Lista categorias do blog com id, para editar/apagar (GET /blog/categories).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, any, error) {
		return s.proxy(ctx, req, "GET", base+"/blog/categories", nil)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "blog_category_create",
		Description: "Cria uma categoria do blog (POST /blog/categories).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in blogCategoryInput) (*mcp.CallToolResult, any, error) {
		return s.proxy(ctx, req, "POST", base+"/blog/categories", in)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "blog_category_update",
		Description: "Substitui uma categoria do blog (PATCH /blog/categories/{id}). Corpo completo — slug e nome são obrigatórios.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in blogCategoryUpdateInput) (*mcp.CallToolResult, any, error) {
		return s.proxy(ctx, req, "PATCH", base+"/blog/categories/"+url.PathEscape(in.ID), in.blogCategoryInput)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "blog_category_delete",
		Description: "Apaga uma categoria do blog (DELETE /blog/categories/{id}). A API exige admin + sudo e recusa categoria ainda em uso por posts.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in blogCategoryIDInput) (*mcp.CallToolResult, any, error) {
		return s.proxy(ctx, req, "DELETE", base+"/blog/categories/"+url.PathEscape(in.ID), nil)
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
		Name:        "links_list",
		Description: "Lista todos os cards da vitrine de links, inclusive inativos (GET /links).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, any, error) {
		return s.proxy(ctx, req, "GET", base+"/links", nil)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "link_create",
		Description: "Cria um card na vitrine de links (POST /links).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in linkItemInput) (*mcp.CallToolResult, any, error) {
		return s.proxy(ctx, req, "POST", base+"/links", in)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "link_update",
		Description: "Substitui um card da vitrine de links (PUT /links/{id}). Corpo completo — title/url/status/ordem são obrigatórios.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in linkUpdateInput) (*mcp.CallToolResult, any, error) {
		return s.proxy(ctx, req, "PUT", base+"/links/"+url.PathEscape(in.ID), in.linkItemInput)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "link_delete",
		Description: "Apaga um card da vitrine de links (DELETE /links/{id}).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in linkIDInput) (*mcp.CallToolResult, any, error) {
		return s.proxy(ctx, req, "DELETE", base+"/links/"+url.PathEscape(in.ID), nil)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "link_settings_get",
		Description: "Mostra as configurações da página da vitrine de links, hoje só a imagem de fundo (GET /links/settings).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, any, error) {
		return s.proxy(ctx, req, "GET", base+"/links/settings", nil)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "link_settings_update",
		Description: "Atualiza as configurações da página da vitrine de links (PUT /links/settings). Omitir/enviar vazio em backgroundImageUrl remove o fundo.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in linkSettingsInput) (*mcp.CallToolResult, any, error) {
		return s.proxy(ctx, req, "PUT", base+"/links/settings", in)
	})

	// ── Calendário editorial (social) ──────────────────────────────────────

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "social_posts_list",
		Description: "Lista todos os posts do calendário editorial (GET /social/posts). Sem filtros — a API devolve a lista inteira.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, any, error) {
		return s.proxy(ctx, req, "GET", base+"/social/posts", nil)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "social_post_get",
		Description: "Detalha um post do calendário editorial pelo id (GET /social/posts/{id}).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in socialPostIDInput) (*mcp.CallToolResult, any, error) {
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
		Name:        "social_post_notes_list",
		Description: "Lista as notas internas de um post do calendário editorial (GET /social/posts/{id}/notes).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in socialPostIDInput) (*mcp.CallToolResult, any, error) {
		return s.proxy(ctx, req, "GET", base+"/social/posts/"+url.PathEscape(in.ID)+"/notes", nil)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "social_post_note_add",
		Description: "Adiciona uma nota interna a um post do calendário editorial (POST /social/posts/{id}/notes).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in socialPostNoteAddInput) (*mcp.CallToolResult, any, error) {
		if in.Content == "" {
			return errResult("informe content"), nil, nil
		}
		return s.proxy(ctx, req, "POST", base+"/social/posts/"+url.PathEscape(in.ID)+"/notes", map[string]any{"content": in.Content})
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "social_post_history",
		Description: "Histórico de mudanças de status de um post do calendário editorial, mais recente primeiro (GET /social/posts/{id}/history).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in socialPostIDInput) (*mcp.CallToolResult, any, error) {
		return s.proxy(ctx, req, "GET", base+"/social/posts/"+url.PathEscape(in.ID)+"/history", nil)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name: "social_post_platform_confirm",
		Description: "Marca uma plataforma como confirmada (peça entregue) num post do calendário editorial " +
			"(POST /social/posts/{id}/publish-confirmations/{platform}). Mudar o status para publicado exige " +
			"confirmação de todas as plataformas em plataformasDestino.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in socialPostPlatformConfirmInput) (*mcp.CallToolResult, any, error) {
		u := base + "/social/posts/" + url.PathEscape(in.ID) + "/publish-confirmations/" + url.PathEscape(in.Platform)
		return s.proxy(ctx, req, "POST", u, nil)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "social_post_platform_unconfirm",
		Description: "Desfaz a confirmação de uma plataforma num post do calendário editorial (DELETE /social/posts/{id}/publish-confirmations/{platform}).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in socialPostPlatformConfirmInput) (*mcp.CallToolResult, any, error) {
		u := base + "/social/posts/" + url.PathEscape(in.ID) + "/publish-confirmations/" + url.PathEscape(in.Platform)
		return s.proxy(ctx, req, "DELETE", u, nil)
	})

	// ── Instagram — mídia e mapeamento de comentário ───────────────────────

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "instagram_media_list",
		Description: "Lista publicações recentes da conta do Instagram conectada, para escolher o media_id a mapear (GET /instagram/media). Indisponível se a automação do Instagram não estiver configurada.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, any, error) {
		return s.proxy(ctx, req, "GET", base+"/instagram/media", nil)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "instagram_links_list",
		Description: "Lista os mapeamentos publicação→link (resposta automática de comentário) já cadastrados (GET /instagram/links).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, any, error) {
		return s.proxy(ctx, req, "GET", base+"/instagram/links", nil)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "instagram_link_upsert",
		Description: "Cria ou atualiza o mapeamento publicação→link do Instagram (PUT /instagram/links/{mediaId}, idempotente por media_id). keyword vazia responde a qualquer comentário na publicação.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in instagramLinkUpsertInput) (*mcp.CallToolResult, any, error) {
		body := map[string]any{"url": in.URL, "note": in.Note, "keyword": in.Keyword}
		return s.proxy(ctx, req, "PUT", base+"/instagram/links/"+url.PathEscape(in.MediaID), body)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "instagram_link_delete",
		Description: "Remove o mapeamento publicação→link do Instagram (DELETE /instagram/links/{mediaId}).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in instagramMediaIDInput) (*mcp.CallToolResult, any, error) {
		return s.proxy(ctx, req, "DELETE", base+"/instagram/links/"+url.PathEscape(in.MediaID), nil)
	})
}
