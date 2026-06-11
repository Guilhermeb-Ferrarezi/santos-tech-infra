package main

import "net/http"

// permGuard libera a rota para admin ou professor (role 2/3) e também para
// cargos personalizados (role 4) cujo cargo tenha a permissão resource:action.
// Os guards padrão (staffGuard/adminGuard) só olham o role; este olha também as
// permissões do cargo, tornando concedível pela tela de Cargos uma permissão
// como `portal:read`.
func (s *Server) permGuard(resource, action string, next http.HandlerFunc) http.HandlerFunc {
	return s.authGuard(func(w http.ResponseWriter, r *http.Request) {
		u, err := s.userByID(r.Context(), userIDFrom(r))
		if err != nil {
			writeErr(w, err)
			return
		}
		if u != nil && (u.Role == RoleAdmin || u.Role == RoleTeacher) {
			next(w, r)
			return
		}
		if u != nil && u.Role == RoleCustom && u.CustomRoleID != nil {
			if cr, err := s.getCustomRole(r.Context(), *u.CustomRoleID); err == nil && cr != nil {
				for _, a := range cr.Permissions[resource] {
					if a == action {
						next(w, r)
						return
					}
				}
			}
		}
		writeErr(w, appErr(http.StatusForbidden, "FORBIDDEN", "Acesso restrito"))
	})
}

// portalRead: leitura do portal — admin/professor ou cargo com `portal:read`.
func (s *Server) portalRead(next http.HandlerFunc) http.HandlerFunc {
	return s.permGuard("portal", "read", next)
}
