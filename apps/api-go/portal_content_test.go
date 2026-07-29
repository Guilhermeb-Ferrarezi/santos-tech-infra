package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseModuleMeta(t *testing.T) {
	desc, id, name := parseModuleMeta("[[module:7|Módulo Um]]\nCorpo da descrição")
	if desc != "Corpo da descrição" {
		t.Fatalf("desc=%q", desc)
	}
	if id == nil || *id != "7" {
		t.Fatalf("id=%v", id)
	}
	if name == nil || *name != "Módulo Um" {
		t.Fatalf("name=%v", name)
	}

	// sem metadado → passa intacto
	desc2, id2, name2 := parseModuleMeta("Só descrição")
	if desc2 != "Só descrição" || id2 != nil || name2 != nil {
		t.Fatalf("sem meta: desc=%q id=%v name=%v", desc2, id2, name2)
	}

	// nome vazio → name nil mas id presente
	_, id3, name3 := parseModuleMeta("[[module:3|]]\nx")
	if id3 == nil || *id3 != "3" || name3 != nil {
		t.Fatalf("nome vazio: id=%v name=%v", id3, name3)
	}
}

func TestComposeModuleMeta(t *testing.T) {
	mid := int64(7)
	name := "Módulo Um"
	got := composeModuleMeta("Corpo", &mid, &name)
	want := "[[module:7|Módulo Um]]\nCorpo"
	if got != want {
		t.Fatalf("got=%q want=%q", got, want)
	}

	// round-trip
	desc, id, n := parseModuleMeta(got)
	if desc != "Corpo" || id == nil || *id != "7" || n == nil || *n != "Módulo Um" {
		t.Fatalf("round-trip falhou: desc=%q id=%v name=%v", desc, id, n)
	}

	// sem módulo → description intacta
	if composeModuleMeta("Corpo", nil, nil) != "Corpo" {
		t.Fatal("sem módulo deveria devolver a description crua")
	}

	// description vazia → só o prefixo
	if composeModuleMeta("", &mid, &name) != "[[module:7|Módulo Um]]" {
		t.Fatal("description vazia deveria virar só o prefixo")
	}
}

func TestPortalExerciseValidation(t *testing.T) {
	// múltipla escolha sem questões → erro
	mult := 1
	in := portalExerciseInput{Title: "Ex", Description: "desc", Type: &mult}
	if err := in.validateCreate(); err == nil {
		t.Fatal("múltipla sem questões deveria falhar")
	}

	// múltipla válida
	ok := portalExerciseInput{
		Title: "Ex", Description: "desc", Type: &mult,
		Questions: []portalQuestionInput{{
			Statement: "1+1?",
			Options: []portalOptionInput{
				{Text: "2", IsCorrect: true},
				{Text: "3", IsCorrect: false},
			},
		}},
	}
	if err := ok.validateCreate(); err != nil {
		t.Fatalf("múltipla válida não deveria falhar: %v", err)
	}

	// duas corretas → erro
	twoCorrect := ok
	twoCorrect.Questions = []portalQuestionInput{{
		Statement: "q", Options: []portalOptionInput{
			{Text: "a", IsCorrect: true}, {Text: "b", IsCorrect: true},
		},
	}}
	if err := twoCorrect.validateCreate(); err == nil {
		t.Fatal("duas opções corretas deveria falhar")
	}

	// type fora do range
	bad := 9
	if err := (&portalExerciseInput{Title: "Ex", Description: "desc", Type: &bad}).validateCreate(); err == nil {
		t.Fatal("type=9 deveria falhar")
	}
}

func TestPortalExercisePhaseIDValidation(t *testing.T) {
	// realocação (phaseId no PATCH): <= 0 deveria falhar
	zero := int64(0)
	if err := (&portalExerciseInput{PhaseID: &zero}).validateUpdate(); err == nil {
		t.Fatal("phaseId=0 deveria falhar")
	}
	neg := int64(-3)
	if err := (&portalExerciseInput{PhaseID: &neg}).validateUpdate(); err == nil {
		t.Fatal("phaseId negativo deveria falhar")
	}
	// phaseId válido não deveria falhar (patch parcial, sem os demais campos)
	valid := int64(9)
	if err := (&portalExerciseInput{PhaseID: &valid}).validateUpdate(); err != nil {
		t.Fatalf("phaseId válido não deveria falhar: %v", err)
	}
	// omitido (nil) não deveria falhar
	if err := (&portalExerciseInput{}).validateUpdate(); err != nil {
		t.Fatalf("phaseId omitido não deveria falhar: %v", err)
	}
}

func TestPortalContentRoutesAuthBeforeDB(t *testing.T) {
	s := testServer(Config{})
	for _, tc := range []struct {
		name string
		h    http.HandlerFunc
		m    string
		path string
	}{
		{"list exercises", s.staffGuard(s.handlePortalListExercises), "GET", "/portal/phases/1/exercises"},
		{"create exercise", s.adminGuard(s.handlePortalCreateExercise), "POST", "/portal/phases/1/exercises"},
		{"list containers", s.staffGuard(s.handlePortalListContainers), "GET", "/portal/phases/1/containers"},
		{"create container", s.adminGuard(s.handlePortalCreateContainer), "POST", "/portal/containers"},
		{"list materials", s.staffGuard(s.handlePortalListMaterials), "GET", "/portal/courses/1/materials"},
		{"list videos", s.staffGuard(s.handlePortalListVideos), "GET", "/portal/videos"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.m, tc.path, nil)
			w := httptest.NewRecorder()
			tc.h(w, req)
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("code=%d want 401", w.Code)
			}
		})
	}
}

func TestPortalContentValidationBeforeDB(t *testing.T) {
	s := testServer(Config{})

	// exercício sem título → 400
	r := httptest.NewRequest("POST", "/portal/phases/1/exercises", strings.NewReader(`{"title":"x","description":"y"}`))
	r.SetPathValue("phaseId", "1")
	w := httptest.NewRecorder()
	s.handlePortalCreateExercise(w, reqAs(r, 1))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("título curto code=%d", w.Code)
	}

	// container sem exerciseIds → 400
	r2 := httptest.NewRequest("POST", "/portal/containers", strings.NewReader(`{"name":"C","phaseId":1,"exerciseIds":[]}`))
	w2 := httptest.NewRecorder()
	s.handlePortalCreateContainer(w2, reqAs(r2, 1))
	if w2.Code != http.StatusBadRequest {
		t.Fatalf("container vazio code=%d", w2.Code)
	}

	// material id inválido → 400
	r3 := httptest.NewRequest("GET", "/portal/materials/x", nil)
	r3.SetPathValue("materialId", "x")
	w3 := httptest.NewRecorder()
	s.handlePortalGetMaterial(w3, reqAs(r3, 1))
	if w3.Code != http.StatusBadRequest {
		t.Fatalf("material id inválido code=%d", w3.Code)
	}

	// vídeo sem url → 400
	r4 := httptest.NewRequest("POST", "/portal/videos", strings.NewReader(`{"title":"Aula"}`))
	w4 := httptest.NewRecorder()
	s.handlePortalCreateVideo(w4, reqAs(r4, 1))
	if w4.Code != http.StatusBadRequest {
		t.Fatalf("vídeo sem url code=%d", w4.Code)
	}
}

func TestPortalMergeModuleDesc(t *testing.T) {
	curID, curName := "7", "Mod A"

	// PATCH troca só a description, sem moduleId → módulo é preservado
	got := portalMergeModuleDesc("desc antiga", &curID, &curName, portalMaterialModule{desc: "desc nova"})
	if got != "[[module:7|Mod A]]\ndesc nova" {
		t.Fatalf("preservar módulo: got=%q", got)
	}

	// troca o módulo explicitamente
	newID, newName := int64(9), "Mod B"
	got2 := portalMergeModuleDesc("d", &curID, &curName, portalMaterialModule{moduleID: &newID, moduleName: &newName})
	if got2 != "[[module:9|Mod B]]\nd" {
		t.Fatalf("trocar módulo: got=%q", got2)
	}

	// sem módulo atual nem novo → description pura
	if got3 := portalMergeModuleDesc("d", nil, nil, portalMaterialModule{desc: "nova"}); got3 != "nova" {
		t.Fatalf("sem módulo: got=%q", got3)
	}
}

func TestPortalExerciseUpdateMultiplaNeedsQuestions(t *testing.T) {
	s := testServer(Config{})
	r := httptest.NewRequest("PATCH", "/portal/exercises/1", strings.NewReader(`{"type":1}`))
	r.SetPathValue("exerciseId", "1")
	w := httptest.NewRecorder()
	s.handlePortalUpdateExercise(w, reqAs(r, 1))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("type=1 sem questões no update code=%d", w.Code)
	}
}
