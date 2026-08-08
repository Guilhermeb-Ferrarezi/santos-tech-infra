package main

import "testing"

func TestHeatmapReferenceWidth(t *testing.T) {
	if got := heatmapReferenceWidth("mobile"); got != heatmapRefWidthMobile {
		t.Errorf("mobile: got %d, want %d", got, heatmapRefWidthMobile)
	}
	if got := heatmapReferenceWidth("desktop"); got != heatmapRefWidthDesktop {
		t.Errorf("desktop: got %d, want %d", got, heatmapRefWidthDesktop)
	}
	// entrada desconhecida cai pro desktop — mesma convenção de fallback
	// silencioso do resto do pacote (nunca panica em input externo).
	if got := heatmapReferenceWidth("bogus"); got != heatmapRefWidthDesktop {
		t.Errorf("bogus: got %d, want fallback %d", got, heatmapRefWidthDesktop)
	}
}

func TestValidateHeatmapBatch(t *testing.T) {
	depth := 0.5
	badDepth := 1.5

	cases := []struct {
		name    string
		in      BlogHeatmapBatch
		wantErr bool
	}{
		{
			name: "válido com clique",
			in: BlogHeatmapBatch{
				PostSlug: "post-x", Viewport: "desktop", SessionID: "s1",
				Clicks: []HeatmapClickInput{{XPct: 0.5, YPx: 100}},
			},
		},
		{
			name: "válido só com scroll",
			in: BlogHeatmapBatch{
				PostSlug: "post-x", Viewport: "mobile", SessionID: "s1",
				MaxScrollDepthPct: &depth,
			},
		},
		{
			name:    "lote vazio",
			in:      BlogHeatmapBatch{PostSlug: "post-x", Viewport: "desktop", SessionID: "s1"},
			wantErr: true,
		},
		{
			name:    "postSlug vazio",
			in:      BlogHeatmapBatch{Viewport: "desktop", SessionID: "s1", Clicks: []HeatmapClickInput{{XPct: 0.1, YPx: 1}}},
			wantErr: true,
		},
		{
			name:    "viewport inválido",
			in:      BlogHeatmapBatch{PostSlug: "post-x", Viewport: "tablet", SessionID: "s1", Clicks: []HeatmapClickInput{{XPct: 0.1, YPx: 1}}},
			wantErr: true,
		},
		{
			name:    "sessionId vazio",
			in:      BlogHeatmapBatch{PostSlug: "post-x", Viewport: "desktop", Clicks: []HeatmapClickInput{{XPct: 0.1, YPx: 1}}},
			wantErr: true,
		},
		{
			name: "xPct fora do intervalo",
			in: BlogHeatmapBatch{
				PostSlug: "post-x", Viewport: "desktop", SessionID: "s1",
				Clicks: []HeatmapClickInput{{XPct: 1.2, YPx: 1}},
			},
			wantErr: true,
		},
		{
			name: "yPx negativo",
			in: BlogHeatmapBatch{
				PostSlug: "post-x", Viewport: "desktop", SessionID: "s1",
				Clicks: []HeatmapClickInput{{XPct: 0.1, YPx: -1}},
			},
			wantErr: true,
		},
		{
			name: "maxScrollDepthPct fora do intervalo",
			in: BlogHeatmapBatch{
				PostSlug: "post-x", Viewport: "desktop", SessionID: "s1",
				MaxScrollDepthPct: &badDepth,
			},
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateHeatmapBatch(&tc.in)
			if (err != nil) != tc.wantErr {
				t.Errorf("got err=%v, wantErr=%v", err, tc.wantErr)
			}
		})
	}
}

func TestValidateHeatmapBatchClicksTooLarge(t *testing.T) {
	clicks := make([]HeatmapClickInput, heatmapMaxClicksPerBatch+1)
	for i := range clicks {
		clicks[i] = HeatmapClickInput{XPct: 0.1, YPx: 1}
	}
	in := BlogHeatmapBatch{PostSlug: "post-x", Viewport: "desktop", SessionID: "s1", Clicks: clicks}
	if err := validateHeatmapBatch(&in); err == nil {
		t.Error("esperava erro com lote acima de heatmapMaxClicksPerBatch, veio nil")
	}
}
