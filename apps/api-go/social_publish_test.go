package main

import (
	"encoding/json"
	"testing"
)

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

func TestCaptionWithHashtags(t *testing.T) {
	cases := []struct {
		caption  string
		hashtags []string
		want     string
	}{
		{"Legenda normal", nil, "Legenda normal"},
		{"Legenda normal", []string{"#santostech", "#educacao"}, "Legenda normal\n\n#santostech #educacao"},
		{"", []string{"#santostech"}, "#santostech"},
		{"", nil, ""},
	}
	for _, c := range cases {
		got := captionWithHashtags(c.caption, c.hashtags)
		if got != c.want {
			t.Errorf("captionWithHashtags(%q, %v) = %q, want %q", c.caption, c.hashtags, got, c.want)
		}
	}
}

func TestResolvePublishTargets(t *testing.T) {
	destino := []string{"instagram", "facebook", "tiktok"}

	got, err := resolvePublishTargets(destino, nil)
	if err != nil || len(got) != 3 {
		t.Errorf("sem requested deveria devolver toda plataformasDestino, got=%v err=%v", got, err)
	}

	got, err = resolvePublishTargets(destino, []string{"instagram"})
	if err != nil || len(got) != 1 || got[0] != "instagram" {
		t.Errorf("subconjunto válido deveria passar, got=%v err=%v", got, err)
	}

	_, err = resolvePublishTargets(destino, []string{"linkedin"})
	if err == nil {
		t.Error("plataforma fora de plataformasDestino deveria falhar")
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

func TestCarouselItemIsVideo(t *testing.T) {
	cases := []struct {
		fileName string
		want     bool
	}{
		{"clipe.mp4", true},
		{"CLIPE.MOV", true},
		{"foto.jpg", false},
		{"foto.PNG", false},
		{"sem-extensao", false},
		{"", false},
	}
	for _, c := range cases {
		if got := carouselItemIsVideo(c.fileName); got != c.want {
			t.Errorf("carouselItemIsVideo(%q) = %v, want %v", c.fileName, got, c.want)
		}
	}
}

func strPtr(s string) *string { return &s }

func TestParseCarouselItems(t *testing.T) {
	extraJSON := func(items ...carouselItemRef) json.RawMessage {
		b, _ := json.Marshal(items)
		return b
	}

	t.Run("só item 1, sem extras: erro (menos de 2)", func(t *testing.T) {
		post := &SocialPost{ID: "p1", DriveFolderID: strPtr("f1"), DriveFileID: "file1", DriveFileName: "a.jpg"}
		if _, err := parseCarouselItems(post); err == nil {
			t.Error("esperava erro com só 1 item")
		}
	})

	t.Run("item 1 + 1 extra: ok, 2 itens", func(t *testing.T) {
		post := &SocialPost{
			ID: "p1", DriveFolderID: strPtr("f1"), DriveFileID: "file1", DriveFileName: "a.jpg",
			CarouselItems: extraJSON(carouselItemRef{FolderID: "f1", FileID: "file2", FileName: "b.mp4"}),
		}
		items, err := parseCarouselItems(post)
		if err != nil {
			t.Fatalf("não esperava erro: %v", err)
		}
		if len(items) != 2 {
			t.Fatalf("esperava 2 itens, got %d", len(items))
		}
		if items[0].FileID != "file1" || items[1].FileID != "file2" {
			t.Errorf("ordem inesperada: %+v", items)
		}
	})

	t.Run("item 1 + 9 extras: ok, 10 itens (teto da Graph API)", func(t *testing.T) {
		extras := make([]carouselItemRef, 9)
		for i := range extras {
			extras[i] = carouselItemRef{FolderID: "f1", FileID: "fileX", FileName: "x.jpg"}
		}
		post := &SocialPost{
			ID: "p1", DriveFolderID: strPtr("f1"), DriveFileID: "file1", DriveFileName: "a.jpg",
			CarouselItems: extraJSON(extras...),
		}
		items, err := parseCarouselItems(post)
		if err != nil {
			t.Fatalf("não esperava erro: %v", err)
		}
		if len(items) != 10 {
			t.Fatalf("esperava 10 itens, got %d", len(items))
		}
	})

	t.Run("item 1 + 10 extras: erro (excede o teto de 10)", func(t *testing.T) {
		extras := make([]carouselItemRef, 10)
		for i := range extras {
			extras[i] = carouselItemRef{FolderID: "f1", FileID: "fileX", FileName: "x.jpg"}
		}
		post := &SocialPost{
			ID: "p1", DriveFolderID: strPtr("f1"), DriveFileID: "file1", DriveFileName: "a.jpg",
			CarouselItems: extraJSON(extras...),
		}
		if _, err := parseCarouselItems(post); err == nil {
			t.Error("esperava erro com 11 itens")
		}
	})

	t.Run("sem item 1, 2 extras: ok", func(t *testing.T) {
		post := &SocialPost{
			ID: "p1",
			CarouselItems: extraJSON(
				carouselItemRef{FolderID: "f1", FileID: "file2", FileName: "b.mp4"},
				carouselItemRef{FolderID: "f1", FileID: "file3", FileName: "c.jpg"},
			),
		}
		items, err := parseCarouselItems(post)
		if err != nil {
			t.Fatalf("não esperava erro: %v", err)
		}
		if len(items) != 2 {
			t.Fatalf("esperava 2 itens, got %d", len(items))
		}
	})

	t.Run("carousel_items corrompido: erro", func(t *testing.T) {
		post := &SocialPost{
			ID: "p1", DriveFolderID: strPtr("f1"), DriveFileID: "file1", DriveFileName: "a.jpg",
			CarouselItems: json.RawMessage(`{not valid json`),
		}
		if _, err := parseCarouselItems(post); err == nil {
			t.Error("esperava erro com JSON corrompido")
		}
	})
}
