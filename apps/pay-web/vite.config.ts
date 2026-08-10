import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import { sentryVitePlugin } from "@sentry/vite-plugin";

// https://vite.dev/config/
export default defineConfig({
  base: "/",
  plugins: [
    react(),
    tailwindcss(),
    // Sobe sourcemap pro Sentry (stacktrace legível em produção) e apaga os
    // .map do build final. Sem SENTRY_AUTH_TOKEN (dev local) não faz nada.
    sentryVitePlugin({
      org: "santos-techrp",
      project: "pay-web",
      authToken: process.env.SENTRY_AUTH_TOKEN,
      disable: !process.env.SENTRY_AUTH_TOKEN,
      sourcemaps: { filesToDeleteAfterUpload: ["**/*.map"] },
    }),
  ],
  build: {
    // Só gera sourcemap quando vai poder subir+apagar (token presente) — sem
    // isso, um .map ficaria público no dist (código fonte exposto).
    sourcemap: !!process.env.SENTRY_AUTH_TOKEN,
  },
});
