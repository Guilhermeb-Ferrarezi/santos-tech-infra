// Gera os ícones de status da bandeja (bolinha colorida com contorno escuro,
// 32x32, fundo transparente) sem depender de nenhuma lib de imagem — PNG é
// simples o bastante pra montar na mão (IHDR + IDAT com zlib do próprio
// runtime + IEND). Rodar uma vez com `bun run gen-tray-dots.mjs` sempre que
// as cores precisarem mudar; os arquivos gerados (tray-*.png) ficam commitados
// como qualquer outro asset de ícone.
import { deflateSync } from "node:zlib";
import { writeFileSync } from "node:fs";

const SIZE = 32;

const CRC_TABLE = (() => {
  const table = new Uint32Array(256);
  for (let n = 0; n < 256; n++) {
    let c = n;
    for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
    table[n] = c >>> 0;
  }
  return table;
})();

function crc32(buf) {
  let c = 0xffffffff;
  for (const byte of buf) c = CRC_TABLE[(c ^ byte) & 0xff] ^ (c >>> 8);
  return (c ^ 0xffffffff) >>> 0;
}

function chunk(type, data) {
  const typeBuf = Buffer.from(type, "ascii");
  const len = Buffer.alloc(4);
  len.writeUInt32BE(data.length);
  const crcBuf = Buffer.alloc(4);
  crcBuf.writeUInt32BE(crc32(Buffer.concat([typeBuf, data])));
  return Buffer.concat([len, typeBuf, data, crcBuf]);
}

function pngFromRGBA(width, height, rgba) {
  const ihdr = Buffer.alloc(13);
  ihdr.writeUInt32BE(width, 0);
  ihdr.writeUInt32BE(height, 4);
  ihdr[8] = 8; // bit depth
  ihdr[9] = 6; // color type RGBA
  ihdr[10] = 0;
  ihdr[11] = 0;
  ihdr[12] = 0;

  const stride = width * 4;
  const raw = Buffer.alloc((stride + 1) * height);
  for (let y = 0; y < height; y++) {
    raw[y * (stride + 1)] = 0; // filter: none
    rgba.copy(raw, y * (stride + 1) + 1, y * stride, y * stride + stride);
  }
  const idat = deflateSync(raw);

  return Buffer.concat([
    Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
    chunk("IHDR", ihdr),
    chunk("IDAT", idat),
    chunk("IEND", Buffer.alloc(0)),
  ]);
}

// Bolinha sólida com contorno escuro sutil (dá contraste em qualquer tema da
// bandeja, claro ou escuro) — anti-aliasing simples por distância ao centro.
function dot(hex) {
  const r = parseInt(hex.slice(0, 2), 16);
  const g = parseInt(hex.slice(2, 4), 16);
  const b = parseInt(hex.slice(4, 6), 16);
  const cx = SIZE / 2;
  const cy = SIZE / 2;
  const radius = SIZE * 0.36;
  const ringWidth = SIZE * 0.045;

  const buf = Buffer.alloc(SIZE * SIZE * 4);
  for (let y = 0; y < SIZE; y++) {
    for (let x = 0; x < SIZE; x++) {
      const dx = x + 0.5 - cx;
      const dy = y + 0.5 - cy;
      const dist = Math.sqrt(dx * dx + dy * dy);
      const i = (y * SIZE + x) * 4;

      const fillEdge = radius - ringWidth;
      const outerEdge = radius;

      let alpha = 0;
      let cr = r,
        cg = g,
        cb = b;

      if (dist <= fillEdge - 1) {
        alpha = 255;
      } else if (dist <= fillEdge + 1) {
        alpha = 255;
        const t = (dist - (fillEdge - 1)) / 2;
        cr = Math.round(r * (1 - t) + 0x21 * t);
        cg = Math.round(g * (1 - t) + 0x29 * t);
        cb = Math.round(b * (1 - t) + 0x37 * t);
      } else if (dist <= outerEdge - 1) {
        alpha = 255;
        cr = 0x21;
        cg = 0x29;
        cb = 0x37; // #212937 — contorno escuro (mesma família do navy da marca)
      } else if (dist <= outerEdge + 1) {
        alpha = Math.round(255 * (outerEdge + 1 - dist) / 2);
        cr = 0x21;
        cg = 0x29;
        cb = 0x37;
      }

      buf[i] = cr;
      buf[i + 1] = cg;
      buf[i + 2] = cb;
      buf[i + 3] = alpha;
    }
  }
  return buf;
}

const COLORS = {
  "tray-gray": "8a94a3", // sem sessão pareada
  "tray-green": "0db88f", // ativa, saldo ok — verde-teal da marca
  "tray-amber": "f5a623", // saldo baixo (<=10min)
  "tray-red": "e5484d", // saldo esgotado
};

for (const [name, hex] of Object.entries(COLORS)) {
  const rgba = dot(hex);
  // .png é só pra conferir visualmente (Read/preview) — o que o Rust usa de
  // verdade é o .rgba cru (tauri::image::Image::new espera RGBA decodificado,
  // não PNG; essa versão do Tauri não tem um construtor que decodifique PNG
  // a partir de bytes embutidos, só de arquivo em disco via feature opcional).
  writeFileSync(new URL(`./${name}.png`, import.meta.url), pngFromRGBA(SIZE, SIZE, rgba));
  writeFileSync(new URL(`./${name}.rgba`, import.meta.url), rgba);
  console.log(`gerado ${name}.png + ${name}.rgba`);
}
