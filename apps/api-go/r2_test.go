package main

import (
	"context"
	"os"
	"strings"
	"testing"
)

// TestR2UploadIntegration valida a assinatura SigV4 do cliente R2 contra o serviço
// real. Pula quando as credenciais não estão no ambiente (CI e dev sem creds).
func TestR2UploadIntegration(t *testing.T) {
	cfg := Config{
		R2AccountID: os.Getenv("CF_ACCOUNT_ID"),
		R2AccessKey: os.Getenv("CF_R2_ACCESS_KEY"),
		R2SecretKey: os.Getenv("CF_R2_SECRET_KEY"),
		R2Bucket:    os.Getenv("CF_R2_BUCKET_NAME"),
		R2PublicURL: os.Getenv("CF_R2_PUBLIC_URL"),
	}
	r := newR2(cfg)
	if r == nil {
		t.Skip("credenciais R2 ausentes; pulando teste de integração")
	}
	url, err := r.Upload(context.Background(), "avatars/_test/sigv4check.png", "image/png", []byte("sigv4 check"))
	if err != nil {
		t.Fatalf("upload R2 falhou (SigV4?): %v", err)
	}
	if !strings.HasSuffix(url, "/avatars/_test/sigv4check.png") {
		t.Errorf("url pública inesperada: %s", url)
	}
}
