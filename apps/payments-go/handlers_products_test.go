package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// fakeProductStore implementa productStore em memória, sem Postgres.
type fakeProductStore struct {
	byID   map[int64]*Product
	bySlug map[string]*Product
	nextID int64
}

func newFakeProductStore() *fakeProductStore {
	return &fakeProductStore{byID: map[int64]*Product{}, bySlug: map[string]*Product{}, nextID: 1}
}

func (f *fakeProductStore) CreateProduct(_ context.Context, p *Product) error {
	p.ID = f.nextID
	f.nextID++
	p.Active = true
	cp := *p
	f.byID[p.ID] = &cp
	f.bySlug[p.Slug] = &cp
	return nil
}

func (f *fakeProductStore) ListProducts(_ context.Context) ([]Product, error) {
	out := make([]Product, 0, len(f.byID))
	for _, p := range f.byID {
		out = append(out, *p)
	}
	return out, nil
}

func (f *fakeProductStore) UpdateProduct(_ context.Context, p *Product) error {
	cur, ok := f.byID[p.ID]
	if !ok {
		return pgx.ErrNoRows
	}
	p.Slug = cur.Slug // slug não muda no update
	cp := *p
	f.byID[p.ID] = &cp
	f.bySlug[cp.Slug] = &cp
	return nil
}

func (f *fakeProductStore) DeleteProduct(_ context.Context, id int64) error {
	p, ok := f.byID[id]
	if !ok {
		return pgx.ErrNoRows
	}
	delete(f.byID, id)
	delete(f.bySlug, p.Slug)
	return nil
}

func (f *fakeProductStore) GetProductByID(_ context.Context, id int64) (*Product, error) {
	p, ok := f.byID[id]
	if !ok {
		return nil, pgx.ErrNoRows
	}
	cp := *p
	return &cp, nil
}

func (f *fakeProductStore) GetProductBySlug(_ context.Context, slug string) (*Product, error) {
	p, ok := f.bySlug[slug]
	if !ok {
		return nil, pgx.ErrNoRows
	}
	cp := *p
	return &cp, nil
}

func TestCreateProduct_ComImagemEArquivo(t *testing.T) {
	s := &Server{products: newFakeProductStore()}
	rec := httptest.NewRecorder()
	body := `{"slug":"ebook","name":"E-book","priceCents":1990,"imageUrl":"https://cdn/x.png","fileUrl":"https://cdn/x.pdf"}`
	req := httptest.NewRequest("POST", "/products", strings.NewReader(body))
	s.handleCreateProduct(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("esperava 201, veio %d (%s)", rec.Code, rec.Body.String())
	}
	var got Product
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("resposta inválida: %v", err)
	}
	if got.ImageURL != "https://cdn/x.png" || got.FileURL != "https://cdn/x.pdf" {
		t.Fatalf("imageUrl/fileUrl não persistidos: %+v", got)
	}
	if got.ID == 0 {
		t.Fatal("produto criado deveria ter id")
	}
}

func TestUpdateProduct_AtualizaImagemEArquivo(t *testing.T) {
	fake := newFakeProductStore()
	s := &Server{products: fake}
	// cria primeiro
	_ = fake.CreateProduct(context.Background(), &Product{Slug: "curso", Name: "Curso", PriceCents: 5000})

	rec := httptest.NewRecorder()
	body := `{"name":"Curso Pro","priceCents":7000,"active":true,"imageUrl":"https://cdn/cap.png","fileUrl":"https://cdn/material.pdf"}`
	req := httptest.NewRequest("PUT", "/products/1", strings.NewReader(body))
	req.SetPathValue("id", "1")
	s.handleUpdateProduct(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("esperava 200, veio %d (%s)", rec.Code, rec.Body.String())
	}
	stored, _ := fake.GetProductByID(context.Background(), 1)
	if stored.ImageURL != "https://cdn/cap.png" || stored.FileURL != "https://cdn/material.pdf" {
		t.Fatalf("update não gravou imageUrl/fileUrl: %+v", stored)
	}
}

func TestGetProductByID_Handler(t *testing.T) {
	fake := newFakeProductStore()
	s := &Server{products: fake}
	_ = fake.CreateProduct(context.Background(), &Product{Slug: "p", Name: "P", PriceCents: 100, ImageURL: "https://cdn/i.png", FileURL: "https://cdn/f.pdf"})

	// encontrado
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/products/1", nil)
	req.SetPathValue("id", "1")
	s.handleGetProduct(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("esperava 200, veio %d (%s)", rec.Code, rec.Body.String())
	}
	var got Product
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("resposta inválida: %v", err)
	}
	if got.ID != 1 || got.ImageURL != "https://cdn/i.png" || got.FileURL != "https://cdn/f.pdf" {
		t.Fatalf("produto retornado incorreto: %+v", got)
	}

	// inexistente → 404
	rec = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/products/999", nil)
	req.SetPathValue("id", "999")
	s.handleGetProduct(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("esperava 404 para id inexistente, veio %d", rec.Code)
	}
}

// TestGetProductBySlugNaoVazaFileURL: /products/by-slug é PÚBLICO. Antes ele serializava
// o Product inteiro, entregando o fileUrl — o produto comprado — a quem soubesse o slug.
// A resposta deve trazer só a vitrine, com hasFile no lugar da URL.
func TestGetProductBySlugNaoVazaFileURL(t *testing.T) {
	st := newFakeProductStore()
	const secreto = "https://cdn.santos-tech.com/entregaveis/curso-secreto.pdf"
	_ = st.CreateProduct(context.Background(), &Product{
		Slug: "curso", Name: "Curso", PriceCents: 9900, Active: true,
		ImageURL: "https://cdn.santos-tech.com/capa.png", FileURL: secreto,
	})

	s := &Server{products: st}
	req := httptest.NewRequest("GET", "/products/by-slug/curso", nil)
	req.SetPathValue("slug", "curso")
	w := httptest.NewRecorder()
	s.handleGetProductBySlug(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("esperado 200, veio %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if strings.Contains(body, secreto) {
		t.Fatalf("rota pública vazou o entregável: %s", body)
	}
	if strings.Contains(body, "fileUrl") {
		t.Fatalf("rota pública não deveria ter o campo fileUrl: %s", body)
	}

	var got PublicProduct
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("resposta não é PublicProduct: %v", err)
	}
	if !got.HasFile {
		t.Fatal("hasFile deveria ser true (existe entregável, sem revelar a URL)")
	}
	if got.Name != "Curso" || got.PriceCents != 9900 || got.ImageURL == "" {
		t.Fatalf("a vitrine deveria continuar completa: %+v", got)
	}
}

// TestPublicProductSemEntregavel: sem file_url, hasFile é false.
func TestPublicProductSemEntregavel(t *testing.T) {
	if publicProduct(Product{Slug: "x", FileURL: "   "}).HasFile {
		t.Fatal("file_url em branco não deveria contar como entregável")
	}
	if !publicProduct(Product{Slug: "x", FileURL: "https://a/b.pdf"}).HasFile {
		t.Fatal("file_url preenchido deveria marcar hasFile")
	}
}
