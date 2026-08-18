package main

import "testing"

func TestSocialPostIsVideo(t *testing.T) {
	cases := []struct {
		formato       string
		wantVideo     bool
		wantSupported bool
	}{
		{"reel", true, true},
		{"short", true, true},
		{"video_longo", true, true},
		{"estatico", false, true},
		{"thumbnail", false, true},
		{"card_link", false, true},
		{"carrossel", false, false},
		{"story", false, false},
		{"algo_inexistente", false, false},
	}
	for _, c := range cases {
		gotVideo, gotSupported := socialPostIsVideo(c.formato)
		if gotVideo != c.wantVideo || gotSupported != c.wantSupported {
			t.Errorf("socialPostIsVideo(%q) = (%v, %v), want (%v, %v)",
				c.formato, gotVideo, gotSupported, c.wantVideo, c.wantSupported)
		}
	}
}

func TestSocialPublishAdaptersEmptyWhenDisabled(t *testing.T) {
	s := &Server{instagram: newInstagramClient(Config{}), facebook: newFacebookClient(Config{})}
	adapters := s.socialPublishAdapters()
	if len(adapters) != 0 {
		t.Errorf("expected no adapters with empty config, got %v", adapters)
	}
}

func TestSocialPublishAdaptersEnabled(t *testing.T) {
	cfg := Config{
		InstagramAccessToken: "tok", InstagramUserID: "123",
		FacebookAccessToken: "tok", FacebookPageID: "456",
	}
	s := &Server{instagram: newInstagramClient(cfg), facebook: newFacebookClient(cfg)}
	adapters := s.socialPublishAdapters()
	if adapters["instagram"] == nil {
		t.Error("expected instagram adapter to be registered")
	}
	if adapters["facebook"] == nil {
		t.Error("expected facebook adapter to be registered")
	}
}
