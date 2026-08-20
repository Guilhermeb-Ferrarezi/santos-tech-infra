package main

import "net/http"

// accessRank ordena os níveis de acesso a uma pasta: "" (nenhum) < read < write.
func accessRank(level string) int {
	switch level {
	case "write":
		return 2
	case "read":
		return 1
	default:
		return 0
	}
}

// folderAccessGuard libera a rota se o usuário logado tiver acesso >= minAccess
// ("read"|"write") na pasta {id} do path. Diferente de permGuard/portalRead
// (que checam um resource FIXO de cargo, tipo "portal_materiais"), aqui o
// acesso é resolvido POR REGISTRO: admin sempre libera (write implícito);
// os demais dependem da união de ACL por cargo (fixo ou personalizado) e por
// usuário individual — ver effectiveDriveFolderAccess em drive_data.go.
func (s *Server) folderAccessGuard(minAccess string, next http.HandlerFunc) http.HandlerFunc {
	return s.authGuard(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if !uuidRe.MatchString(id) {
			writeErr(w, appErr(http.StatusNotFound, "NOT_FOUND", "pasta não encontrada"))
			return
		}
		u, err := s.cachedUserByID(r.Context(), userIDFrom(r))
		if err != nil {
			writeErr(w, err)
			return
		}
		if u == nil {
			writeErr(w, appErr(http.StatusForbidden, "FORBIDDEN", "Acesso restrito"))
			return
		}
		if u.Role == RoleAdmin {
			next(w, r)
			return
		}
		access, err := s.effectiveDriveFolderAccess(r.Context(), id, u.ID, u.Role, u.CustomRoleID)
		if err != nil {
			writeErr(w, err)
			return
		}
		if accessRank(access) < accessRank(minAccess) {
			writeErr(w, appErr(http.StatusForbidden, "FORBIDDEN", "Acesso restrito"))
			return
		}
		next(w, r)
	})
}
