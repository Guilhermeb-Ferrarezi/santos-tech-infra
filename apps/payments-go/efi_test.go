package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"math/big"
	"testing"
	"time"

	pkcs12 "software.sslmate.com/src/go-pkcs12"
)

// genTestP12Base64 gera um par chave/cert self-signed e devolve um .p12 (senha vazia) em base64.
func genTestP12Base64(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test-efi"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}
	raw, err := pkcs12.Modern.Encode(key, cert, nil, "")
	if err != nil {
		t.Fatalf("encode p12: %v", err)
	}
	return base64.StdEncoding.EncodeToString(raw)
}

func TestLoadClientCert(t *testing.T) {
	b64 := genTestP12Base64(t)
	cert, err := loadClientCert(b64, "")
	if err != nil {
		t.Fatalf("loadClientCert: %v", err)
	}
	if cert.Leaf == nil || cert.Leaf.Subject.CommonName != "test-efi" {
		t.Fatalf("cert sem leaf esperado: %+v", cert.Leaf)
	}
}

func TestLoadClientCertRejeitaBase64Invalido(t *testing.T) {
	if _, err := loadClientCert("não é base64 @@@", ""); err == nil {
		t.Fatal("esperava erro com base64 inválido")
	}
}
