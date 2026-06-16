package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Comandos de operação pelo WhatsApp (frente 3).
//
// Mensagens recebidas com prefixo "/" vindas de um ADMIN do tenant acionam ações
// na Coolify e respondem no MESMO chat (em grupo, o JID ...@g.us). Qualquer outro
// remetente é IGNORADO silenciosamente (fail-closed, sem vazar a existência dos
// comandos). /redeploy é registrado via slog (quem, qual app, resultado).
//
// Comandos:
//
//	/status                  → lista os apps da Coolify e se estão de pé
//	/redeploy <nome|parte>   → dispara deploy do app correspondente
//	/logs <nome>             → últimas ~15 linhas do último deployment
//	/ajuda                   → lista os comandos

const coolifyLogLines = 15

// handleAdminCommand intercepta uma mensagem recebida. Se o texto começa com "/"
// e o remetente é admin do tenant, trata como comando de operação e responde no
// chat de origem (chatJid — em grupo, o ...@g.us), retornando handled=true para
// que o fluxo normal de IA/atendimento NÃO seja executado.
//
//   - from:    telefone do remetente (E.164/cru) — usado para checar admin.
//   - chatJid: destino da resposta (número do contato OU group id ...@g.us).
//   - instance: instância da Evolution por onde a mensagem chegou (para responder
//     pelo mesmo número); se vazia, cai no EVOLUTION_INSTANCE da config.
//
// Fail-closed: na dúvida (remetente não-admin, sem texto, etc.) retorna
// handled=false e NÃO responde nada.
func (s *Server) handleAdminCommand(ctx context.Context, from, chatJid, instance, text string) (handled bool, err error) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "/") {
		return false, nil
	}

	// fail-closed: só admin opera. Não-admin → ignora em silêncio (não vaza).
	if !s.isAdminSender(ctx, from) {
		return false, nil
	}

	fields := strings.Fields(text)
	cmd := strings.ToLower(strings.TrimPrefix(fields[0], "/"))
	arg := strings.TrimSpace(strings.TrimPrefix(text, fields[0]))

	switch cmd {
	case "ajuda", "help", "comandos":
		s.replyCommand(ctx, chatJid, instance, ajudaText())
		return true, nil
	case "status":
		s.cmdStatus(ctx, chatJid, instance)
		return true, nil
	case "redeploy", "deploy":
		s.cmdRedeploy(ctx, from, chatJid, instance, arg)
		return true, nil
	case "logs", "log":
		s.cmdLogs(ctx, chatJid, instance, arg)
		return true, nil
	default:
		// Comando desconhecido vindo de admin: responde a ajuda (já é admin, sem vazamento).
		s.replyCommand(ctx, chatJid, instance, "Comando desconhecido.\n\n"+ajudaText())
		return true, nil
	}
}

func ajudaText() string {
	return strings.Join([]string{
		"Comandos de operação:",
		"/status — lista os apps da Coolify e se estão de pé",
		"/redeploy <nome> — dispara deploy do app",
		"/logs <nome> — últimas linhas do último deployment",
		"/ajuda — esta mensagem",
	}, "\n")
}

// ── Comandos ──────────────────────────────────────────────────────────────────

func (s *Server) cmdStatus(ctx context.Context, chatJid, instance string) {
	cool := s.coolify()
	if !cool.Enabled() {
		s.replyCommand(ctx, chatJid, instance, "Coolify não configurada.")
		return
	}
	apps, err := cool.Applications(ctx)
	if err != nil {
		s.logger.Error("comando /status: falha ao listar apps", "err", err)
		s.replyCommand(ctx, chatJid, instance, "Falha ao consultar a Coolify.")
		return
	}
	if len(apps) == 0 {
		s.replyCommand(ctx, chatJid, instance, "Nenhuma aplicação encontrada na Coolify.")
		return
	}
	var b strings.Builder
	b.WriteString("Apps na Coolify:\n")
	for _, a := range apps {
		mark := "🔴"
		if coolifyAppUp(a.Status) {
			mark = "🟢"
		}
		status := a.Status
		if status == "" {
			status = "desconhecido"
		}
		fmt.Fprintf(&b, "%s %s (%s)\n", mark, a.Name, status)
	}
	s.replyCommand(ctx, chatJid, instance, strings.TrimRight(b.String(), "\n"))
}

func (s *Server) cmdRedeploy(ctx context.Context, from, chatJid, instance, arg string) {
	cool := s.coolify()
	if !cool.Enabled() {
		s.replyCommand(ctx, chatJid, instance, "Coolify não configurada.")
		return
	}
	if arg == "" {
		s.replyCommand(ctx, chatJid, instance, "Uso: /redeploy <nome-ou-parte-do-nome>")
		return
	}
	apps, err := cool.Applications(ctx)
	if err != nil {
		s.logger.Error("comando /redeploy: falha ao listar apps", "err", err)
		s.replyCommand(ctx, chatJid, instance, "Falha ao consultar a Coolify.")
		return
	}
	app, candidates := ResolveApp(apps, arg)
	if app == nil {
		s.replyCommand(ctx, chatJid, instance, ambiguousOrNone(arg, candidates))
		return
	}

	// /redeploy é destrutivo-ish: registra quem disparou, qual app e o resultado.
	s.logger.Info("comando /redeploy disparado", "from", from, "app", app.Name, "uuid", app.UUID)

	if err := cool.Deploy(ctx, app.UUID); err != nil {
		s.logger.Error("comando /redeploy: falha no deploy", "from", from, "app", app.Name, "err", err)
		s.replyCommand(ctx, chatJid, instance, fmt.Sprintf("Falha ao disparar deploy de %s.", app.Name))
		return
	}
	s.logger.Info("comando /redeploy: deploy disparado", "from", from, "app", app.Name)
	s.replyCommand(ctx, chatJid, instance, fmt.Sprintf("🚀 Deploy de %s disparado.", app.Name))
}

func (s *Server) cmdLogs(ctx context.Context, chatJid, instance, arg string) {
	cool := s.coolify()
	if !cool.Enabled() {
		s.replyCommand(ctx, chatJid, instance, "Coolify não configurada.")
		return
	}
	if arg == "" {
		s.replyCommand(ctx, chatJid, instance, "Uso: /logs <nome>")
		return
	}
	apps, err := cool.Applications(ctx)
	if err != nil {
		s.logger.Error("comando /logs: falha ao listar apps", "err", err)
		s.replyCommand(ctx, chatJid, instance, "Falha ao consultar a Coolify.")
		return
	}
	app, candidates := ResolveApp(apps, arg)
	if app == nil {
		s.replyCommand(ctx, chatJid, instance, ambiguousOrNone(arg, candidates))
		return
	}
	logs, err := cool.DeploymentLogs(ctx, app.UUID, coolifyLogLines)
	if err != nil {
		s.logger.Error("comando /logs: falha ao buscar logs", "app", app.Name, "err", err)
		s.replyCommand(ctx, chatJid, instance, fmt.Sprintf("Falha ao buscar logs de %s.", app.Name))
		return
	}
	if logs == "" {
		s.replyCommand(ctx, chatJid, instance, fmt.Sprintf("Sem logs de deployment para %s.", app.Name))
		return
	}
	s.replyCommand(ctx, chatJid, instance, fmt.Sprintf("Logs de %s (últimas %d linhas):\n%s", app.Name, coolifyLogLines, logs))
}

// ── Auxiliares ────────────────────────────────────────────────────────────────

func ambiguousOrNone(query string, candidates []CoolifyApp) string {
	if len(candidates) == 0 {
		return fmt.Sprintf("Nenhum app corresponde a %q.", query)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Mais de um app corresponde a %q. Seja mais específico:\n", query)
	for _, a := range candidates {
		fmt.Fprintf(&b, "- %s\n", a.Name)
	}
	return strings.TrimRight(b.String(), "\n")
}

// coolify cria um CoolifyClient a partir da config (env). Enabled()=false quando
// COOLIFY_API_URL/COOLIFY_API_TOKEN ausentes.
func (s *Server) coolify() *CoolifyClient {
	return NewCoolifyClient(s.cfg.CoolifyAPIURL, s.cfg.CoolifyAPIToken)
}

// isAdminSender compara o telefone normalizado do remetente com a lista de admins
// do tenant (admin_whatsapp_numbers, com fallback ao número único legado). Erro de
// consulta → false (fail-closed: sem confirmação de admin, não opera).
func (s *Server) isAdminSender(ctx context.Context, from string) bool {
	want := normalizePhone(from)
	if want == "" {
		return false
	}
	var legacy string
	var numbersRaw []byte
	err := s.pool.QueryRow(ctx,
		`SELECT admin_whatsapp_number, admin_whatsapp_numbers FROM tenant_config WHERE tenant_id = $1`,
		s.cfg.TenantID,
	).Scan(&legacy, &numbersRaw)
	if err != nil {
		s.logger.Error("comando: falha ao carregar admins do tenant", "err", err)
		return false
	}
	if normalizePhone(legacy) == want {
		return true
	}
	var numbers []string
	if len(numbersRaw) > 0 {
		if err := json.Unmarshal(numbersRaw, &numbers); err != nil {
			return false
		}
	}
	for _, n := range numbers {
		if normalizePhone(n) == want {
			return true
		}
	}
	return false
}

// replyCommand responde no chat de origem via Evolution (mesmo número/instância).
func (s *Server) replyCommand(ctx context.Context, chatJid, instance, text string) {
	inst := instance
	if inst == "" {
		inst = s.cfg.EvolutionInstance
	}
	if s.cfg.EvolutionAPIURL == "" || s.cfg.EvolutionAPIKey == "" || inst == "" {
		s.logger.Error("comando: Evolution não configurada para responder", "chat", chatJid)
		return
	}
	sendCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	evo := NewEvolutionClient(s.cfg.EvolutionAPIURL, s.cfg.EvolutionAPIKey, inst)
	if err := evo.SendText(sendCtx, chatJid, text); err != nil {
		s.logger.Error("comando: falha ao responder no WhatsApp", "err", err, "chat", chatJid)
	}
}
