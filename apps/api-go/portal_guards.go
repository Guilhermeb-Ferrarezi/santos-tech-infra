package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"
)

// customRolePermCacheTTL: permissões de cargo customizado ficam em cache por 2 min.
// Cargos mudam raramente (só via painel admin); 2 min é uma janela aceitável.
// O cache é invalidado explicitamente ao atualizar/deletar o cargo (handlers_admin_custom_roles.go).
const customRolePermCacheTTL = 2 * time.Minute

// cachedCustomRole carrega as permissões do cargo pelo Redis (cache) e, em caso de
// miss ou erro, cai no banco. Fail-open: se o Redis falhar, a DB é usada diretamente.
func (s *Server) cachedCustomRole(ctx context.Context, roleID string) (*CustomRole, error) {
	const keyPrefix = "api-go:auth:custom_role:"
	key := keyPrefix + roleID
	if s.rdb != nil {
		rctx, cancel := context.WithTimeout(ctx, cacheOpTimeout)
		raw, err := s.rdb.Get(rctx, key).Bytes()
		cancel()
		if err == nil {
			var perms map[string][]string
			if json.Unmarshal(raw, &perms) == nil {
				return &CustomRole{ID: roleID, Permissions: perms}, nil
			}
		}
	}
	cr, err := s.getCustomRole(ctx, roleID)
	if err != nil || cr == nil {
		return cr, err
	}
	if s.rdb != nil {
		if b, merr := json.Marshal(cr.Permissions); merr == nil {
			wctx, wcancel := context.WithTimeout(ctx, cacheOpTimeout)
			if serr := s.rdb.Set(wctx, key, b, customRolePermCacheTTL).Err(); serr != nil {
				slog.Warn("custom_role_perms: falha ao gravar cache", "role", roleID, "err", serr)
			}
			wcancel()
		}
	}
	return cr, nil
}

// invalidateCustomRoleCache remove a entrada de permissões do cargo do Redis.
// Chamado em update/delete para encurtar a janela de dados desatualizados.
func (s *Server) invalidateCustomRoleCache(ctx context.Context, roleID string) {
	if s.rdb == nil {
		return
	}
	if err := s.rdb.Del(ctx, "api-go:auth:custom_role:"+roleID).Err(); err != nil {
		slog.Warn("custom_role_perms: falha ao invalidar cache", "role", roleID, "err", err)
	}
}

// permGuard libera a rota para admin (sempre), professor (se allowTeacher), ou
// quem tiver resource:action nas permissões individuais ou do cargo.
func (s *Server) permGuard(resource, action string, allowTeacher bool, next http.HandlerFunc) http.HandlerFunc {
	return s.authGuard(func(w http.ResponseWriter, r *http.Request) {
		u, err := s.cachedUserByID(r.Context(), userIDFrom(r))
		if err != nil {
			writeErr(w, err)
			return
		}
		if hasPerm(u, s.rolePermsOf(r.Context(), u), resource, action, allowTeacher) {
			next(w, r)
			return
		}
		writeErr(w, appErr(http.StatusForbidden, "FORBIDDEN", "Acesso restrito"))
	})
}

// portalRead: leitura de uma área (ex.: "portal_metas") — admin/professor OU
// cargo com {resource}:read.
func (s *Server) portalRead(resource string, next http.HandlerFunc) http.HandlerFunc {
	return s.permGuard(resource, "read", true, next)
}

// portalWrite: escrita de uma área — admin OU cargo com {resource}:write
// (professor NÃO, igual ao adminGuard atual). Exclusão destrutiva segue
// adminGuard+sudoGuard, não passa por aqui.
func (s *Server) portalWrite(resource string, next http.HandlerFunc) http.HandlerFunc {
	return s.permGuard(resource, "write", false, next)
}

// portalDiario: diário de aula — admin/professor OU cargo com
// portal_diario:read|write. É a exceção à regra "professor não escreve":
// registrar a aula que acabou de dar é obrigação do PRÓPRIO professor (ver
// portal_diario.go), então allowTeacher vale pras duas ações.
func (s *Server) portalDiario(action string, next http.HandlerFunc) http.HandlerFunc {
	return s.permGuard("portal_diario", action, true, next)
}

// portalCorrigir: correção de respostas — admin/professor OU cargo com
// portal_correcao:corrigir (professor corrige, por isso allowTeacher).
func (s *Server) portalCorrigir(next http.HandlerFunc) http.HandlerFunc {
	return s.permGuard("portal_correcao", "corrigir", true, next)
}

// portalAnyRead: rotas transversais (overview, busca de usuários) — admin/
// professor OU cargo com QUALQUER permissão de leitura no portal (portal_*:read).
func (s *Server) portalAnyRead(next http.HandlerFunc) http.HandlerFunc {
	return s.authGuard(func(w http.ResponseWriter, r *http.Request) {
		u, err := s.cachedUserByID(r.Context(), userIDFrom(r))
		if err != nil {
			writeErr(w, err)
			return
		}
		if u != nil {
			if u.Role == RoleAdmin || u.Role == RoleTeacher {
				next(w, r)
				return
			}
			for res, acts := range s.effectivePerms(r.Context(), u) {
				if strings.HasPrefix(res, "portal_") && slices.Contains(acts, "read") {
					next(w, r)
					return
				}
			}
		}
		writeErr(w, appErr(http.StatusForbidden, "FORBIDDEN", "Acesso restrito"))
	})
}
