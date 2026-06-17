package main

import "testing"

func TestValidRef(t *testing.T) {
	ok := []string{"main", "master", "feat/x", "release-1.2.3", "a_b.c"}
	for _, s := range ok {
		if !validRef(s) {
			t.Errorf("validRef(%q) deveria ser true", s)
		}
	}
	bad := []string{"", "-rf", "--upload-pack=evil", "foo bar", "a;b", "$(x)", "a\nb"}
	for _, s := range bad {
		if validRef(s) {
			t.Errorf("validRef(%q) deveria ser false", s)
		}
	}
}
