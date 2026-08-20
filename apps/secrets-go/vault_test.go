package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testVault(t *testing.T) *Vault {
	t.Helper()
	v := newVault("segredo-de-teste", "", vaultHKDFInfo)
	if v == nil {
		t.Fatal("newVault devolveu nil com secret preenchido")
	}
	return v
}

func TestVaultRoundTrip(t *testing.T) {
	v := testVault(t)
	const plain = "sk-ant-api03-abcdefghijklmnop"
	enc, err := v.Encrypt(plain, hitAAD("doc1"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(enc, plain) {
		t.Fatal("ciphertext contém o texto claro")
	}
	got, err := v.Decrypt(enc, hitAAD("doc1"))
	if err != nil || got != plain {
		t.Fatalf("round-trip falhou: %q %v", got, err)
	}
	// AAD amarra ao documento: o mesmo blob não pode ser movido para outro hit.
	if _, err := v.Decrypt(enc, hitAAD("doc2")); err == nil {
		t.Fatal("blob de um doc não deveria decifrar em outro doc")
	}
}

func TestVaultDesabilitado(t *testing.T) {
	var v *Vault
	if _, err := v.Encrypt("x", nil); err != errVaultDisabled {
		t.Fatalf("esperava errVaultDisabled, veio %v", err)
	}
	if _, err := v.Decrypt("v1.abc", nil); err != errVaultDisabled {
		t.Fatalf("esperava errVaultDisabled, veio %v", err)
	}
}

func TestValuePrefixNaoRevelaMaisQueUmTerco(t *testing.T) {
	if got := valuePrefix("sk-ant-api03-abcdefghijklmnopqrst"); got != "sk-ant-a…" {
		t.Fatalf("prefixo inesperado: %q", got)
	}
	// Valor curto: no máximo um terço.
	if got := valuePrefix("abcdef"); got != "ab…" {
		t.Fatalf("prefixo de valor curto inesperado: %q", got)
	}
	if got := valuePrefix("ab"); got != "" {
		t.Fatalf("valor curtíssimo deveria dar prefixo vazio: %q", got)
	}
}

// O documento gravado no Elasticsearch não pode conter o valor em claro.
func TestIndexHitNaoGravaValorEmClaro(t *testing.T) {
	var gotBody []byte
	es := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.Write([]byte(`{"result":"created"}`))
	}))
	defer es.Close()

	client := NewElasticClient(es.URL, testVault(t))
	const plain = "ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	err := client.IndexHit(context.Background(), Hit{
		Keyword: "GITHUB_TOKEN", RepoFullName: "a/b", FilePath: ".env",
		HasCandidate: true, MatchedValue: plain, IndexedAt: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(gotBody), plain) {
		t.Fatalf("valor em claro foi para o Elasticsearch: %s", gotBody)
	}
	var doc map[string]any
	if err := json.Unmarshal(gotBody, &doc); err != nil {
		t.Fatal(err)
	}
	if _, ok := doc["matchedValue"]; ok {
		t.Fatal("campo legado matchedValue não deveria ser reescrito")
	}
	if doc["matchedValueEnc"] == nil || doc["matchedValueHash"] == nil || doc["matchedValuePrefix"] == nil {
		t.Fatalf("faltou enc/hash/prefix no documento: %v", doc)
	}
}

// A listagem devolve prefixo + hash, nunca o valor — mesmo quando o documento
// no índice ainda está no formato legado (valor em claro).
func TestListHitsNaoSerializaValor(t *testing.T) {
	const plain = "sk_live_naoDeveVazar123456"
	es := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"hits":{"hits":[{"_id":"abc","_source":{"keyword":"K","matchedValue":"` + plain + `"}}]}}`))
	}))
	defer es.Close()

	client := NewElasticClient(es.URL, testVault(t))
	hits, err := client.ListHits(context.Background(), "", false, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatalf("esperava 1 hit, veio %d", len(hits))
	}
	// Em memória o valor está disponível (revalidação/sync precisam dele)…
	if hits[0].MatchedValue != plain {
		t.Fatalf("valor legado não foi lido: %q", hits[0].MatchedValue)
	}
	if hits[0].ID != "abc" {
		t.Fatalf("_id não propagado: %q", hits[0].ID)
	}
	// …mas nunca ao serializar de volta para o cliente.
	out, _ := json.Marshal(hits)
	if strings.Contains(string(out), plain) {
		t.Fatalf("listagem serializou o valor: %s", out)
	}
	if !strings.Contains(string(out), `"matchedValueHash"`) {
		t.Fatalf("listagem sem fingerprint: %s", out)
	}
}

func TestValidHitID(t *testing.T) {
	ok := docID("a/b", ".env", "K")
	if !validHitID(ok) {
		t.Fatalf("docID gerado deveria ser válido: %q", ok)
	}
	for _, bad := range []string{"", "x", strings.Repeat("g", 40), ok + "a", "../_cluster/health"} {
		if validHitID(bad) {
			t.Fatalf("id inválido aceito: %q", bad)
		}
	}
}
