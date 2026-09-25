package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestValidatePermissions(t *testing.T) {
	ok := map[string][]string{"dispositivos": {"ver", "executar"}, "portal_cursos": {"read"}}
	if err := validatePermissions(ok); err != nil {
		t.Fatalf("válido recusado: %v", err)
	}
	ruins := []map[string][]string{
		{"Dispositivos": {"ver"}},                                      // maiúscula
		{"dispositivos": {"ver;drop"}},                                 // caractere inválido
		{"": {"ver"}},                                                  // vazio
		{"dispositivos": {strings.Repeat("a", 41)}},                    // longo demais
		{"x": {"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k"}}, // 11 ações
	}
	for i, p := range ruins {
		if err := validatePermissions(p); err == nil {
			t.Fatalf("caso %d deveria falhar: %v", i, p)
		}
	}
	muitos := map[string][]string{}
	for i := 0; i < 61; i++ {
		muitos[strings.Repeat("r", 1)+string(rune('a'+i%26))+strings.Repeat("x", i/26)] = []string{"ver"}
	}
	if err := validatePermissions(muitos); err == nil {
		t.Fatal("61 recursos deveria falhar")
	}
}

func TestMergePermsUneSemDuplicar(t *testing.T) {
	a := map[string][]string{"dispositivos": {"ver"}, "agenda": {"read"}}
	b := map[string][]string{"dispositivos": {"ver", "executar"}}
	got := mergePerms(a, b)
	want := map[string][]string{"dispositivos": {"ver", "executar"}, "agenda": {"read"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	if mergePerms(nil, nil) == nil {
		t.Fatal("merge de nils deve devolver mapa vazio, não nil")
	}
}

func TestHasPerm(t *testing.T) {
	cargo := map[string][]string{"agenda": {"read"}}
	cases := []struct {
		nome         string
		u            *User
		rolePerms    map[string][]string
		res, act     string
		allowTeacher bool
		want         bool
	}{
		{"nil nega", nil, nil, "dispositivos", "ver", false, false},
		{"admin passa sempre", &User{Role: RoleAdmin}, nil, "dispositivos", "executar", false, true},
		{"professor com allowTeacher", &User{Role: RoleTeacher}, nil, "agenda", "read", true, true},
		{"professor sem allowTeacher e sem perm", &User{Role: RoleTeacher}, nil, "agenda", "read", false, false},
		{"individual concede a aluno", &User{Role: RoleStudent, Permissions: map[string][]string{"dispositivos": {"ver"}}}, nil, "dispositivos", "ver", false, true},
		{"individual não vaza pra outra ação", &User{Role: RoleCustom, Permissions: map[string][]string{"dispositivos": {"ver"}}}, nil, "dispositivos", "executar", false, false},
		{"cargo concede", &User{Role: RoleCustom}, cargo, "agenda", "read", false, true},
		{"soma cargo + individual", &User{Role: RoleCustom, Permissions: map[string][]string{"dispositivos": {"executar"}}}, cargo, "dispositivos", "executar", false, true},
	}
	for _, c := range cases {
		if got := hasPerm(c.u, c.rolePerms, c.res, c.act, c.allowTeacher); got != c.want {
			t.Errorf("%s: got %v want %v", c.nome, got, c.want)
		}
	}
}
