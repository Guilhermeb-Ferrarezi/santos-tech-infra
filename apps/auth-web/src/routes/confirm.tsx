import { useEffect, useState } from 'react'
import { useMutation, useQuery } from '@tanstack/react-query'
import { Label } from '@radix-ui/react-label'
import AuthLayout from '@/components/AuthLayout'
import PasswordInput from '@/components/PasswordInput'
import OtpInput from '@/components/OtpInput'
import { me, sudoVerify, sendAccountEmailCode, getSafeRedirect } from '@/lib/auth'
import { ApiError } from '@/lib/api'

// Tela de sudo mode (estilo GitHub "Confirm access"): ações sensíveis nos apps
// redirecionam pra cá; o usuário confirma a identidade (MFA — método preferido
// primeiro — ou senha, quando sem MFA) e volta pro app com a sessão elevada 15min.
export default function ConfirmAccessPage() {
  const rawRedirect = new URLSearchParams(window.location.search).get('redirect') ?? undefined

  const { data, isLoading } = useQuery({ queryKey: ['me'], queryFn: me, retry: false })
  const user = data?.user

  const [code, setCode] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')
  const [emailSent, setEmailSent] = useState(false)
  const [useRecovery, setUseRecovery] = useState(false)
  // Método em uso na tela; inicia no preferido da conta quando o perfil chega.
  const [method, setMethod] = useState<'totp' | 'email' | null>(null)

  const mfaOn = user?.mfaEnabled ?? false
  const hasTotp = user?.mfaTotp ?? false
  const hasEmail = user?.mfaEmail ?? false

  const sendEmail = useMutation({
    mutationFn: sendAccountEmailCode,
    onSuccess: () => setEmailSent(true),
    onError: (err) => {
      setError(err instanceof ApiError ? err.message : 'Falha ao enviar o código')
    },
  })

  // Define o método inicial e, quando for email, já dispara o envio do código.
  useEffect(() => {
    if (!user || !mfaOn || method !== null) return
    const preferred = user.mfaMethod === 'email' && hasEmail ? 'email' : 'totp'
    setMethod(preferred)
    if (preferred === 'email') sendEmail.mutate()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [user])

  const verify = useMutation({
    mutationFn: () => sudoVerify(mfaOn ? { code } : { password }),
    onSuccess: () => {
      window.location.href = getSafeRedirect(rawRedirect)
    },
    onError: (err) => {
      setError(err instanceof ApiError ? err.message : 'Confirmação inválida')
    },
  })

  function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    setError('')
    verify.mutate()
  }

  if (isLoading) {
    return (
      <AuthLayout>
        <p className="text-base text-[#496B84]">Carregando…</p>
      </AuthLayout>
    )
  }

  if (!user) {
    // Sem sessão: volta pro login com o redirect preservado.
    window.location.href = '/?redirect=' + encodeURIComponent(rawRedirect ?? '')
    return null
  }

  return (
    <AuthLayout>
      <h2 className="text-3xl font-bold text-[#0E2937] mb-1">Confirme seu acesso</h2>
      <p className="text-base text-[#496B84] mb-6">
        Esta ação é sensível — confirme que é você antes de continuar.
      </p>

      {/* Card do usuário logado */}
      <div className="mb-6 flex items-center gap-3 rounded-xl border border-gray-200 bg-[#F5F8FA] p-4">
        {user.avatarUrl ? (
          <img src={user.avatarUrl} alt="" className="size-10 rounded-full object-cover" />
        ) : (
          <div className="grid size-10 place-items-center rounded-full bg-[#187ABF] text-white font-semibold">
            {(user.name || '?').trim().charAt(0).toUpperCase()}
          </div>
        )}
        <div className="min-w-0 leading-tight">
          <p className="truncate text-sm font-semibold text-[#0E2937]">{user.name}</p>
          <p className="truncate text-xs text-[#496B84]">{user.username ? '@' + user.username : user.email}</p>
        </div>
      </div>

      <form onSubmit={handleSubmit} className="flex flex-col gap-5">
        {mfaOn ? (
          <>
            <div className="flex flex-col gap-2">
              <Label className="text-sm font-semibold text-[#0E2937] text-center">
                {useRecovery ? 'Código de recuperação' : method === 'email' ? 'Código enviado por email' : 'Código do app autenticador'}
              </Label>
              {method === 'email' && emailSent && !useRecovery && (
                <p className="text-xs text-green-700 text-center">Código enviado para o seu email.</p>
              )}
              {useRecovery ? (
                <input
                  type="text"
                  autoFocus
                  value={code}
                  onChange={e => setCode(e.target.value.toUpperCase().replace(/[^0-9A-Z]/g, '').slice(0, 12))}
                  placeholder="XXXXXXXX"
                  required
                  className="w-full px-4 py-3.5 border border-gray-200 rounded-xl bg-[#F5F8FA] text-center text-xl font-mono tracking-widest focus:outline-none focus:border-[#187ABF] focus:bg-white transition-colors"
                />
              ) : (
                <OtpInput value={code} onChange={setCode} autoFocus />
              )}
            </div>

            {error && <p className="text-sm text-red-600">{error}</p>}

            <button
              type="submit"
              disabled={verify.isPending || code.length < 6}
              className="w-full py-3.5 bg-[#0DB88F] hover:bg-[#0aa37f] text-white text-base font-semibold rounded-xl transition-colors disabled:opacity-60"
            >
              {verify.isPending ? 'Confirmando...' : 'Confirmar'}
            </button>

            <div className="rounded-xl border border-gray-200 bg-[#F5F8FA] p-4 text-sm">
              <p className="font-semibold text-[#0E2937] mb-2">Está com problemas?</p>
              <ul className="flex flex-col gap-1.5">
                {method === 'totp' && hasEmail && (
                  <li>
                    <button
                      type="button"
                      onClick={() => { setError(''); setMethod('email'); sendEmail.mutate() }}
                      disabled={sendEmail.isPending}
                      className="text-[#187ABF] hover:underline disabled:opacity-60"
                    >
                      {sendEmail.isPending ? 'Enviando...' : 'Enviar um código por email'}
                    </button>
                  </li>
                )}
                {method === 'email' && (
                  <>
                    {hasTotp && (
                      <li>
                        <button
                          type="button"
                          onClick={() => { setError(''); setMethod('totp') }}
                          className="text-[#187ABF] hover:underline"
                        >
                          Usar o app autenticador
                        </button>
                      </li>
                    )}
                    <li>
                      <button
                        type="button"
                        onClick={() => { setError(''); sendEmail.mutate() }}
                        disabled={sendEmail.isPending}
                        className="text-[#187ABF] hover:underline disabled:opacity-60"
                      >
                        {sendEmail.isPending ? 'Reenviando...' : 'Reenviar o código'}
                      </button>
                    </li>
                  </>
                )}
                {!useRecovery ? (
                  <li>
                    <button
                      type="button"
                      onClick={() => { setCode(''); setError(''); setUseRecovery(true) }}
                      className="text-[#187ABF] hover:underline"
                    >
                      Usar código de recuperação
                    </button>
                  </li>
                ) : (
                  <li>
                    <button
                      type="button"
                      onClick={() => { setCode(''); setError(''); setUseRecovery(false) }}
                      className="text-[#187ABF] hover:underline"
                    >
                      Voltar para o código de 6 dígitos
                    </button>
                  </li>
                )}
              </ul>
            </div>
          </>
        ) : (
          <>
            <div className="flex flex-col gap-2">
              <Label htmlFor="password" className="text-sm font-semibold text-[#0E2937]">Confirme sua senha</Label>
              <PasswordInput
                id="password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                placeholder="••••••••"
                required
              />
            </div>

            {error && <p className="text-sm text-red-600">{error}</p>}

            <button
              type="submit"
              disabled={verify.isPending || !password}
              className="w-full py-3.5 bg-[#0DB88F] hover:bg-[#0aa37f] text-white text-base font-semibold rounded-xl transition-colors disabled:opacity-60"
            >
              {verify.isPending ? 'Confirmando...' : 'Confirmar'}
            </button>
          </>
        )}

        <p className="text-center text-xs text-[#496B84]">
          Após confirmar, você não será perguntado de novo pelos próximos 15 minutos.
        </p>
      </form>
    </AuthLayout>
  )
}
