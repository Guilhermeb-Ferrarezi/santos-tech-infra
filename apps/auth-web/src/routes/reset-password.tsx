import { useState } from 'react'
import { useMutation } from '@tanstack/react-query'
import { useSearch, useNavigate } from '@tanstack/react-router'
import { Label } from '@radix-ui/react-label'
import AuthLayout from '@/components/AuthLayout'
import PasswordInput from '@/components/PasswordInput'
import { resetPassword } from '@/lib/auth'
import { ApiError } from '@/lib/api'

export default function ResetPasswordPage() {
  const search = useSearch({ strict: false }) as { token?: string }
  const navigate = useNavigate()
  const [password, setPassword] = useState('')
  const [confirm, setConfirm] = useState('')
  const [error, setError] = useState('')

  const mutation = useMutation({
    mutationFn: () => resetPassword(search.token ?? '', password),
    onSuccess: () => {
      navigate({ to: '/', search: { reset: 'success' } })
    },
    onError: (err) => {
      setError(err instanceof ApiError ? err.message : 'Erro ao redefinir senha')
    },
  })

  if (!search.token) {
    return (
      <AuthLayout>
        <h2 className="text-3xl font-bold text-[#0E2937] mb-2">Link inválido</h2>
        <p className="text-base text-[#496B84] mb-5">
          Este link de recuperação é inválido ou expirou.
        </p>
        <a href="/forgot-password" className="text-base text-[#187ABF] hover:underline">
          Solicitar novo link →
        </a>
      </AuthLayout>
    )
  }

  function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    if (password !== confirm) {
      setError('As senhas não coincidem')
      return
    }
    setError('')
    mutation.mutate()
  }

  return (
    <AuthLayout>
      <h2 className="text-3xl font-bold text-[#0E2937] mb-1">Nova senha</h2>
      <p className="text-base text-[#496B84] mb-8">
        Digite e confirme sua nova senha.
      </p>

      <form onSubmit={handleSubmit} className="flex flex-col gap-5">
        <div className="flex flex-col gap-2">
          <Label htmlFor="password" className="text-sm font-semibold text-[#0E2937]">Nova senha</Label>
          <PasswordInput
            id="password"
            value={password}
            onChange={e => setPassword(e.target.value)}
            placeholder="Mínimo 8 caracteres"
            minLength={8}
            required
          />
        </div>

        <div className="flex flex-col gap-2">
          <Label htmlFor="confirm" className="text-sm font-semibold text-[#0E2937]">Confirmar senha</Label>
          <PasswordInput
            id="confirm"
            value={confirm}
            onChange={e => setConfirm(e.target.value)}
            placeholder="Repita a senha"
            required
          />
        </div>

        {error && <p className="text-sm text-red-600">{error}</p>}

        <button
          type="submit"
          disabled={mutation.isPending}
          className="w-full py-3.5 bg-[#0DB88F] hover:bg-[#0aa37f] text-white text-base font-semibold rounded-xl transition-colors disabled:opacity-60"
        >
          {mutation.isPending ? 'Salvando...' : 'Redefinir senha'}
        </button>
      </form>

      {mutation.isError && (
        <a href="/forgot-password" className="block text-center mt-5 text-base text-[#187ABF] hover:underline">
          Solicitar novo link →
        </a>
      )}
    </AuthLayout>
  )
}
