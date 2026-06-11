package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Guards: sem token, leitura (staff) e escrita (admin) das rotas de Fase 4
// respondem 401 antes de tocar o banco.
func TestPortalRewardsRoutesRequireAuthBeforeDB(t *testing.T) {
	s := testServer(Config{})
	for _, tc := range []struct {
		name   string
		h      http.HandlerFunc
		method string
		path   string
	}{
		{"list badges", s.staffGuard(s.handlePortalListBadges), "GET", "/portal/badges"},
		{"create badge", s.adminGuard(s.handlePortalCreateBadge), "POST", "/portal/badges"},
		{"list holders", s.staffGuard(s.handlePortalListHolders), "GET", "/portal/badges/holders"},
		{"list goals", s.staffGuard(s.handlePortalListGoals), "GET", "/portal/goals"},
		{"create goal", s.adminGuard(s.handlePortalCreateGoal), "POST", "/portal/goals"},
		{"list rewards", s.staffGuard(s.handlePortalListGoalRewards), "GET", "/portal/goals/rewards"},
		{"list goal students", s.staffGuard(s.handlePortalListGoalStudents), "GET", "/portal/goals/students"},
		{"templates", s.staffGuard(s.handlePortalListTemplates), "GET", "/portal/notifications/templates"},
		{"dispatches", s.staffGuard(s.handlePortalListDispatches), "GET", "/portal/notifications/dispatches"},
		{"activity logs", s.staffGuard(s.handlePortalActivityLogs), "GET", "/portal/activity-logs"},
		{"my notifications", s.authGuard(s.handlePortalMyNotifications), "GET", "/portal/notifications/me"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			w := httptest.NewRecorder()
			tc.h(w, req)
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("code=%d want %d", w.Code, http.StatusUnauthorized)
			}
		})
	}
}

// IDs malformados no path → 400 antes de tocar o banco.
func TestPortalRewardsBadIDsBeforeDB(t *testing.T) {
	s := testServer(Config{})
	cases := []struct {
		name string
		fn   http.HandlerFunc
		key  string
		val  string
	}{
		{"update badge", s.handlePortalUpdateBadge, "badgeId", "x"},
		{"delete badge", s.handlePortalDeleteBadge, "badgeId", "0"},
		{"get goal", s.handlePortalGetGoal, "goalId", "-1"},
		{"update reward", s.handlePortalUpdateGoalReward, "rewardId", "abc"},
		{"claim", s.handlePortalClaimGoalReward, "goalStudentId", "0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("PATCH", "/portal/x", strings.NewReader("{}"))
			r.SetPathValue(tc.key, tc.val)
			w := httptest.NewRecorder()
			tc.fn(w, reqAs(r, 1))
			if w.Code != http.StatusBadRequest {
				t.Fatalf("code=%d want %d", w.Code, http.StatusBadRequest)
			}
		})
	}
}

// Payloads inválidos → 400 antes de qualquer acesso a banco/gateway.
func TestPortalRewardsValidationBeforeDB(t *testing.T) {
	s := testServer(Config{})
	cases := []struct {
		name string
		fn   http.HandlerFunc
		body string
	}{
		{"badge sem nome", s.handlePortalCreateBadge, `{"description":"abc","iconUrl":"http://x/y.png"}`},
		{"badge sem ícone", s.handlePortalCreateBadge, `{"name":"Top","description":"abc"}`},
		{"goal sem nome", s.handlePortalCreateGoal, `{"description":"abc"}`},
		{"goal tipo inválido", s.handlePortalCreateGoal, `{"name":"Meta","type":9}`},
		{"reward sem goalId", s.handlePortalCreateGoalReward, `{"badgeId":1,"courseId":1}`},
		{"assign sem userId", s.handlePortalAssignBadge, `{"badgeId":1}`},
		{"goal student sem ids", s.handlePortalCreateGoalStudent, `{}`},
		{"template sem nome", s.handlePortalCreateTemplate, `{"tituloTemplate":"t","mensagemTemplate":"m","ativo":true}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/portal/x", strings.NewReader(tc.body))
			w := httptest.NewRecorder()
			tc.fn(w, reqAs(r, 1))
			if w.Code != http.StatusBadRequest {
				t.Fatalf("code=%d want %d body=%s", w.Code, http.StatusBadRequest, w.Body.String())
			}
		})
	}
}

// Sem gateway configurado, callGateway falha com 502 (não tenta rede).
func TestCallGatewayUnconfigured(t *testing.T) {
	s := testServer(Config{})
	if s.gatewayConfigured() {
		t.Fatal("gateway não deveria estar configurado com Config{}")
	}
	_, err := s.callGateway(context.Background(), http.MethodGet, "/x", nil)
	ae, ok := err.(*AppError)
	if !ok {
		t.Fatalf("erro=%T want *AppError", err)
	}
	if ae.Status != http.StatusBadGateway {
		t.Fatalf("status=%d want %d", ae.Status, http.StatusBadGateway)
	}
}

func TestPortalTemplateRequirements(t *testing.T) {
	cases := []struct {
		title, msg           string
		wantCurso, wantTurma bool
	}{
		{"Olá", "sem placeholder", false, false},
		{"Bem-vindo ao {{ curso.name }}", "corpo", true, false},
		{"Aviso", "Sua turma {{turma.nome}} começou", false, true},
		{"{{ course.title }}", "{{ class.code }}", true, true},
	}
	for _, c := range cases {
		got := portalTemplateRequirements(c.title, c.msg)
		if got.RequiresCurso != c.wantCurso || got.RequiresTurma != c.wantTurma {
			t.Errorf("%q/%q → curso=%v turma=%v want curso=%v turma=%v", c.title, c.msg, got.RequiresCurso, got.RequiresTurma, c.wantCurso, c.wantTurma)
		}
	}
}

func TestParseNumericText(t *testing.T) {
	if parseNumericText(nil) != nil {
		t.Error("nil → nil")
	}
	empty := ""
	if parseNumericText(&empty) != nil {
		t.Error(`"" → nil`)
	}
	v := "12.5"
	if got := parseNumericText(&v); got == nil || *got != 12.5 {
		t.Errorf("12.5 → %v", got)
	}
	bad := "xyz"
	if parseNumericText(&bad) != nil {
		t.Error("inválido → nil")
	}
}

func TestPortalRoleLabel(t *testing.T) {
	for role, want := range map[int16]string{RoleStudent: "aluno", RoleTeacher: "professor", RoleAdmin: "admin", RoleCustom: ""} {
		if got := portalRoleLabel(role); got != want {
			t.Errorf("role=%d → %q want %q", role, got, want)
		}
	}
}
