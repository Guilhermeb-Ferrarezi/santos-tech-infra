package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// R2 é um cliente mínimo de Cloudflare R2 (S3-compatível) com assinatura AWS SigV4,
// sem SDK externo — cobre o que precisamos (PUT de objetos para uploads).
type R2 struct {
	accountID string
	accessKey string
	secretKey string
	bucket    string
	publicURL string
	http      *http.Client
}

// newR2 devolve um cliente R2, ou nil se a config estiver incompleta (feature off).
func newR2(cfg Config) *R2 {
	if cfg.R2AccountID == "" || cfg.R2AccessKey == "" || cfg.R2SecretKey == "" || cfg.R2Bucket == "" {
		return nil
	}
	return &R2{
		accountID: cfg.R2AccountID,
		accessKey: cfg.R2AccessKey,
		secretKey: cfg.R2SecretKey,
		bucket:    cfg.R2Bucket,
		publicURL: cfg.R2PublicURL,
		http:      &http.Client{Timeout: 30 * time.Second},
	}
}

const (
	r2Region  = "auto"
	r2Service = "s3"
)

func r2SHA256(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func r2HMAC(key []byte, msg string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(msg))
	return m.Sum(nil)
}

// Upload faz PUT do objeto em key e devolve a URL pública ({publicURL}/{key}).
// key deve usar apenas caracteres seguros de caminho (ex: "avatars/12/abcd.png").
func (r *R2) Upload(ctx context.Context, key, contentType string, body []byte) (string, error) {
	host := r.accountID + ".r2.cloudflarestorage.com"
	canonURI := "/" + r.bucket + "/" + key

	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")
	payloadHash := r2SHA256(body)

	// Cabeçalhos assinados, em ordem alfabética.
	canonicalHeaders := "content-type:" + contentType + "\n" +
		"host:" + host + "\n" +
		"x-amz-content-sha256:" + payloadHash + "\n" +
		"x-amz-date:" + amzDate + "\n"
	signedHeaders := "content-type;host;x-amz-content-sha256;x-amz-date"

	canonicalRequest := strings.Join([]string{
		http.MethodPut, canonURI, "", canonicalHeaders, signedHeaders, payloadHash,
	}, "\n")

	scope := dateStamp + "/" + r2Region + "/" + r2Service + "/aws4_request"
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256", amzDate, scope, r2SHA256([]byte(canonicalRequest)),
	}, "\n")

	kDate := r2HMAC([]byte("AWS4"+r.secretKey), dateStamp)
	kRegion := r2HMAC(kDate, r2Region)
	kService := r2HMAC(kRegion, r2Service)
	kSigning := r2HMAC(kService, "aws4_request")
	signature := hex.EncodeToString(r2HMAC(kSigning, stringToSign))

	auth := fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		r.accessKey, scope, signedHeaders, signature)

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, "https://"+host+canonURI, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", auth)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	req.Header.Set("X-Amz-Date", amzDate)

	resp, err := r.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return "", fmt.Errorf("r2 put %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return strings.TrimRight(r.publicURL, "/") + "/" + key, nil
}
