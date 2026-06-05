package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"mime/multipart"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Upload de imagens pro R2 via POST /auth/upload do auth central (multipart,
// campo "file"). O formato é detectado pelo conteúdo lá; aqui só validamos
// base64 e tamanho.

const maxUploadBytes = 5 << 20 // 5 MB — mesmo teto do /auth/upload

type uploadImageInput struct {
	ImageBase64 string `json:"imageBase64" jsonschema:"conteúdo da imagem em base64 (aceita com ou sem prefixo data:...;base64,); png, jpeg, webp ou gif; máx 5MB"`
}

func (s *Server) addUploadTools(srv *mcp.Server) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "upload_image",
		Description: "Sobe uma imagem (png/jpeg/webp/gif, máx 5MB) para o armazenamento (R2) e devolve a URL pública (CDN).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in uploadImageInput) (*mcp.CallToolResult, any, error) {
		b64 := strings.TrimSpace(in.ImageBase64)
		if i := strings.Index(b64, ";base64,"); i >= 0 { // data URI
			b64 = b64[i+len(";base64,"):]
		}
		data, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return errResult("imageBase64 inválido: " + err.Error()), nil, nil
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
		url := s.cfg.AuthBaseURL + "/auth/upload"
		status, raw, err := s.client.doRaw(ctx, "POST", url, authorization(req.Extra), mw.FormDataContentType(), &buf)
		return resultFrom("POST", url, status, raw, err)
	})
}
