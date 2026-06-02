package main

import (
	"regexp"
	"testing"
)

var uuidV4 = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestNewUUIDFormat(t *testing.T) {
	id := newUUID()
	if !uuidV4.MatchString(id) {
		t.Fatalf("UUID v4 inválido: %q", id)
	}
}

func TestNewUUIDUnique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		id := newUUID()
		if seen[id] {
			t.Fatalf("UUID repetido: %q", id)
		}
		seen[id] = true
	}
}
