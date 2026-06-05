package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Upload de imagens pro R2 via POST /auth/upload do auth central (multipart,
// campo "file"). O formato é detectado pelo conteúdo lá; aqui só validamos
// origem (URL ou base64) e tamanho.

const maxUploadBytes = 5 << 20 // 5 MB — mesmo teto do /auth/upload

type uploadImageInput struct {
	ImageURL    string `json:"imageUrl,omitempty" jsonschema:"URL pública (http/https) da imagem — PREFIRA este campo: o servidor baixa direto, sem você transcrever bytes"`
	ImageBase64 string `json:"imageBase64,omitempty" jsonschema:"alternativa para imagens MINÚSCULAS (poucos KB): conteúdo em base64 (aceita data URI); para qualquer outra use imageUrl"`
}

func (s *Server) addUploadTools(srv *mcp.Server) {
	mcp.AddTool(srv, &mcp.Tool{
		Name: "upload_image",
		Description: "Sobe uma imagem (png/jpeg/webp/gif, máx 5MB) para o armazenamento (R2) e devolve a URL pública (CDN). " +
			"Informe imageUrl (preferido — o servidor baixa a imagem) OU imageBase64 (só para imagens minúsculas). " +
			"ARQUIVO LOCAL (você tem acesso a shell/Bash)? NÃO use esta tool — suba direto: " +
			"`curl -F file=@caminho.png -H 'Authorization: Bearer <token>' https://api.santos-tech.com/auth/upload` " +
			"(avatar do usuário: mesma chamada em /auth/avatar).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in uploadImageInput) (*mcp.CallToolResult, any, error) {
		var data []byte
		switch {
		case in.ImageURL != "" && in.ImageBase64 != "":
			return errResult("informe imageUrl OU imageBase64, não os dois"), nil, nil
		case in.ImageURL != "":
			d, errMsg := s.fetchImage(ctx, in.ImageURL)
			if errMsg != "" {
				return errResult(errMsg), nil, nil
			}
			data = d
		case in.ImageBase64 != "":
			b64 := strings.TrimSpace(in.ImageBase64)
			if i := strings.Index(b64, ";base64,"); i >= 0 { // data URI
				b64 = b64[i+len(";base64,"):]
			}
			d, err := base64.StdEncoding.DecodeString(b64)
			if err != nil {
				return errResult("imageBase64 inválido: " + err.Error()), nil, nil
			}
			data = d
		default:
			return errResult("informe imageUrl (preferido) ou imageBase64"), nil, nil
		}
		if len(data) == 0 {
			return errResult("imagem vazia"), nil, nil
		}
		if len(data) > maxUploadBytes {
			return errResult("imagem grande demais (máx 5MB)"), nil, nil
		}

		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		fw, err := mw.CreateFormFile("file", "upload")
		if err != nil {
			return errResult("falha ao montar o upload: " + err.Error()), nil, nil
		}
		if _, err := fw.Write(data); err != nil {
			return errResult("falha ao montar o upload: " + err.Error()), nil, nil
		}
		mw.Close()

		ctx, cancel := context.WithTimeout(ctx, defaultCallTimeout)
		defer cancel()
		u := s.cfg.AuthBaseURL + "/auth/upload"
		status, raw, err := s.client.doRaw(ctx, "POST", u, authorization(req.Extra), mw.FormDataContentType(), &buf)
		return resultFrom("POST", u, status, raw, err)
	})
}

// fetchImage baixa a imagem da URL do usuário (cliente anti-SSRF; só IPs
// públicos). Devolve os bytes ou uma mensagem de erro pronta pro usuário.
func (s *Server) fetchImage(ctx context.Context, rawURL string) ([]byte, string) {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, "imageUrl inválida: use uma URL http(s) completa"
	}
	req, err := http.NewRequestWithContext(ctx, "GET", rawURL, nil)
	if err != nil {
		return nil, "imageUrl inválida: " + err.Error()
	}
	resp, err := s.fetch.Do(req)
	if err != nil {
		return nil, "não consegui baixar a imagem: " + err.Error()
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Sprintf("não consegui baixar a imagem: HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxUploadBytes+1))
	if err != nil {
		return nil, "falha lendo a imagem: " + err.Error()
	}
	if len(data) > maxUploadBytes {
		return nil, "imagem grande demais (máx 5MB)"
	}
	return data, ""
}
