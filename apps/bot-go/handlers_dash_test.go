package main

import "testing"

func TestValidRescheduleSource(t *testing.T) {
	cases := map[string]bool{
		"notion":  true,
		"pending": true,
		"":        false,
		"x":       false,
		"NOTION":  false,
	}
	for in, want := range cases {
		if got := validRescheduleSource(in); got != want {
			t.Errorf("validRescheduleSource(%q) = %v, quer %v", in, got, want)
		}
	}
}
