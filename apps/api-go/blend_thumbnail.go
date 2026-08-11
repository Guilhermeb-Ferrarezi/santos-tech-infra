package main

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"image"
	"image/png"
	"io"
	"strconv"

	"github.com/klauspost/compress/zstd"
)

// blendThumbScanBudget: quantos bytes descomprimidos lemos no máximo procurando
// o bloco TEST. O Blender grava a miniatura logo depois do REND, perto do
// início do arquivo — 4MB é bem mais que suficiente e evita descomprimir um
// .blend inteiro (até 50MB) só pra checar se tem miniatura.
const blendThumbScanBudget = 4 << 20

// extractBlendThumbnailPNG tenta extrair a miniatura embutida de um .blend
// (bloco TEST, gravado pelo Blender quando salvo com "Save Preview Images").
// É best-effort: qualquer coisa fora do esperado (sem miniatura, formato não
// reconhecido, arquivo corrompido) devolve ok=false silenciosamente — nunca
// deve bloquear o upload do modelo.
func extractBlendThumbnailPNG(raw []byte) (pngBytes []byte, ok bool) {
	data, ok := decompressBlendPrefix(raw)
	if !ok {
		return nil, false
	}
	w, h, pix, ok := findBlendTestBlock(data)
	if !ok {
		return nil, false
	}
	img := &image.RGBA{Pix: pix, Stride: w * 4, Rect: image.Rect(0, 0, w, h)}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, false
	}
	return buf.Bytes(), true
}

// decompressBlendPrefix devolve os primeiros bytes (já descomprimidos, se
// preciso) do .blend, o bastante pra achar o header e os primeiros blocos.
func decompressBlendPrefix(raw []byte) ([]byte, bool) {
	if bytes.HasPrefix(raw, []byte("BLENDER")) {
		return raw, true
	}

	var r io.Reader
	switch {
	case len(raw) >= 4 && raw[0] == 0x28 && raw[1] == 0xb5 && raw[2] == 0x2f && raw[3] == 0xfd:
		zr, err := zstd.NewReader(bytes.NewReader(raw))
		if err != nil {
			return nil, false
		}
		defer zr.Close()
		r = zr
	case len(raw) >= 2 && raw[0] == 0x1f && raw[1] == 0x8b:
		gr, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return nil, false
		}
		defer gr.Close()
		r = gr
	default:
		return nil, false
	}

	buf := make([]byte, blendThumbScanBudget)
	n, err := io.ReadFull(r, buf)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return nil, false
	}
	buf = buf[:n]
	if !bytes.HasPrefix(buf, []byte("BLENDER")) {
		return nil, false
	}
	return buf, true
}

// findBlendTestBlock percorre os blocos do .blend (formato legado, header
// fixo de 12 bytes + BHead de 24 bytes; ou formato novo do Blender 5.0+,
// header com tamanho variável embutido + BHead de 32 bytes — ver
// BLO_core_bhead.hh no source do Blender) até achar o bloco "TEST"
// (miniatura), gravado logo após o "REND".
func findBlendTestBlock(data []byte) (w, h int, pix []byte, ok bool) {
	if len(data) < 12 || !bytes.HasPrefix(data, []byte("BLENDER")) {
		return 0, 0, nil, false
	}

	var headerSize, bheadSize int
	if data[7] >= '0' && data[7] <= '9' {
		// formato novo: "BLENDER" + dígitos(tamanho do header) + '-' + ...
		i := 7
		for i < len(data) && data[i] != '-' {
			i++
		}
		if i == 7 || i >= len(data) {
			return 0, 0, nil, false
		}
		hs, err := strconv.Atoi(string(data[7:i]))
		if err != nil || hs <= 0 {
			return 0, 0, nil, false
		}
		headerSize, bheadSize = hs, 32 // LargeBHead8
	} else {
		headerSize, bheadSize = 12, 24 // SmallBHead8 (formato legado, 64-bit)
	}

	off := headerSize
	for iter := 0; iter < 64; iter++ {
		if off+bheadSize > len(data) {
			return 0, 0, nil, false
		}
		code := string(bytes.TrimRight(data[off:off+4], "\x00"))

		var blockLen int64
		if bheadSize == 32 {
			blockLen = int64(binary.LittleEndian.Uint64(data[off+16 : off+24]))
		} else {
			blockLen = int64(int32(binary.LittleEndian.Uint32(data[off+4 : off+8])))
		}
		payloadOff := off + bheadSize

		if code == "TEST" {
			if payloadOff+8 > len(data) {
				return 0, 0, nil, false
			}
			ww := int(int32(binary.LittleEndian.Uint32(data[payloadOff : payloadOff+4])))
			hh := int(int32(binary.LittleEndian.Uint32(data[payloadOff+4 : payloadOff+8])))
			if ww <= 0 || hh <= 0 || ww > 1024 || hh > 1024 {
				return 0, 0, nil, false
			}
			need := ww * hh * 4
			pixOff := payloadOff + 8
			if pixOff+need > len(data) {
				return 0, 0, nil, false
			}
			return ww, hh, data[pixOff : pixOff+need], true
		}

		if code == "" || code == "ENDB" || blockLen < 0 {
			return 0, 0, nil, false
		}
		off = payloadOff + int(blockLen)
	}
	return 0, 0, nil, false
}
