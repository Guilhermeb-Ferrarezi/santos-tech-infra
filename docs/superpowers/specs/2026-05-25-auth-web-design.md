# Auth Web — Design Spec
*Data: 2026-05-25*

## Contexto

Frontend centralizado de autenticação da santos-tech.com. Serve todos os projetos educacionais (portal-do-aluno e futuros). Contas são criadas pelos administradores — sem cadastro público.

## Decisões Técnicas

| Decisão | Escolha | Motivo |
|---------|---------|--------|
| Framework | React 19 + TypeScript | Padrão dos projetos sg |
| Build | Vite | Padrão dos projetos sg |
| CSS | TailwindCSS v4 | Padrão dos projetos sg |
| Roteamento | TanStack Router | Padrão do infra repo |
| Dados | TanStack Query | Padrão do infra repo |
| Componentes | Radix UI | Acessibilidade nativa |
| Ícones | lucide-react | Leve e moderno |
| Email reset | Resend SDK | TypeScript nativo, gratuito até 3k/mês |

## Layout

Split de duas colunas, sem scrollbar em telas ≥ 768px:

- **Esquerda (42% da largura):** painel de marca — gradiente azul marinho (`#0E2937`) para azul primário (`#187ABF`), logo da Santos Tech, nome e tagline
- **Direita (58%):** fundo branco, formulário centralizado verticalmente
- **Mobile (< 768px):** painel de marca vira header compacto, formulário ocupa a tela toda

## Telas

### 1. Login (`/`)

Formulário com:
- Campo email
- Campo senha (toggle mostrar/ocultar)
- Botão "Entrar" (verde-teal `#0DB88F`)
- Separador "ou"
- Botão "Entrar com Google" (redirect para `GET /auth/google`)
- Link "Esqueci minha senha" → `/forgot-password`

Comportamento pós-login:
- Lê parâmetro `?redirect=url` da query string
- Se presente e for URL válida do domínio `santos-tech.com` → redireciona para lá
- Caso contrário → redireciona para `VITE_REDIRECT_DEFAULT` (variável de ambiente)

### 2. Recuperação de senha (`/forgot-password`)

**Etapa 1 — Solicitar link:**
- Campo email
- Botão "Enviar link de recuperação"
- POST `→ /auth/forgot-password`
- Após envio (sucesso ou email não encontrado): mostra mensagem genérica "Se este email estiver cadastrado, você receberá um link em instantes." (evita enumeração de emails)

**Etapa 2 — Nova senha (`/reset-password?token=...`):**
- Campo nova senha (mínimo 8 caracteres)
- Campo confirmação de senha
- Botão "Redefinir senha"
- POST `→ /auth/reset-password` com `{ token, newPassword }`
- Sucesso: redireciona para `/` com mensagem de confirmação
- Token inválido/expirado: mensagem de erro + link para solicitar novo

## Backend — Novos Endpoints

### POST /auth/forgot-password

Body: `{ email: string }`

- Busca usuário por email
- Se encontrado: gera token aleatório (32 bytes), salva hash em `password_resets`, envia email via Resend
- Resposta sempre `200 { message: "ok" }` (não revela se email existe)
- Token expira em 1 hora

### POST /auth/reset-password

Body: `{ token: string; newPassword: string }`

- Valida token contra hash em `password_resets`
- Verifica expiração
- Atualiza `password_hash` do usuário
- Deleta o token usado
- Resposta `200` ou `400 INVALID_TOKEN`

### Schema PostgreSQL (nova tabela)

```sql
password_resets (
  id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id    uuid REFERENCES users(id) ON DELETE CASCADE,
  token_hash text NOT NULL UNIQUE,
  expires_at timestamp NOT NULL,
  created_at timestamp DEFAULT now()
)
```

### Variáveis de ambiente novas

```env
# Backend (apps/api)
RESEND_API_KEY=re_...
RESEND_FROM_EMAIL=noreply@santos-tech.com

# Frontend (apps/auth-web)
VITE_API_URL=http://localhost:3000
VITE_REDIRECT_DEFAULT=http://localhost:5173
```

## Estrutura de Arquivos

```
apps/auth-web/
├── package.json
├── tsconfig.json
├── vite.config.ts
├── index.html
├── public/
│   └── logo.png              ← movido de apps/public/
└── src/
    ├── main.tsx
    ├── styles.css
    ├── lib/
    │   ├── api.ts            ← fetch wrapper (base URL + credentials)
    │   └── auth.ts           ← funções: login, logout, me, forgotPassword, resetPassword
    ├── components/
    │   ├── BrandPanel.tsx    ← painel esquerdo (logo + texto)
    │   ├── AuthLayout.tsx    ← split layout wrapper
    │   ├── GoogleButton.tsx  ← botão estilizado Google
    │   └── PasswordInput.tsx ← input com toggle show/hide
    └── routes/
        ├── __root.tsx
        ├── index.tsx         ← tela de login
        ├── forgot-password.tsx
        └── reset-password.tsx
```

## Identidade Visual

- Painel esquerdo: `background: linear-gradient(160deg, #0E2937, #187ABF)`
- Botão principal: `#0DB88F` (verde-teal)
- Botão Google: branco com borda `#e0e0e0`, ícone G colorido
- Inputs: fundo `#F5F8FA`, borda `#e0e0e0` → focus `#187ABF`
- Títulos: `#0E2937` (azul marinho)
- Texto secundário: `#496B84`
- Logo: `public/logo.png` no painel esquerdo

## Verificação

1. `bun run dev` no auth-web sobe em `localhost:5174`
2. `POST /auth/login` com credenciais válidas → redireciona conforme `?redirect=`
3. Botão Google → redireciona para `accounts.google.com`
4. `POST /auth/forgot-password` → email chega via Resend
5. Link do email abre `/reset-password?token=...` → nova senha funciona
6. Mobile (< 768px): layout responsivo sem overflow horizontal
