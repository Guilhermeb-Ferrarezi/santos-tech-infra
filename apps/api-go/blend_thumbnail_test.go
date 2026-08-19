package main

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"image/png"
	"testing"
)

// appendSmallBHead8 grava um BHead do formato legado (64-bit): code(4) len(4) old(8) SDNAnr(4) nr(4) = 24 bytes.
func appendSmallBHead8(buf *bytes.Buffer, code string, payload []byte) {
	buf.WriteString(code)
	var le32 [4]byte
	binary.LittleEndian.PutUint32(le32[:], uint32(len(payload)))
	buf.Write(le32[:])         // len
	buf.Write(make([]byte, 8)) // old (ponteiro, irrelevante)
	buf.Write(make([]byte, 4)) // SDNAnr
	binary.LittleEndian.PutUint32(le32[:], 1)
	buf.Write(le32[:]) // nr
	buf.Write(payload)
}

// appendLargeBHead8 grava um BHead do formato novo (Blender 5.0+): code(4) SDNAnr(4) old(8) len(8) nr(8) = 32 bytes.
func appendLargeBHead8(buf *bytes.Buffer, code string, payload []byte) {
	buf.WriteString(code)
	buf.Write(make([]byte, 4)) // SDNAnr
	buf.Write(make([]byte, 8)) // old
	var le64 [8]byte
	binary.LittleEndian.PutUint64(le64[:], uint64(len(payload)))
	buf.Write(le64[:]) // len
	binary.LittleEndian.PutUint64(le64[:], 1)
	buf.Write(le64[:]) // nr
	buf.Write(payload)
}

// testBlendPixels gera pixels RGBA de teste sempre opacos (alpha=255) — evita
// que a comparação de round-trip do PNG se enrole com pré-multiplicação de
// alfa (fora de escopo aqui; miniaturas reais do Blender também são opacas).
func testBlendPixels(w, h int) []byte {
	pix := make([]byte, w*h*4)
	for i := 0; i < w*h; i++ {
		pix[i*4+0] = byte((i * 37) % 256)
		pix[i*4+1] = byte((i * 61) % 256)
		pix[i*4+2] = byte((i * 89) % 256)
		pix[i*4+3] = 255
	}
	return pix
}

func testBlockPayload(w, h int, pix []byte) []byte {
	var buf bytes.Buffer
	var le32 [4]byte
	binary.LittleEndian.PutUint32(le32[:], uint32(w))
	buf.Write(le32[:])
	binary.LittleEndian.PutUint32(le32[:], uint32(h))
	buf.Write(le32[:])
	buf.Write(pix)
	return buf.Bytes()
}

func buildLegacyBlend(withThumb bool) []byte {
	var buf bytes.Buffer
	buf.WriteString("BLENDER-v301")
	appendSmallBHead8(&buf, "REND", make([]byte, 8))
	if withThumb {
		pix := testBlendPixels(2, 2)
		appendSmallBHead8(&buf, "TEST", testBlockPayload(2, 2, pix))
	}
	appendSmallBHead8(&buf, "ENDB", nil)
	return buf.Bytes()
}

func buildNewFormatBlend(withThumb bool) []byte {
	var buf bytes.Buffer
	buf.WriteString("BLENDER17-01v0502")
	appendLargeBHead8(&buf, "REND", make([]byte, 8))
	if withThumb {
		pix := testBlendPixels(3, 2)
		appendLargeBHead8(&buf, "TEST", testBlockPayload(3, 2, pix))
	}
	appendLargeBHead8(&buf, "ENDB", nil)
	return buf.Bytes()
}

func TestExtractBlendThumbnailPNG(t *testing.T) {
	t.Run("formato legado com miniatura", func(t *testing.T) {
		png, ok := extractBlendThumbnailPNG(buildLegacyBlend(true))
		if !ok {
			t.Fatal("esperava ok=true")
		}
		assertDecodesTo(t, png, 2, 2, testBlendPixels(2, 2))
	})

	t.Run("formato novo (Blender 5.0+) com miniatura", func(t *testing.T) {
		png, ok := extractBlendThumbnailPNG(buildNewFormatBlend(true))
		if !ok {
			t.Fatal("esperava ok=true")
		}
		assertDecodesTo(t, png, 3, 2, testBlendPixels(3, 2))
	})

	t.Run("sem bloco TEST", func(t *testing.T) {
		if _, ok := extractBlendThumbnailPNG(buildLegacyBlend(false)); ok {
			t.Fatal("esperava ok=false (sem miniatura salva)")
		}
	})

	t.Run("gzip (compressão legada)", func(t *testing.T) {
		raw := buildLegacyBlend(true)
		var gz bytes.Buffer
		w := gzip.NewWriter(&gz)
		w.Write(raw)
		w.Close()

		png, ok := extractBlendThumbnailPNG(gz.Bytes())
		if !ok {
			t.Fatal("esperava ok=true (gzip)")
		}
		assertDecodesTo(t, png, 2, 2, testBlendPixels(2, 2))
	})

	t.Run("lixo", func(t *testing.T) {
		if _, ok := extractBlendThumbnailPNG([]byte("isso não é um blend")); ok {
			t.Fatal("esperava ok=false")
		}
	})

	t.Run("vazio", func(t *testing.T) {
		if _, ok := extractBlendThumbnailPNG(nil); ok {
			t.Fatal("esperava ok=false")
		}
	})

	// blockLen absurdo estourava payloadOff+int(blockLen) e deixava off
	// NEGATIVO — a checagem off+bheadSize > len(data) da iteração seguinte não
	// pegava (negativo não é maior que len) e data[off:off+4] dava panic de
	// slice bounds. Um .blend forjado num upload derrubava o handler.
	t.Run("blockLen forjado não causa panic", func(t *testing.T) {
		for _, blockLen := range []uint64{
			0x7FFF_FFFF_FFFF_FFFF, // int64 máximo: overflow ao somar
			0xFFFF_FFFF_FFFF_FFFF, // vira -1 em int64
			1 << 62,
			1 << 31,
		} {
			var buf bytes.Buffer
			buf.WriteString("BLENDER17-01v0502")
			buf.WriteString("REND")
			buf.Write(make([]byte, 4)) // SDNAnr
			buf.Write(make([]byte, 8)) // old
			var le64 [8]byte
			binary.LittleEndian.PutUint64(le64[:], blockLen)
			buf.Write(le64[:])          // len forjado
			buf.Write(make([]byte, 8))  // nr
			buf.Write(make([]byte, 64)) // payload qualquer

			if _, ok := extractBlendThumbnailPNG(buf.Bytes()); ok {
				t.Errorf("blockLen=%#x: esperava ok=false", blockLen)
			}
		}
	})

	// Formato legado (len em int32): mesmo tratamento, o bloco não pode ser
	// maior que o próprio arquivo.
	t.Run("blockLen legado maior que o arquivo", func(t *testing.T) {
		var buf bytes.Buffer
		buf.WriteString("BLENDER-v301")
		buf.WriteString("REND")
		var le32 [4]byte
		binary.LittleEndian.PutUint32(le32[:], 0x7FFF_FFFF)
		buf.Write(le32[:])
		buf.Write(make([]byte, 8))
		buf.Write(make([]byte, 4))
		buf.Write(make([]byte, 4))
		buf.Write(make([]byte, 64))

		if _, ok := extractBlendThumbnailPNG(buf.Bytes()); ok {
			t.Fatal("esperava ok=false")
		}
	})
}

// assertDecodesTo confere o PNG contra wantPix (o buffer bruto do bloco TEST,
// bottom-up) — extractBlendThumbnailPNG inverte as linhas, então a linha y da
// saída deve bater com a linha (h-1-y) do buffer original.
func assertDecodesTo(t *testing.T, pngBytes []byte, wantW, wantH int, wantPix []byte) {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(pngBytes))
	if err != nil {
		t.Fatalf("PNG inválido: %v", err)
	}
	b := img.Bounds()
	if b.Dx() != wantW || b.Dy() != wantH {
		t.Fatalf("dimensões=%dx%d, queria %dx%d", b.Dx(), b.Dy(), wantW, wantH)
	}
	for y := 0; y < wantH; y++ {
		for x := 0; x < wantW; x++ {
			r, g, bl, a := img.At(x, y).RGBA()
			i := ((wantH-1-y)*wantW + x) * 4
			got := [4]byte{byte(r >> 8), byte(g >> 8), byte(bl >> 8), byte(a >> 8)}
			want := [4]byte{wantPix[i], wantPix[i+1], wantPix[i+2], wantPix[i+3]}
			if got != want {
				t.Fatalf("pixel(%d,%d)=%v, queria %v", x, y, got, want)
			}
		}
	}
}
