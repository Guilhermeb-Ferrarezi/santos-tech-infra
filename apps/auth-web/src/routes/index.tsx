import { useState } from 'react'
import { useSearch, useNavigate } from '@tanstack/react-router'
import { useMutation, useQuery } from '@tanstack/react-query'
import { Label } from '@radix-ui/react-label'
import AuthLayout from '@/components/AuthLayout'
import GoogleButton from '@/components/GoogleButton'
import PasswordInput from '@/components/PasswordInput'
import { login, me, getSafeRedirect, verifyMfa, sendMfaEmail } from '@/lib/auth'
import { ApiError } from '@/lib/api'

export default function LoginPage() {
  const search = useSearch({ strict: false }) as { redirect?: string; reset?: string; error?: string }
  const navigate = useNavigate()

  // TanStack Router v1 usa JSON.parse nos search params, então URLs padrão
  // (?redirect=https://...) não chegam via useSearch. Lê direto da URL como fallback.
  const rawRedirect = search.redirect ?? new URLSearchParams(window.location.search).get('redirect') ?? undefined
  const [identifier, setIdentifier] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')
  // 2º passo do MFA: quando setado, a tela troca pro formulário do código.
  const [mfaChallenge, setMfaChallenge] = useState<string | null>(null)
  const [code, setCode] = useState('')
  const [emailSent, setEmailSent] = useState(false)

  useQuery({
    queryKey: ['me'],
    queryFn: me,
    retry: false,
    select: (data) => {
      if (data?.user) window.location.href = getSafeRedirect(rawRedirect)
      return data
    },
  })

  function finishLogin() {
    window.location.href = getSafeRedirect(rawRedirect)
  }

  const mutation = useMutation({
    mutationFn: () => login(identifier, password),
    onSuccess: (res) => {
      // MFA ativo: não há sessão ainda, vamos pro passo do código.
      if ('mfaRequired' in res) {
        setError('')
        setMfaChallenge(res.challenge)
        return
      }
      finishLogin()
    },
    onError: (err) => {
      setError(err instanceof ApiError ? err.message : 'Erro ao entrar')
    },
  })

  const verify = useMutation({
    mutationFn: () => verifyMfa(mfaChallenge!, code),
    onSuccess: finishLogin,
    onError: (err) => {
      setError(err instanceof ApiError ? err.message : 'Código inválido')
    },
  })

  const sendEmail = useMutation({
    mutationFn: () => sendMfaEmail(mfaChallenge!),
    onSuccess: () => setEmailSent(true),
    onError: (err) => {
      setError(err instanceof ApiError ? err.message : 'Falha ao enviar o código')
    },
  })

  function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    setError('')
    mutation.mutate()
  }

  function handleVerify(e: React.FormEvent) {
    e.preventDefault()
    setError('')
    verify.mutate()
  }

  function backToLogin() {
    setMfaChallenge(null)
    setCode('')
    setEmailSent(false)
    setError('')
  }

  return (
    <AuthLayout>
      {search.reset === 'success' && (
        <div className="mb-5 p-4 bg-green-50 border border-green-200 rounded-xl text-sm text-green-700">
          Senha redefinida com sucesso. Faça login abaixo.
        </div>
      )}

      {search.error === 'account_not_found' && (
        <div className="mb-5 p-4 bg-red-50 border border-red-200 rounded-xl text-sm text-red-700">
          Nenhuma conta encontrada com este email Google. Entre com email e senha ou solicite acesso ao administrador.
        </div>
      )}
      {search.error === 'oauth_denied' && (
        <div className="mb-5 p-4 bg-yellow-50 border border-yellow-200 rounded-xl text-sm text-yellow-700">
          Autenticação com Google cancelada.
        </div>
      )}
      {search.error === 'oauth_failed' && (
        <div className="mb-5 p-4 bg-red-50 border border-red-200 rounded-xl text-sm text-red-700">
          Falha ao conectar com o Google. Tente novamente.
        </div>
      )}

      {mfaChallenge ? (
        <>
          <h2 className="text-3xl font-bold text-[#0E2937] mb-1">Verificação em duas etapas</h2>
          <p className="text-base text-[#496B84] mb-8">
            Digite o código do seu app autenticador — ou use o enviado por email ou um código de
            recuperação.
          </p>

          {emailSent && (
            <div className="mb-5 p-4 bg-green-50 border border-green-200 rounded-xl text-sm text-green-700">
              Enviamos um código para o seu email.
            </div>
          )}

          <form onSubmit={handleVerify} className="flex flex-col gap-5">
            <div className="flex flex-col gap-2">
              <Label htmlFor="code" className="text-sm font-semibold text-[#0E2937]">Código de verificação</Label>
              <input
                id="code"
                inputMode="numeric"
                autoComplete="one-time-code"
                autoFocus
                value={code}
                onChange={e => setCode(e.target.value.toUpperCase().replace(/[^0-9A-Z]/g, '').slice(0, 8))}
                placeholder="000000"
                required
                className="w-full px-4 py-3.5 border border-gray-200 rounded-xl bg-[#F5F8FA] text-center text-2xl font-mono tracking-[0.4em] focus:outline-none focus:border-[#187ABF] focus:bg-white transition-colors"
              />
            </div>

            {error && <p className="text-sm text-red-600">{error}</p>}

            <button
              type="submit"
              disabled={verify.isPending || code.length < 6}
              className="w-full py-3.5 bg-[#0DB88F] hover:bg-[#0aa37f] text-white text-base font-semibold rounded-xl transition-colors disabled:opacity-60"
            >
              {verify.isPending ? 'Verificando...' : 'Verificar'}
            </button>

            <button
              type="button"
              onClick={() => { setError(''); sendEmail.mutate() }}
              disabled={sendEmail.isPending}
              className="text-center text-sm text-[#187ABF] hover:underline disabled:opacity-60"
            >
              {sendEmail.isPending ? 'Enviando...' : 'Enviar código por email'}
            </button>

            <button
              type="button"
              onClick={backToLogin}
              className="text-center text-sm text-gray-500 hover:underline"
            >
              Voltar ao login
            </button>
          </form>
        </>
      ) : (
      <>
      <h2 className="text-3xl font-bold text-[#0E2937] mb-1">Entrar</h2>
      <p className="text-base text-[#496B84] mb-8">Acesse sua conta Santos Tech</p>

      <form onSubmit={handleSubmit} className="flex flex-col gap-5">
        <div className="flex flex-col gap-2">
          <Label htmlFor="identifier" className="text-sm font-semibold text-[#0E2937]">Email ou nome de usuário</Label>
          <input
            id="identifier"
            type="text"
            value={identifier}
            onChange={e => setIdentifier(e.target.value)}
            placeholder="seu@email.com ou @username"
            required
            className="w-full px-4 py-3.5 border border-gray-200 rounded-xl bg-[#F5F8FA] text-base focus:outline-none focus:border-[#187ABF] focus:bg-white transition-colors"
          />
        </div>

        <div className="flex flex-col gap-2">
          <Label htmlFor="password" className="text-sm font-semibold text-[#0E2937]">Senha</Label>
          <PasswordInput
            id="password"
            value={password}
            onChange={e => setPassword(e.target.value)}
            placeholder="••••••••"
            required
          />
        </div>

        {error && <p className="text-sm text-red-600">{error}</p>}

        <button
          type="submit"
          disabled={mutation.isPending}
          className="w-full py-3.5 bg-[#0DB88F] hover:bg-[#0aa37f] text-white text-base font-semibold rounded-xl transition-colors disabled:opacity-60"
        >
          {mutation.isPending ? 'Entrando...' : 'Entrar'}
        </button>

        <a href="/forgot-password" className="text-center text-sm text-[#187ABF] hover:underline">
          Esqueci minha senha
        </a>
      </form>

      <div className="flex items-center gap-3 my-5">
        <div className="flex-1 h-px bg-gray-200" />
        <span className="text-sm text-gray-400">ou</span>
        <div className="flex-1 h-px bg-gray-200" />
      </div>

      <GoogleButton />
      </>
      )}
    </AuthLayout>
  )
}
