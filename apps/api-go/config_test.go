package main

import (
	"os"
	"reflect"
	"testing"
)

func TestSplitCSV(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"a", []string{"a"}},
		{"a,b,c", []string{"a", "b", "c"}},
		{" a , b ,, c ", []string{"a", "b", "c"}}, // trim + ignora vazios
	}
	for _, c := range cases {
		if got := splitCSV(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("splitCSV(%q) = %v, queria %v", c.in, got, c.want)
		}
	}
}

func TestGetEnv(t *testing.T) {
	os.Unsetenv("X_AUTH_TEST")
	if getEnv("X_AUTH_TEST", "def") != "def" {
		t.Error("deveria usar o fallback")
	}
	os.Setenv("X_AUTH_TEST", "val")
	defer os.Unsetenv("X_AUTH_TEST")
	if getEnv("X_AUTH_TEST", "def") != "val" {
		t.Error("deveria usar o valor do ambiente")
	}
}

func TestGetEnvInt64(t *testing.T) {
	os.Unsetenv("X_AUTH_TEST_INT")
	if got := getEnvInt64("X_AUTH_TEST_INT", 26); got != 26 {
		t.Errorf("sem env, queria o fallback 26, veio %d", got)
	}
	os.Setenv("X_AUTH_TEST_INT", "42")
	defer os.Unsetenv("X_AUTH_TEST_INT")
	if got := getEnvInt64("X_AUTH_TEST_INT", 26); got != 42 {
		t.Errorf("com env, queria 42, veio %d", got)
	}
	os.Setenv("X_AUTH_TEST_INT", "não-é-número")
	if got := getEnvInt64("X_AUTH_TEST_INT", 26); got != 26 {
		t.Errorf("valor inválido deveria cair no fallback 26, veio %d", got)
	}
}

func TestGetEnvFloat(t *testing.T) {
	os.Unsetenv("X_AUTH_TEST_FLOAT")
	if got := getEnvFloat("X_AUTH_TEST_FLOAT", 0.75); got != 0.75 {
		t.Errorf("sem env, queria o fallback 0.75, veio %v", got)
	}
	os.Setenv("X_AUTH_TEST_FLOAT", "0.5")
	defer os.Unsetenv("X_AUTH_TEST_FLOAT")
	if got := getEnvFloat("X_AUTH_TEST_FLOAT", 0.75); got != 0.5 {
		t.Errorf("com env, queria 0.5, veio %v", got)
	}
	os.Setenv("X_AUTH_TEST_FLOAT", "não-é-número")
	if got := getEnvFloat("X_AUTH_TEST_FLOAT", 0.75); got != 0.75 {
		t.Errorf("valor inválido deveria cair no fallback 0.75, veio %v", got)
	}
}
