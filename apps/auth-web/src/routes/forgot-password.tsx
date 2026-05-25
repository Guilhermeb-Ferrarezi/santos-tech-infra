import { useState } from 'react'
import { useMutation } from '@tanstack/react-query'
import { Label } from '@radix-ui/react-label'
import AuthLayout from '@/components/AuthLayout'
import { forgotPassword } from '@/lib/auth'

export default function ForgotPasswordPage() {
  const [email, setEmail] = useState('')
  const [sent, setSent] = useState(false)

  const mutation = useMutation({
    mutationFn: () => forgotPassword(email),
    onSuccess: () => setSent(true),
    onError: () => setSent(true), // Sempre mostra sucesso para não revelar se email existe
  })

  if (sent) {
    return (
      <AuthLayout>
        <h2 className="text-2xl font-bold text-[#0E2937] mb-2">Verifique seu email</h2>
        <p className="text-sm text-[#496B84] mb-6">
          Se este email estiver cadastrado, você receberá um link de recuperação em instantes.
          Verifique também sua caixa de spam.
        </p>
        <a href="/" className="text-sm text-[#187ABF] hover:underline">
          ← Voltar para o login
        </a>
      </AuthLayout>
    )
  }

  return (
    <AuthLayout>
      <h2 className="text-2xl font-bold text-[#0E2937] mb-1">Recuperar senha</h2>
      <p className="text-sm text-[#496B84] mb-6">
        Digite seu email e enviaremos um link para criar uma nova senha.
      </p>

      <form
        onSubmit={e => { e.preventDefault(); mutation.mutate() }}
        className="flex flex-col gap-4"
      >
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

        <button
          type="submit"
          disabled={mutation.isPending}
          className="w-full py-2.5 bg-[#0DB88F] hover:bg-[#0aa37f] text-white font-semibold rounded-lg transition-colors disabled:opacity-60"
        >
          {mutation.isPending ? 'Enviando...' : 'Enviar link de recuperação'}
        </button>
      </form>

      <a href="/" className="block text-center mt-4 text-sm text-[#187ABF] hover:underline">
        ← Voltar para o login
      </a>
    </AuthLayout>
  )
}
