import { useState } from 'react'
import { useSearch, useNavigate } from '@tanstack/react-router'
import { useMutation, useQuery } from '@tanstack/react-query'
import { Label } from '@radix-ui/react-label'
import AuthLayout from '@/components/AuthLayout'
import GoogleButton from '@/components/GoogleButton'
import PasswordInput from '@/components/PasswordInput'
import { login, me, getSafeRedirect } from '@/lib/auth'
import { ApiError } from '@/lib/api'

export default function LoginPage() {
  const search = useSearch({ strict: false }) as { redirect?: string; reset?: string }
  const navigate = useNavigate()
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')

  // Se já autenticado (ex: volta do Google OAuth), redireciona
  useQuery({
    queryKey: ['me'],
    queryFn: me,
    retry: false,
    select: (data) => {
      if (data?.user) window.location.href = getSafeRedirect(search.redirect)
      return data
    },
  })

  const mutation = useMutation({
    mutationFn: () => login(email, password),
    onSuccess: () => {
      window.location.href = getSafeRedirect(search.redirect)
    },
    onError: (err) => {
      setError(err instanceof ApiError ? err.message : 'Erro ao entrar')
    },
  })

  function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    setError('')
    mutation.mutate()
  }

  return (
    <AuthLayout>
      {search.reset === 'success' && (
        <div className="mb-4 p-3 bg-green-50 border border-green-200 rounded-lg text-sm text-green-700">
          Senha redefinida com sucesso. Faça login abaixo.
        </div>
      )}

      <h2 className="text-2xl font-bold text-[#0E2937] mb-1">Entrar</h2>
      <p className="text-sm text-[#496B84] mb-6">Acesse sua conta Santos Tech</p>

      <form onSubmit={handleSubmit} className="flex flex-col gap-4">
        <div className="flex flex-col gap-1.5">
          <Label htmlFor="email" className="text-sm font-medium text-[#0E2937]">Email</Label>
          <input
            id="email"
            type="email"
            value={email}
            onChange={e => setEmail(e.target.value)}
            placeholder="seu@email.com"
            required
            className="w-full px-3 py-2.5 border border-gray-200 rounded-lg bg-[#F5F8FA] text-sm focus:outline-none focus:border-[#187ABF] focus:bg-white transition-colors"
          />
        </div>

        <div className="flex flex-col gap-1.5">
          <div className="flex justify-between items-center">
            <Label htmlFor="password" className="text-sm font-medium text-[#0E2937]">Senha</Label>
            <a href="/forgot-password" className="text-xs text-[#187ABF] hover:underline">
              Esqueci minha senha
            </a>
          </div>
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
          className="w-full py-2.5 bg-[#0DB88F] hover:bg-[#0aa37f] text-white font-semibold rounded-lg transition-colors disabled:opacity-60"
        >
          {mutation.isPending ? 'Entrando...' : 'Entrar'}
        </button>
      </form>

      <div className="flex items-center gap-3 my-4">
        <div className="flex-1 h-px bg-gray-200" />
        <span className="text-xs text-gray-400">ou</span>
        <div className="flex-1 h-px bg-gray-200" />
      </div>

      <GoogleButton />
    </AuthLayout>
  )
}
