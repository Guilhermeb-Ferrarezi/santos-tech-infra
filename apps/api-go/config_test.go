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
