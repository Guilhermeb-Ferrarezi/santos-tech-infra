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
	"net/url"
	"strconv"
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
	return r.PublicURL(key), nil
}

// PublicURL devolve a URL pública de um objeto já enviado (ou que vai ser enviado
// via PresignPut) em key.
func (r *R2) PublicURL(key string) string {
	return strings.TrimRight(r.publicURL, "/") + "/" + key
}

// PresignPut gera uma URL PUT pré-assinada (SigV4 via query string) pra key,
// válida por `expiry`: o cliente sobe o arquivo direto pro R2 com um PUT nessa URL,
// sem os bytes passarem pelo backend — usado pra arquivos grandes demais pra
// bufferizar em memória numa request normal (ex.: vídeo). Só assina o header
// "host": o Content-Type do PUT real fica livre pro cliente escolher.
//
// ponytail: validação de tamanho aqui é só o que o cliente declara na hora de pedir
// a URL (ver handleVideoPresign) — não há enforcement server-side dos bytes
// efetivamente enviados ao R2. Upgrade se abuso virar problema: presigned POST com
// policy de content-length-range, que o R2 também suporta.
func (r *R2) PresignPut(key string, expiry time.Duration) string {
	host := r.accountID + ".r2.cloudflarestorage.com"
	canonURI := "/" + r.bucket + "/" + key

	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")
	scope := dateStamp + "/" + r2Region + "/" + r2Service + "/aws4_request"

	q := url.Values{}
	q.Set("X-Amz-Algorithm", "AWS4-HMAC-SHA256")
	q.Set("X-Amz-Credential", r.accessKey+"/"+scope)
	q.Set("X-Amz-Date", amzDate)
	q.Set("X-Amz-Expires", strconv.Itoa(int(expiry.Seconds())))
	q.Set("X-Amz-SignedHeaders", "host")
	canonicalQuery := q.Encode()

	canonicalHeaders := "host:" + host + "\n"
	signedHeaders := "host"

	canonicalRequest := strings.Join([]string{
		http.MethodPut, canonURI, canonicalQuery, canonicalHeaders, signedHeaders, "UNSIGNED-PAYLOAD",
	}, "\n")

	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256", amzDate, scope, r2SHA256([]byte(canonicalRequest)),
	}, "\n")

	kDate := r2HMAC([]byte("AWS4"+r.secretKey), dateStamp)
	kRegion := r2HMAC(kDate, r2Region)
	kService := r2HMAC(kRegion, r2Service)
	kSigning := r2HMAC(kService, "aws4_request")
	signature := hex.EncodeToString(r2HMAC(kSigning, stringToSign))

	return "https://" + host + canonURI + "?" + canonicalQuery + "&X-Amz-Signature=" + signature
}
