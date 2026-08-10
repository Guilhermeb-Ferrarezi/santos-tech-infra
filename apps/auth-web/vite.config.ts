import { defineConfig, loadEnv } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import { sentryVitePlugin } from '@sentry/vite-plugin'
import { resolve } from 'path'

export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), '')
  return {
    plugins: [
      react(),
      tailwindcss(),
      // Sobe sourcemap pro Sentry (stacktrace legível em produção) e apaga os
      // .map do build final. Sem SENTRY_AUTH_TOKEN (dev local) não faz nada.
      sentryVitePlugin({
        org: 'santos-techrp',
        project: 'auth-web',
        authToken: process.env.SENTRY_AUTH_TOKEN,
        disable: !process.env.SENTRY_AUTH_TOKEN,
        sourcemaps: { filesToDeleteAfterUpload: ['**/*.map'] },
      }),
    ],
    build: {
      // Só gera sourcemap quando vai poder subir+apagar (token presente) —
      // sem isso, um .map ficaria público no dist (código fonte exposto).
      sourcemap: !!process.env.SENTRY_AUTH_TOKEN,
    },
    resolve: {
      alias: { '@': resolve(import.meta.dirname, './src') },
    },
    server: {
      port: 5174,
      proxy: {
        '/auth': {
          target: env.VITE_API_URL || 'http://localhost:3000',
          changeOrigin: true,
        },
      },
    },
  }
})
