package main

import "testing"

func TestParseUserAgent(t *testing.T) {
	cases := []struct {
		name    string
		ua      string
		device  string
		browser string
		os      string
	}{
		{
			name:   "iphone safari",
			ua:     "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1",
			device: "mobile", browser: "Safari", os: "iOS",
		},
		{
			name:   "android chrome",
			ua:     "Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0.0.0 Mobile Safari/537.36",
			device: "mobile", browser: "Chrome", os: "Android",
		},
		{
			name:   "windows chrome desktop",
			ua:     "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0.0.0 Safari/537.36",
			device: "desktop", browser: "Chrome", os: "Windows",
		},
		{
			name:   "windows edge desktop",
			ua:     "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0.0.0 Safari/537.36 Edg/125.0.0.0",
			device: "desktop", browser: "Edge", os: "Windows",
		},
		{
			name:   "mac firefox desktop",
			ua:     "Mozilla/5.0 (Macintosh; Intel Mac OS X 10.15; rv:126.0) Gecko/20100101 Firefox/126.0",
			device: "desktop", browser: "Firefox", os: "macOS",
		},
		{
			name:   "ipad tablet",
			ua:     "Mozilla/5.0 (iPad; CPU OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1",
			device: "tablet", browser: "Safari", os: "iOS",
		},
		{
			name:   "vazio",
			ua:     "",
			device: "desktop", browser: "", os: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseUserAgent(tc.ua)
			if got.Device != tc.device {
				t.Errorf("device: got %q, want %q", got.Device, tc.device)
			}
			if got.Browser != tc.browser {
				t.Errorf("browser: got %q, want %q", got.Browser, tc.browser)
			}
			if got.OS != tc.os {
				t.Errorf("os: got %q, want %q", got.OS, tc.os)
			}
		})
	}
}

func TestReferrerDomain(t *testing.T) {
	cases := map[string]string{
		"https://www.google.com/search?q=tela": "google.com",
		"https://instagram.com/":               "instagram.com",
		"http://t.co/abc123":                   "t.co",
		"":                                     "",
		"direto":                               "direto",
	}
	for in, want := range cases {
		if got := referrerDomain(in); got != want {
			t.Errorf("referrerDomain(%q) = %q, want %q", in, got, want)
		}
	}
}
