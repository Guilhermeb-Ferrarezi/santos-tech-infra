package main

import (
	"context"
	"fmt"
	"regexp"
	"slices"
)

// Permissões por recurso/ação no formato de custom_roles.permissions:
// {"dispositivos": ["ver", "executar"]}. A permissão EFETIVA de um usuário é a
// união das individuais (users.permissions) com as do cargo (só role 4 com
// custom_role_id). Admin passa sempre, sem consultar nada.

var permNameRe = regexp.MustCompile(`^[a-z_]{1,40}$`)

const (
	maxPermResources = 60
	maxPermActions   = 10
)

func validatePermissions(p map[string][]string) error {
	if len(p) > maxPermResources {
		return fmt.Errorf("no máximo %d recursos", maxPermResources)
	}
	for res, acts := range p {
		if !permNameRe.MatchString(res) {
			return fmt.Errorf("recurso inválido: %q", res)
		}
		if len(acts) > maxPermActions {
			return fmt.Errorf("recurso %q: no máximo %d ações", res, maxPermActions)
		}
		for _, a := range acts {
			if !permNameRe.MatchString(a) {
				return fmt.Errorf("ação inválida em %q: %q", res, a)
			}
		}
	}
	return nil
}

func mergePerms(a, b map[string][]string) map[string][]string {
	out := map[string][]string{}
	for _, m := range []map[string][]string{a, b} {
		for res, acts := range m {
			for _, act := range acts {
				if !slices.Contains(out[res], act) {
					out[res] = append(out[res], act)
				}
			}
		}
	}
	return out
}

// hasPerm é a regra única de autorização por recurso/ação.
func hasPerm(u *User, rolePerms map[string][]string, resource, action string, allowTeacher bool) bool {
	if u == nil {
		return false
	}
	if u.Role == RoleAdmin {
		return true
	}
	if allowTeacher && u.Role == RoleTeacher {
		return true
	}
	return slices.Contains(u.Permissions[resource], action) || slices.Contains(rolePerms[resource], action)
}

// rolePermsOf carrega as permissões do cargo (cache Redis de 2min). Falha de
// leitura vira "sem permissões de cargo" — nega, nunca libera.
func (s *Server) rolePermsOf(ctx context.Context, u *User) map[string][]string {
	if u == nil || u.Role != RoleCustom || u.CustomRoleID == nil {
		return nil
	}
	cr, err := s.cachedCustomRole(ctx, *u.CustomRoleID)
	if err != nil || cr == nil {
		return nil
	}
	return cr.Permissions
}

// effectivePerms: o que o usuário pode, pra exibir (/auth/me) e checar em
// massa. nil para admin (convenção antiga: admin = tudo).
func (s *Server) effectivePerms(ctx context.Context, u *User) map[string][]string {
	if u == nil || u.Role == RoleAdmin {
		return nil
	}
	return mergePerms(u.Permissions, s.rolePermsOf(ctx, u))
}
