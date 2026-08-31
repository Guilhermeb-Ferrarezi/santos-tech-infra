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

// r2EncodePath codifica CADA segmento de um path (bucket ou key) nas regras
// de path-escaping do SigV4 (RFC 3986, "/" preservado como separador) — sem
// isso, uma key com espaço ou acento (ex: "Sequência 21.mp4") assina um
// canonical URI cru, mas o net/http reescreve o path de verdade (%20, %C3%AA
// etc.) antes de mandar pra rede: a assinatura nunca bate com o que o R2
// recebe e todo PUT/DELETE/HEAD nesse objeto falha com SignatureDoesNotMatch.
func r2EncodePath(p string) string {
	segments := strings.Split(p, "/")
	for i, seg := range segments {
		segments[i] = url.PathEscape(seg)
	}
	return strings.Join(segments, "/")
}

// Upload faz PUT do objeto em key e devolve a URL pública ({publicURL}/{key}).
func (r *R2) Upload(ctx context.Context, key, contentType string, body []byte) (string, error) {
	host := r.accountID + ".r2.cloudflarestorage.com"
	canonURI := "/" + r2EncodePath(r.bucket) + "/" + r2EncodePath(key)

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

// Delete remove o objeto em key. Mesma assinatura SigV4 do Upload, método DELETE
// e sem corpo (payload hash do vazio).
func (r *R2) Delete(ctx context.Context, key string) error {
	host := r.accountID + ".r2.cloudflarestorage.com"
	canonURI := "/" + r2EncodePath(r.bucket) + "/" + r2EncodePath(key)

	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")
	payloadHash := r2SHA256(nil)

	canonicalHeaders := "host:" + host + "\n" +
		"x-amz-content-sha256:" + payloadHash + "\n" +
		"x-amz-date:" + amzDate + "\n"
	signedHeaders := "host;x-amz-content-sha256;x-amz-date"

	canonicalRequest := strings.Join([]string{
		http.MethodDelete, canonURI, "", canonicalHeaders, signedHeaders, payloadHash,
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

	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, "https://"+host+canonURI, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", auth)
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	req.Header.Set("X-Amz-Date", amzDate)

	resp, err := r.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// R2/S3 devolve 204 no delete bem-sucedido; 404 também é aceitável aqui (idempotente).
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusNotFound {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("r2 delete %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

// HeadObject devolve o tamanho e o Content-Type que o objeto REALMENTE tem no
// R2. É o enforcement server-side do upload presigned: os bytes vão direto do
// navegador pro storage, então o backend só descobre o que subiu de fato
// perguntando ao R2 — sem isto, o tamanho e o tipo gravados no catálogo eram os
// que o cliente declarou.
//
// found=false (404) significa que o PUT nunca aconteceu.
func (r *R2) HeadObject(ctx context.Context, key string) (size int64, contentType string, found bool, err error) {
	host := r.accountID + ".r2.cloudflarestorage.com"
	canonURI := "/" + r2EncodePath(r.bucket) + "/" + r2EncodePath(key)

	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")
	payloadHash := r2SHA256(nil)

	canonicalHeaders := "host:" + host + "\n" +
		"x-amz-content-sha256:" + payloadHash + "\n" +
		"x-amz-date:" + amzDate + "\n"
	signedHeaders := "host;x-amz-content-sha256;x-amz-date"

	canonicalRequest := strings.Join([]string{
		http.MethodHead, canonURI, "", canonicalHeaders, signedHeaders, payloadHash,
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

	req, err := http.NewRequestWithContext(ctx, http.MethodHead, "https://"+host+canonURI, nil)
	if err != nil {
		return 0, "", false, err
	}
	req.Header.Set("Authorization", auth)
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	req.Header.Set("X-Amz-Date", amzDate)

	resp, err := r.http.Do(req)
	if err != nil {
		return 0, "", false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return 0, "", false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return 0, "", false, fmt.Errorf("r2 head %d", resp.StatusCode)
	}
	// HEAD não tem corpo: o tamanho vem do Content-Length.
	size, _ = strconv.ParseInt(resp.Header.Get("Content-Length"), 10, 64)
	contentType = strings.TrimSpace(resp.Header.Get("Content-Type"))
	if i := strings.IndexByte(contentType, ';'); i >= 0 {
		contentType = strings.TrimSpace(contentType[:i])
	}
	return size, strings.ToLower(contentType), true, nil
}

// PresignGet gera uma URL GET pré-assinada pra key, válida por `expiry` —
// substitui o link público permanente onde o objeto não deve ficar acessível
// pra sempre a quem já viu a URL uma vez (ver downloadURL).
//
// ATENÇÃO: isto só protege de fato se o objeto NÃO estiver sendo servido
// publicamente pelo domínio de CDN do bucket (CF_R2_PUBLIC_URL). Se o prefixo
// continuar público, o mesmo arquivo segue acessível pela URL permanente,
// assinada ou não — a parte do R2/Cloudflare tem que acompanhar.
func (r *R2) PresignGet(key string, expiry time.Duration) string {
	return r.presign(http.MethodGet, key, "", expiry)
}

// PresignPut gera uma URL PUT pré-assinada (SigV4 via query string) pra key,
// válida por `expiry`: o cliente sobe o arquivo direto pro R2 com um PUT nessa URL,
// sem os bytes passarem pelo backend — usado pra arquivos grandes demais pra
// bufferizar em memória numa request normal (ex.: vídeo).
//
// O `contentType` é FIXADO PELO SERVIDOR e entra na assinatura (SignedHeaders
// "content-type;host"): o PUT real só é aceito pelo R2 se mandar exatamente esse
// Content-Type. Sem isso o cliente escolheria o tipo livremente e conseguiria
// hospedar text/html no domínio público do bucket (cdn.santos-tech.com) — que é
// same-site com os cookies de .santos-tech.com, virando XSS/phishing sob o
// domínio real. Quem chama deve validar o contentType contra uma allow-list.
//
// O tamanho NÃO é assinado: o teto declarado no pedido da URL não vincula os
// bytes que sobem. Quem grava uma linha de catálogo a partir de um objeto
// presigned tem que conferir depois com HeadObject (ver handleCreateDownload);
// a expiração curta limita a janela em que a URL pode ser reusada pra
// sobrescrever o objeto. Upgrade se abuso virar problema: presigned POST com
// policy de content-length-range, que o R2 também suporta.
func (r *R2) PresignPut(key, contentType string, expiry time.Duration) string {
	return r.presign(http.MethodPut, key, strings.TrimSpace(contentType), expiry)
}

// presign monta a URL SigV4 por query string. contentType vazio => só o header
// "host" é assinado (correto pro GET, que não manda Content-Type); com valor,
// "content-type;host" (ver PresignPut).
func (r *R2) presign(method, key, contentType string, expiry time.Duration) string {
	host := r.accountID + ".r2.cloudflarestorage.com"
	canonURI := "/" + r2EncodePath(r.bucket) + "/" + r2EncodePath(key)

	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")
	scope := dateStamp + "/" + r2Region + "/" + r2Service + "/aws4_request"

	q := url.Values{}
	q.Set("X-Amz-Algorithm", "AWS4-HMAC-SHA256")
	q.Set("X-Amz-Credential", r.accessKey+"/"+scope)
	q.Set("X-Amz-Date", amzDate)
	q.Set("X-Amz-Expires", strconv.Itoa(int(expiry.Seconds())))
	// Ordem alfabética dos headers assinados, nome em minúsculas.
	canonicalHeaders := "host:" + host + "\n"
	signedHeaders := "host"
	if contentType != "" {
		canonicalHeaders = "content-type:" + contentType + "\n" + canonicalHeaders
		signedHeaders = "content-type;host"
	}
	q.Set("X-Amz-SignedHeaders", signedHeaders)
	canonicalQuery := q.Encode()

	canonicalRequest := strings.Join([]string{
		method, canonURI, canonicalQuery, canonicalHeaders, signedHeaders, "UNSIGNED-PAYLOAD",
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
