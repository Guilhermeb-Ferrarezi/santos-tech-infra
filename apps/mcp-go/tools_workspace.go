package main

// Tools de quadros (Excalidraw), tarefas e glossário da API central. Guards de
// permissão (portalRead/permGuard/adminGuard) são responsabilidade da API — aqui
// só montamos a chamada e repassamos o Authorization do request MCP.

import (
	"context"
	"net/url"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ── Quadros ──────────────────────────────────────────────────────────────

type boardIDInput struct {
	ID string `json:"id" jsonschema:"id (UUID) do quadro"`
}

type boardCreateInput struct {
	Title string `json:"title" jsonschema:"título do quadro (até 200 caracteres)"`
}

type boardUpdateInput struct {
	ID string `json:"id" jsonschema:"id (UUID) do quadro"`
	// Dois modos mutuamente exclusivos na API: só title (renomeia, exige ser
	// dono) ou scene+sceneVersion (salva a cena, exige ser dono ou editor).
	Title        *string        `json:"title,omitempty" jsonschema:"novo título — envie sozinho para só renomear"`
	Scene        map[string]any `json:"scene,omitempty" jsonschema:"cena completa do Excalidraw (elements/appState/files); exige sceneVersion junto"`
	SceneVersion *int           `json:"sceneVersion,omitempty" jsonschema:"versão da cena para controle otimista; obrigatório junto com scene"`
}

type boardMembersListInput struct {
	ID string `json:"id" jsonschema:"id (UUID) do quadro"`
}

type boardMemberAddInput struct {
	ID    string `json:"id" jsonschema:"id (UUID) do quadro"`
	Email string `json:"email" jsonschema:"email de um admin ou professor já cadastrado"`
	Role  string `json:"role" jsonschema:"papel do membro: viewer ou editor"`
}

type boardMemberRemoveInput struct {
	ID     string `json:"id" jsonschema:"id (UUID) do quadro"`
	UserID string `json:"userId" jsonschema:"id numérico do usuário a remover"`
}

// ── Tarefas ──────────────────────────────────────────────────────────────

type taskIDInput struct {
	ID string `json:"id" jsonschema:"id (UUID) da tarefa"`
}

type taskCreateInput struct {
	Title         string  `json:"title" jsonschema:"título da tarefa"`
	Description   string  `json:"description,omitempty" jsonschema:"descrição livre"`
	CategoryID    *string `json:"categoryId,omitempty" jsonschema:"id (UUID) da categoria (ver task_categories_list)"`
	Status        string  `json:"status" jsonschema:"status: a_fazer, em_andamento, concluida ou cancelada"`
	Priority      string  `json:"priority" jsonschema:"prioridade: baixa, media ou alta"`
	DueDate       *string `json:"dueDate,omitempty" jsonschema:"prazo, formato RFC3339 (ex: 2026-08-20T00:00:00Z)"`
	ResponsavelID *int64  `json:"responsavelId,omitempty" jsonschema:"id do usuário responsável"`
}

// taskUpdateInput reflete o mesmo TaskInput do create: a API faz PUT como
// substituição completa, não patch parcial — title/status/priority continuam
// obrigatórios mesmo ao atualizar.
type taskUpdateInput struct {
	ID            string  `json:"id" jsonschema:"id (UUID) da tarefa"`
	Title         string  `json:"title" jsonschema:"título da tarefa"`
	Description   string  `json:"description,omitempty" jsonschema:"descrição livre"`
	CategoryID    *string `json:"categoryId,omitempty" jsonschema:"id (UUID) da categoria (ver task_categories_list)"`
	Status        string  `json:"status" jsonschema:"status: a_fazer, em_andamento, concluida ou cancelada"`
	Priority      string  `json:"priority" jsonschema:"prioridade: baixa, media ou alta"`
	DueDate       *string `json:"dueDate,omitempty" jsonschema:"prazo, formato RFC3339 (ex: 2026-08-20T00:00:00Z)"`
	ResponsavelID *int64  `json:"responsavelId,omitempty" jsonschema:"id do usuário responsável"`
}

type taskNotesListInput struct {
	ID string `json:"id" jsonschema:"id (UUID) da tarefa"`
}

type taskNoteAddInput struct {
	ID      string `json:"id" jsonschema:"id (UUID) da tarefa"`
	Content string `json:"content" jsonschema:"texto da nota"`
}

type taskCategoryCreateInput struct {
	Name string `json:"name" jsonschema:"nome da categoria"`
}

type taskCategoryUpdateInput struct {
	ID   string `json:"id" jsonschema:"id (UUID) da categoria"`
	Name string `json:"name" jsonschema:"novo nome da categoria"`
}

type taskCategoryIDInput struct {
	ID string `json:"id" jsonschema:"id (UUID) da categoria"`
}

// ── Glossário ────────────────────────────────────────────────────────────

type glossaryTermInput struct {
	Term      string `json:"term" jsonschema:"termo do glossário"`
	Definicao string `json:"definicao" jsonschema:"definição do termo"`
}

type glossaryTermUpdateInput struct {
	ID        string `json:"id" jsonschema:"id (UUID) do termo"`
	Term      string `json:"term" jsonschema:"termo do glossário"`
	Definicao string `json:"definicao" jsonschema:"definição do termo"`
}

type glossaryTermIDInput struct {
	ID string `json:"id" jsonschema:"id (UUID) do termo"`
}

// Espelham validTaskStatuses/validTaskPriorities de apps/api-go — duplicadas
// aqui porque mcp-go é um módulo Go separado. A validação real é sempre da
// API downstream; isto só evita um round-trip óbvio com valor claramente ruim.
var validTaskStatuses = map[string]bool{
	"a_fazer": true, "em_andamento": true, "concluida": true, "cancelada": true,
}
var validTaskPriorities = map[string]bool{
	"baixa": true, "media": true, "alta": true,
}

func (s *Server) addWorkspaceTools(srv *mcp.Server) {
	base := s.cfg.AuthBaseURL

	// ── Quadros ──────────────────────────────────────────────────────────

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "boards_list",
		Description: "Lista os quadros do usuário — próprios e compartilhados com ele, sem a cena (GET /boards).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, any, error) {
		return s.proxy(ctx, req, "GET", base+"/boards", nil)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "board_create",
		Description: "Cria um quadro Excalidraw vazio (POST /boards). Restrito a admins e professores.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in boardCreateInput) (*mcp.CallToolResult, any, error) {
		if in.Title == "" {
			return errResult("informe title"), nil, nil
		}
		return s.proxy(ctx, req, "POST", base+"/boards", in)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "board_get",
		Description: "Detalha um quadro pelo id, incluindo a cena completa (GET /boards/{id}).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in boardIDInput) (*mcp.CallToolResult, any, error) {
		return s.proxy(ctx, req, "GET", base+"/boards/"+url.PathEscape(in.ID), nil)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "board_update",
		Description: "Renomeia um quadro e/ou salva a cena com controle otimista de versão (PUT /boards/{id}). Envie só title para renomear, ou scene+sceneVersion para salvar a cena.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in boardUpdateInput) (*mcp.CallToolResult, any, error) {
		body := map[string]any{}
		if in.Title != nil {
			body["title"] = *in.Title
		}
		if in.Scene != nil {
			body["scene"] = in.Scene
			if in.SceneVersion == nil {
				return errResult("sceneVersion é obrigatório junto com scene"), nil, nil
			}
			body["sceneVersion"] = *in.SceneVersion
		}
		if len(body) == 0 {
			return errResult("nenhum campo para atualizar (title, ou scene+sceneVersion)"), nil, nil
		}
		return s.proxy(ctx, req, "PUT", base+"/boards/"+url.PathEscape(in.ID), body)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "board_delete",
		Description: "Exclui um quadro (DELETE /boards/{id}). Só o dono pode.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in boardIDInput) (*mcp.CallToolResult, any, error) {
		return s.proxy(ctx, req, "DELETE", base+"/boards/"+url.PathEscape(in.ID), nil)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "board_members_list",
		Description: "Lista os membros com quem um quadro foi compartilhado (GET /boards/{id}/members). Só o dono vê.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in boardMembersListInput) (*mcp.CallToolResult, any, error) {
		return s.proxy(ctx, req, "GET", base+"/boards/"+url.PathEscape(in.ID)+"/members", nil)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "board_member_add",
		Description: "Compartilha um quadro com um admin ou professor pelo email, como viewer ou editor (POST /boards/{id}/members). Só o dono pode.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in boardMemberAddInput) (*mcp.CallToolResult, any, error) {
		if in.Email == "" {
			return errResult("informe email"), nil, nil
		}
		if in.Role != "viewer" && in.Role != "editor" {
			return errResult("role deve ser viewer ou editor"), nil, nil
		}
		return s.proxy(ctx, req, "POST", base+"/boards/"+url.PathEscape(in.ID)+"/members", in)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "board_member_remove",
		Description: "Remove o acesso de um membro a um quadro (DELETE /boards/{id}/members/{userId}). Só o dono pode.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in boardMemberRemoveInput) (*mcp.CallToolResult, any, error) {
		return s.proxy(ctx, req, "DELETE", base+"/boards/"+url.PathEscape(in.ID)+"/members/"+url.PathEscape(in.UserID), nil)
	})

	// ── Tarefas ──────────────────────────────────────────────────────────

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "tasks_list",
		Description: "Lista tarefas (GET /tasks). Admin vê todas; demais usuários só as que criaram ou são responsáveis.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, any, error) {
		return s.proxy(ctx, req, "GET", base+"/tasks", nil)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "task_get",
		Description: "Detalha uma tarefa pelo id (GET /tasks/{id}).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in taskIDInput) (*mcp.CallToolResult, any, error) {
		return s.proxy(ctx, req, "GET", base+"/tasks/"+url.PathEscape(in.ID), nil)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "task_create",
		Description: "Cria uma tarefa (POST /tasks). title, status e priority são obrigatórios.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in taskCreateInput) (*mcp.CallToolResult, any, error) {
		if in.Title == "" {
			return errResult("informe title"), nil, nil
		}
		if !validTaskStatuses[in.Status] {
			return errResult("status inválido (use: a_fazer, em_andamento, concluida ou cancelada)"), nil, nil
		}
		if !validTaskPriorities[in.Priority] {
			return errResult("priority inválida (use: baixa, media ou alta)"), nil, nil
		}
		return s.proxy(ctx, req, "POST", base+"/tasks", in)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "task_update",
		Description: "Substitui os dados de uma tarefa (PUT /tasks/{id}). A API sobrescreve o registro inteiro — envie title, status e priority mesmo que não estejam mudando.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in taskUpdateInput) (*mcp.CallToolResult, any, error) {
		if in.Title == "" {
			return errResult("informe title"), nil, nil
		}
		if !validTaskStatuses[in.Status] {
			return errResult("status inválido (use: a_fazer, em_andamento, concluida ou cancelada)"), nil, nil
		}
		if !validTaskPriorities[in.Priority] {
			return errResult("priority inválida (use: baixa, media ou alta)"), nil, nil
		}
		return s.proxy(ctx, req, "PUT", base+"/tasks/"+url.PathEscape(in.ID), in)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "task_delete",
		Description: "Exclui uma tarefa (DELETE /tasks/{id}).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in taskIDInput) (*mcp.CallToolResult, any, error) {
		return s.proxy(ctx, req, "DELETE", base+"/tasks/"+url.PathEscape(in.ID), nil)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "task_notes_list",
		Description: "Lista as notas de uma tarefa (GET /tasks/{id}/notes).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in taskNotesListInput) (*mcp.CallToolResult, any, error) {
		return s.proxy(ctx, req, "GET", base+"/tasks/"+url.PathEscape(in.ID)+"/notes", nil)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "task_note_add",
		Description: "Adiciona uma nota a uma tarefa (POST /tasks/{id}/notes).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in taskNoteAddInput) (*mcp.CallToolResult, any, error) {
		if in.Content == "" {
			return errResult("informe content"), nil, nil
		}
		return s.proxy(ctx, req, "POST", base+"/tasks/"+url.PathEscape(in.ID)+"/notes", in)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "task_categories_list",
		Description: "Lista as categorias de tarefa disponíveis (GET /tasks/categories).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, any, error) {
		return s.proxy(ctx, req, "GET", base+"/tasks/categories", nil)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "task_category_create",
		Description: "Cria uma categoria de tarefa (POST /tasks/categories). Admin-only.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in taskCategoryCreateInput) (*mcp.CallToolResult, any, error) {
		if in.Name == "" {
			return errResult("informe name"), nil, nil
		}
		return s.proxy(ctx, req, "POST", base+"/tasks/categories", in)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "task_category_update",
		Description: "Renomeia uma categoria de tarefa (PUT /tasks/categories/{id}). Admin-only.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in taskCategoryUpdateInput) (*mcp.CallToolResult, any, error) {
		if in.Name == "" {
			return errResult("informe name"), nil, nil
		}
		return s.proxy(ctx, req, "PUT", base+"/tasks/categories/"+url.PathEscape(in.ID), map[string]any{"name": in.Name})
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "task_category_delete",
		Description: "Exclui uma categoria de tarefa (DELETE /tasks/categories/{id}). Admin-only.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in taskCategoryIDInput) (*mcp.CallToolResult, any, error) {
		return s.proxy(ctx, req, "DELETE", base+"/tasks/categories/"+url.PathEscape(in.ID), nil)
	})

	// ── Glossário ────────────────────────────────────────────────────────

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "glossary_list",
		Description: "Lista os termos do glossário (GET /glossary). Qualquer usuário autenticado pode ver.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, any, error) {
		return s.proxy(ctx, req, "GET", base+"/glossary", nil)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "glossary_create",
		Description: "Cria um termo de glossário (POST /glossary). Admin-only.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in glossaryTermInput) (*mcp.CallToolResult, any, error) {
		if in.Term == "" || in.Definicao == "" {
			return errResult("informe term e definicao"), nil, nil
		}
		return s.proxy(ctx, req, "POST", base+"/glossary", in)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "glossary_update",
		Description: "Substitui termo e definição de um termo de glossário (PUT /glossary/{id}). Admin-only.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in glossaryTermUpdateInput) (*mcp.CallToolResult, any, error) {
		if in.Term == "" || in.Definicao == "" {
			return errResult("informe term e definicao"), nil, nil
		}
		return s.proxy(ctx, req, "PUT", base+"/glossary/"+url.PathEscape(in.ID), map[string]any{
			"term": in.Term, "definicao": in.Definicao,
		})
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "glossary_delete",
		Description: "Exclui um termo de glossário (DELETE /glossary/{id}). Admin-only.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in glossaryTermIDInput) (*mcp.CallToolResult, any, error) {
		return s.proxy(ctx, req, "DELETE", base+"/glossary/"+url.PathEscape(in.ID), nil)
	})
}
