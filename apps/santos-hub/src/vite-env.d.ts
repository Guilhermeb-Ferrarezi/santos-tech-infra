/// <reference types="vite/client" />

interface ImportMetaEnv {
  // PAT do Hub exigido por GET /public/downloads (ver lib/api.ts). Ausente no
  // build = catálogo responde 401.
  readonly VITE_SANTOS_HUB_TOKEN?: string;
}

interface ImportMeta {
  readonly env: ImportMetaEnv;
}
