package main

import (
	"net/http"
	"testing"
)

func TestClientIP(t *testing.T) {
	mk := func(remote string, hdr map[string]string) *http.Request {
		r := &http.Request{RemoteAddr: remote, Header: http.Header{}}
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		return r
	}
	cases := []struct {
		name string
		req  *http.Request
		want string
	}{
		{"cloudflare", mk("10.0.0.1:1234", map[string]string{"CF-Connecting-IP": "203.0.113.7"}), "203.0.113.7"},
		{"x-forwarded-for", mk("10.0.0.1:1234", map[string]string{"X-Forwarded-For": "198.51.100.2, 10.0.0.1"}), "198.51.100.2"},
		{"remote-addr", mk("192.0.2.5:9999", nil), "192.0.2.5"},
		{"cf-tem-prioridade", mk("10.0.0.1:1", map[string]string{"CF-Connecting-IP": "1.1.1.1", "X-Forwarded-For": "2.2.2.2"}), "1.1.1.1"},
		{"remote-sem-porta", mk("192.0.2.9", nil), "192.0.2.9"},
	}
	for _, c := range cases {
		if got := clientIP(c.req); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}
