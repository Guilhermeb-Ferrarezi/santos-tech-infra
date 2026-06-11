package main

import (
	"context"
	"net/http"
	"strings"
)

// permGuard libera a rota para admin (sempre), professor (se allowTeacher) e
// cargos personalizados (role 4) cujo cargo tenha a permissão resource:action.
// allowTeacher reflete o modelo atual: leitura é admin+professor; escrita é
// admin-only (professor não escreve) — exceto correção, que professor faz.
func (s *Server) permGuard(resource, action string, allowTeacher bool, next http.HandlerFunc) http.HandlerFunc {
	return s.authGuard(func(w http.ResponseWriter, r *http.Request) {
		u, err := s.userByID(r.Context(), userIDFrom(r))
		if err != nil {
			writeErr(w, err)
			return
		}
		if u != nil {
			if u.Role == RoleAdmin {
				next(w, r)
				return
			}
			if allowTeacher && u.Role == RoleTeacher {
				next(w, r)
				return
			}
			if u.Role == RoleCustom && u.CustomRoleID != nil && s.customRoleHasPerm(r.Context(), *u.CustomRoleID, resource, action) {
				next(w, r)
				return
			}
		}
		writeErr(w, appErr(http.StatusForbidden, "FORBIDDEN", "Acesso restrito"))
	})
}

// customRoleHasPerm carrega o cargo e verifica se resource tem a ação.
func (s *Server) customRoleHasPerm(ctx context.Context, roleID, resource, action string) bool {
	cr, err := s.getCustomRole(ctx, roleID)
	if err != nil || cr == nil {
		return false
	}
	for _, a := range cr.Permissions[resource] {
		if a == action {
			return true
		}
	}
	return false
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

// portalCorrigir: correção de respostas — admin/professor OU cargo com
// portal_correcao:corrigir (professor corrige, por isso allowTeacher).
func (s *Server) portalCorrigir(next http.HandlerFunc) http.HandlerFunc {
	return s.permGuard("portal_correcao", "corrigir", true, next)
}

// portalAnyRead: rotas transversais (overview, busca de usuários) — admin/
// professor OU cargo com QUALQUER permissão de leitura no portal (portal_*:read).
func (s *Server) portalAnyRead(next http.HandlerFunc) http.HandlerFunc {
	return s.authGuard(func(w http.ResponseWriter, r *http.Request) {
		u, err := s.userByID(r.Context(), userIDFrom(r))
		if err != nil {
			writeErr(w, err)
			return
		}
		if u != nil {
			if u.Role == RoleAdmin || u.Role == RoleTeacher {
				next(w, r)
				return
			}
			if u.Role == RoleCustom && u.CustomRoleID != nil {
				if cr, err := s.getCustomRole(r.Context(), *u.CustomRoleID); err == nil && cr != nil {
					for res, acts := range cr.Permissions {
						if strings.HasPrefix(res, "portal_") {
							for _, a := range acts {
								if a == "read" {
									next(w, r)
									return
								}
							}
						}
					}
				}
			}
		}
		writeErr(w, appErr(http.StatusForbidden, "FORBIDDEN", "Acesso restrito"))
	})
}
