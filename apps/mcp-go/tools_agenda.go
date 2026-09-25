package main

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Tools da Agenda (api.santos-tech.com/agenda). Mesmo padrão de addWorkspaceTools:
// guards de permissão (permGuard "agenda":read/write, admin/professor ou cargo
// com a permissão) são responsabilidade da API — aqui só montamos a chamada e
// repassamos o Authorization do request MCP (proxy, não proxyBot: a rota não
// fica atrás de adminGuard, então o token OAuth do MCP passa sem o bloqueio de
// "aud" que list_users/create_user/update_user levam em tools_auth.go).
//
// Sem update/delete de propósito por ora — o caso de uso inicial é só
// popular a Agenda a partir de dados migrados (ex.: grade do Notion), então
// list+create bastam; se aparecer necessidade de editar/remover por aqui,
// adicionar depois seguindo o mesmo padrão de agenda_event_create.

var validAgendaTiposMCP = map[string]bool{
	"aula_turma": true, "aula_particular": true, "aula_experimental": true,
	"avulso": true, "corujao": true, "mix": true, "dia_inteiro": true,
}

type agendaEventCreateInput struct {
	Tipo         string  `json:"tipo" jsonschema:"aula_turma, aula_particular, aula_experimental, avulso, corujao, mix ou dia_inteiro"`
	Titulo       string  `json:"titulo" jsonschema:"título do evento"`
	AlunoOuGrupo *string `json:"alunoOuGrupo,omitempty" jsonschema:"nome do aluno ou do grupo/turma"`
	// professorOuResponsavelId é o id numérico de usuário do auth central — se
	// não souber o id, omita e informe só o nome livre em observações/notas;
	// a API aceita null aqui (não é obrigatório).
	ProfessorOuResponsavelID *int64  `json:"professorOuResponsavelId,omitempty" jsonschema:"id numérico do usuário responsável (auth central) — omita se não souber"`
	Conteudo                 *string `json:"conteudo,omitempty" jsonschema:"conteúdo da aula (só faz sentido pra tipos de aula)"`
	Jogo                     *string `json:"jogo,omitempty" jsonschema:"jogo/atividade (só faz sentido pra tipos de Arena: avulso, corujao, mix)"`
	QtdPessoas               *int    `json:"qtdPessoas,omitempty" jsonschema:"quantidade de pessoas (Arena)"`
	// computadoresUsados null = "não informado": fica de fora da soma de
	// capacidade do motor de conflito em vez de virar 0 disfarçado. Nunca
	// invente um número aqui só pra preencher o campo.
	ComputadoresUsados *int   `json:"computadoresUsados,omitempty" jsonschema:"quantidade de PCs usados; omita se não houver contagem confiável (vira null = não informado, não conta como 0)"`
	DataInicio         string `json:"dataInicio" jsonschema:"data de início, formato YYYY-MM-DD"`
	HoraInicio         string `json:"horaInicio" jsonschema:"hora de início, formato HH:MM"`
	HoraFim            string `json:"horaFim" jsonschema:"hora de fim, formato HH:MM — precisa ser depois da hora de início"`
	// recorrencia só é escolha do cliente em aula_particular (aluno sozinho com
	// horário fixo); aula_turma é sempre semanal e os demais tipos nunca repetem
	// — a API força isso sozinha.
	Recorrencia string `json:"recorrencia,omitempty" jsonschema:"'semanal' pra aula_particular que se repete toda semana (aluno sozinho com horário fixo); omita pra particular avulsa. Ignorado nos demais tipos (aula_turma é sempre semanal). Aluno sozinho NUNCA vai como aula_turma"`
	// diaSemana é obrigatório em evento semanal (aula_turma, ou aula_particular
	// com recorrencia=semanal) e ignorado nos demais.
	DiaSemana *int `json:"diaSemana,omitempty" jsonschema:"dia da semana (0=domingo..6=sábado) — obrigatório se tipo=aula_turma ou aula_particular semanal, ignorado nos demais"`
	// dataFimRecorrencia null (omitido) num evento semanal = recorrência
	// INDEFINIDA (sem data de término conhecida) — estado válido, não invente
	// uma data-teto arbitrária só pra preencher o campo.
	DataFimRecorrencia *string `json:"dataFimRecorrencia,omitempty" jsonschema:"data de fim da recorrência (YYYY-MM-DD), só pra evento semanal; omita pra recorrência indefinida (sem data de término conhecida) em vez de inventar uma data"`
	// dataFim é o último dia (inclusive) de um evento "dia_inteiro" — nada a
	// ver com dataFimRecorrencia (recorrência semanal).
	DataFim           *string `json:"dataFim,omitempty" jsonschema:"último dia (inclusive) do intervalo, só pra tipo=dia_inteiro"`
	StatusPreparo     *string `json:"statusPreparo,omitempty" jsonschema:"nao_aplica, pendente ou pronto — só relevante pra tipos de Arena (avulso/corujao/mix); a API define um default sensato se omitido"`
	Notas             string  `json:"notas,omitempty" jsonschema:"observações livres"`
	ConfirmarConflito bool    `json:"confirmarConflito,omitempty" jsonschema:"reenvie true depois de um 409 AGENDA_CONFLITO_POLITICA pra confirmar a gravação mesmo com o conflito de política sinalizado; não afeta o bloqueio de capacidade (400 AGENDA_CAPACIDADE_EXCEDIDA), que não tem override"`
}

func (s *Server) addAgendaTools(srv *mcp.Server) {
	base := s.cfg.AuthBaseURL

	mcp.AddTool(srv, &mcp.Tool{
		Name: "agenda_events_list",
		Description: "Lista TODOS os eventos da Agenda (aulas de turma, particulares, experimentais, avulso/Corujão/Mix, dia inteiro) — sem filtro nem paginação, mesma convenção de GET /agenda/eventos. " +
			"Use antes de agenda_event_create pra checar se um evento equivalente (mesmo título/dia/horário) já existe, evitando duplicidade.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, any, error) {
		return s.proxy(ctx, req, "GET", base+"/agenda/eventos", nil)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name: "agenda_event_create",
		Description: "Cria um evento na Agenda (POST /agenda/eventos) — aula de turma recorrente, aula particular/experimental, avulso/Corujão/Mix, ou banner de dia inteiro. " +
			"A API valida e checa capacidade/conflito antes de gravar: 400 AGENDA_CAPACIDADE_EXCEDIDA (teto de 10 PCs, sem override) ou 409 AGENDA_CONFLITO_POLITICA (Arena sobre aula — reenvie com confirmarConflito:true pra confirmar mesmo assim). " +
			"NUNCA invente professorOuResponsavelId, computadoresUsados ou dataFimRecorrencia — omita o campo (vira null, estado válido) em vez de preencher um valor que não veio da fonte de dado real.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in agendaEventCreateInput) (*mcp.CallToolResult, any, error) {
		if in.Titulo == "" {
			return errResult("informe titulo"), nil, nil
		}
		if !validAgendaTiposMCP[in.Tipo] {
			return errResult("tipo inválido (use: aula_turma, aula_particular, aula_experimental, avulso, corujao, mix ou dia_inteiro)"), nil, nil
		}
		if in.DataInicio == "" {
			return errResult("informe dataInicio (YYYY-MM-DD)"), nil, nil
		}
		if in.Tipo == "aula_turma" && in.DiaSemana == nil {
			return errResult("tipo=aula_turma exige diaSemana (0=domingo..6=sábado)"), nil, nil
		}
		if in.Tipo == "aula_particular" && in.Recorrencia == "semanal" && in.DiaSemana == nil {
			return errResult("aula_particular semanal exige diaSemana (0=domingo..6=sábado)"), nil, nil
		}
		return s.proxy(ctx, req, "POST", base+"/agenda/eventos", in)
	})
}
