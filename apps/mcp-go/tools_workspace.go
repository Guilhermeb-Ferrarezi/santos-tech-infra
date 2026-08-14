package main

// Tools de quadros (Excalidraw), tarefas e glossário da API central. Guards de
// permissão (portalRead/permGuard/adminGuard) são responsabilidade da API — aqui
// só montamos a chamada e repassamos o Authorization do request MCP.
//
// Uso interno (só nós usamos este MCP): get+list de mesmo recurso e os CRUDs de
// baixo risco (task_category, glossary, board_member — nada de dinheiro/segredo/
// dado sensível por trás) ficam consolidados numa tool só, pra reduzir o total
// de tools que o cliente MCP carrega. board/task continuam com create/update/
// delete separados porque a ação errada ali é mais cara (perde board/tarefa).

import (
	"context"
	"net/url"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ── Quadros ──────────────────────────────────────────────────────────────

type boardIDInput struct {
	ID string `json:"id" jsonschema:"id (UUID) do quadro"`
}

type boardGetInput struct {
	ID string `json:"id,omitempty" jsonschema:"id (UUID) do quadro; omitido lista todos os quadros do usuário"`
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

// boardMemberActionInput consolida list/add/remove numa tool só — é gestão de
// compartilhamento de baixo risco (não apaga o quadro nem dado nenhum, só quem
// pode vê-lo), então o schema mais "solto" (campos condicionais por action) vale
// a pena pra reduzir de 3 tools pra 1.
type boardMemberActionInput struct {
	Action  string  `json:"action" jsonschema:"list, add ou remove"`
	BoardID string  `json:"boardId" jsonschema:"id (UUID) do quadro"`
	Email   *string `json:"email,omitempty" jsonschema:"email de um admin ou professor já cadastrado — obrigatório em add"`
	Role    *string `json:"role,omitempty" jsonschema:"papel do membro: viewer ou editor — obrigatório em add"`
	UserID  *string `json:"userId,omitempty" jsonschema:"id numérico do usuário a remover — obrigatório em remove"`
}

// ── Tarefas ──────────────────────────────────────────────────────────────

type taskIDInput struct {
	ID string `json:"id" jsonschema:"id (UUID) da tarefa"`
}

type taskGetInput struct {
	ID string `json:"id,omitempty" jsonschema:"id (UUID) da tarefa; omitido lista as tarefas visíveis ao usuário"`
}

type taskCreateInput struct {
	Title         string  `json:"title" jsonschema:"título da tarefa"`
	Description   string  `json:"description,omitempty" jsonschema:"descrição livre"`
	CategoryID    *string `json:"categoryId,omitempty" jsonschema:"id (UUID) da categoria (ver task_category action=list)"`
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
	CategoryID    *string `json:"categoryId,omitempty" jsonschema:"id (UUID) da categoria (ver task_category action=list)"`
	Status        string  `json:"status" jsonschema:"status: a_fazer, em_andamento, concluida ou cancelada"`
	Priority      string  `json:"priority" jsonschema:"prioridade: baixa, media ou alta"`
	DueDate       *string `json:"dueDate,omitempty" jsonschema:"prazo, formato RFC3339 (ex: 2026-08-20T00:00:00Z)"`
	ResponsavelID *int64  `json:"responsavelId,omitempty" jsonschema:"id do usuário responsável"`
}

// taskNoteActionInput consolida list/add — notas de tarefa não têm update nem
// delete na API, então não há ação destrutiva a proteger aqui.
type taskNoteActionInput struct {
	Action  string `json:"action" jsonschema:"list ou add"`
	ID      string `json:"id" jsonschema:"id (UUID) da tarefa"`
	Content string `json:"content,omitempty" jsonschema:"texto da nota — obrigatório em add"`
}

// taskCategoryActionInput consolida list/create/update/delete — categoria é só
// um nome, sem dado sensível por trás, então uma tool com action cobre o CRUD
// inteiro sem perder nada em segurança.
type taskCategoryActionInput struct {
	Action string  `json:"action" jsonschema:"list, create, update ou delete"`
	ID     *string `json:"id,omitempty" jsonschema:"id (UUID) da categoria — obrigatório em update/delete"`
	Name   *string `json:"name,omitempty" jsonschema:"nome da categoria — obrigatório em create/update"`
}

// ── Glossário ────────────────────────────────────────────────────────────

// glossaryActionInput consolida list/create/update/delete pelo mesmo motivo de
// taskCategoryActionInput: termo+definição não é dado sensível nem destrutivo
// caro.
type glossaryActionInput struct {
	Action    string  `json:"action" jsonschema:"list, create, update ou delete"`
	ID        *string `json:"id,omitempty" jsonschema:"id (UUID) do termo — obrigatório em update/delete"`
	Term      *string `json:"term,omitempty" jsonschema:"termo do glossário — obrigatório em create/update"`
	Definicao *string `json:"definicao,omitempty" jsonschema:"definição do termo — obrigatório em create/update"`
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
		Name:        "board_create",
		Description: "Cria um quadro Excalidraw vazio (POST /boards). Restrito a admins e professores.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in boardCreateInput) (*mcp.CallToolResult, any, error) {
		if in.Title == "" {
			return errResult("informe title"), nil, nil
		}
		return s.proxy(ctx, req, "POST", base+"/boards", in)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name: "board_get",
		Description: "Sem id: lista os quadros do usuário — próprios e compartilhados, sem a cena (GET /boards). " +
			"Com id: detalha um quadro, incluindo a cena completa (GET /boards/{id}).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in boardGetInput) (*mcp.CallToolResult, any, error) {
		if in.ID == "" {
			return s.proxy(ctx, req, "GET", base+"/boards", nil)
		}
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
		Name: "board_member",
		Description: "Gerencia membros de um quadro: action=list (GET /boards/{boardId}/members), " +
			"add (POST, email+role) ou remove (DELETE .../members/{userId}). Só o dono do quadro pode add/remove.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in boardMemberActionInput) (*mcp.CallToolResult, any, error) {
		membersURL := base + "/boards/" + url.PathEscape(in.BoardID) + "/members"
		switch in.Action {
		case "list":
			return s.proxy(ctx, req, "GET", membersURL, nil)
		case "add":
			if in.Email == nil || *in.Email == "" {
				return errResult("email é obrigatório para add"), nil, nil
			}
			if in.Role == nil || (*in.Role != "viewer" && *in.Role != "editor") {
				return errResult("role deve ser viewer ou editor"), nil, nil
			}
			return s.proxy(ctx, req, "POST", membersURL, map[string]any{"email": *in.Email, "role": *in.Role})
		case "remove":
			if in.UserID == nil || *in.UserID == "" {
				return errResult("userId é obrigatório para remove"), nil, nil
			}
			return s.proxy(ctx, req, "DELETE", membersURL+"/"+url.PathEscape(*in.UserID), nil)
		default:
			return errResult("action deve ser list, add ou remove"), nil, nil
		}
	})

	// ── Tarefas ──────────────────────────────────────────────────────────

	mcp.AddTool(srv, &mcp.Tool{
		Name: "task_get",
		Description: "Sem id: lista tarefas (GET /tasks) — admin vê todas, demais usuários só as que criaram ou são responsáveis. " +
			"Com id: detalha uma tarefa (GET /tasks/{id}).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in taskGetInput) (*mcp.CallToolResult, any, error) {
		if in.ID == "" {
			return s.proxy(ctx, req, "GET", base+"/tasks", nil)
		}
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
		Name:        "task_note",
		Description: "Gerencia notas de uma tarefa: action=list (GET /tasks/{id}/notes) ou add (POST, content).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in taskNoteActionInput) (*mcp.CallToolResult, any, error) {
		notesURL := base + "/tasks/" + url.PathEscape(in.ID) + "/notes"
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
		Name: "task_category",
		Description: "Gerencia categorias de tarefa: action=list (GET /tasks/categories), create (POST), " +
			"update (PUT /tasks/categories/{id}) ou delete (DELETE /tasks/categories/{id}). Admin-only, exceto list.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in taskCategoryActionInput) (*mcp.CallToolResult, any, error) {
		switch in.Action {
		case "list":
			return s.proxy(ctx, req, "GET", base+"/tasks/categories", nil)
		case "create":
			if in.Name == nil || *in.Name == "" {
				return errResult("name é obrigatório para create"), nil, nil
			}
			return s.proxy(ctx, req, "POST", base+"/tasks/categories", map[string]any{"name": *in.Name})
		case "update":
			if in.ID == nil || *in.ID == "" {
				return errResult("id é obrigatório para update"), nil, nil
			}
			if in.Name == nil || *in.Name == "" {
				return errResult("name é obrigatório para update"), nil, nil
			}
			return s.proxy(ctx, req, "PUT", base+"/tasks/categories/"+url.PathEscape(*in.ID), map[string]any{"name": *in.Name})
		case "delete":
			if in.ID == nil || *in.ID == "" {
				return errResult("id é obrigatório para delete"), nil, nil
			}
			return s.proxy(ctx, req, "DELETE", base+"/tasks/categories/"+url.PathEscape(*in.ID), nil)
		default:
			return errResult("action deve ser list, create, update ou delete"), nil, nil
		}
	})

	// ── Glossário ────────────────────────────────────────────────────────

	mcp.AddTool(srv, &mcp.Tool{
		Name: "glossary",
		Description: "Gerencia o glossário: action=list (GET /glossary, qualquer usuário autenticado), create (POST), " +
			"update (PUT /glossary/{id}) ou delete (DELETE /glossary/{id}). create/update/delete são admin-only.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in glossaryActionInput) (*mcp.CallToolResult, any, error) {
		switch in.Action {
		case "list":
			return s.proxy(ctx, req, "GET", base+"/glossary", nil)
		case "create":
			if in.Term == nil || *in.Term == "" || in.Definicao == nil || *in.Definicao == "" {
				return errResult("term e definicao são obrigatórios para create"), nil, nil
			}
			return s.proxy(ctx, req, "POST", base+"/glossary", map[string]any{"term": *in.Term, "definicao": *in.Definicao})
		case "update":
			if in.ID == nil || *in.ID == "" {
				return errResult("id é obrigatório para update"), nil, nil
			}
			if in.Term == nil || *in.Term == "" || in.Definicao == nil || *in.Definicao == "" {
				return errResult("term e definicao são obrigatórios para update"), nil, nil
			}
			return s.proxy(ctx, req, "PUT", base+"/glossary/"+url.PathEscape(*in.ID), map[string]any{
				"term": *in.Term, "definicao": *in.Definicao,
			})
		case "delete":
			if in.ID == nil || *in.ID == "" {
				return errResult("id é obrigatório para delete"), nil, nil
			}
			return s.proxy(ctx, req, "DELETE", base+"/glossary/"+url.PathEscape(*in.ID), nil)
		default:
			return errResult("action deve ser list, create, update ou delete"), nil, nil
		}
	})
}
