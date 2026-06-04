import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { X } from 'lucide-react'
import AuthLayout from '@/components/AuthLayout'
import { listAccounts, removeAccount, activateAccount, getSafeRedirect, type Account } from '@/lib/auth'
import { ApiError } from '@/lib/api'

// Troca de conta para apps com SSO por cookie (*.santos-tech.com): escolher uma
// conta a torna a sessão ATIVA do navegador e volta pro app de origem.
export default function SwitchAccountPage() {
  const redirect = new URLSearchParams(window.location.search).get('redirect')
  const queryClient = useQueryClient()
  const [error, setError] = useState('')

  const { data, isLoading } = useQuery({ queryKey: ['accounts'], queryFn: listAccounts })

  function finish() {
    window.location.href = getSafeRedirect(redirect)
  }

  const activate = useMutation({
    mutationFn: activateAccount,
    onSuccess: finish,
    onError: (err) => {
      if (err instanceof ApiError && err.code === 'SESSION_EXPIRED') {
        queryClient.invalidateQueries({ queryKey: ['accounts'] })
        setError('Essa sessão expirou. Escolha outra conta ou entre novamente.')
        return
      }
      setError(err instanceof ApiError ? err.message : 'Erro ao trocar de conta')
    },
  })

  const remove = useMutation({
    mutationFn: removeAccount,
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['accounts'] }),
  })

  const loginUrl = `/?redirect=${encodeURIComponent(redirect ?? '')}`

  return (
    <AuthLayout>
      <h2 className="text-3xl font-bold text-[#0E2937] mb-1">Trocar de conta</h2>
      <p className="text-base text-[#496B84] mb-8">Escolha a conta que vai ficar ativa</p>

      {error && (
        <div className="mb-5 p-4 bg-red-50 border border-red-200 rounded-xl text-sm text-red-700">
          {error}
        </div>
      )}

      {isLoading ? (
        <p className="text-sm text-[#496B84]">Carregando contas...</p>
      ) : (
        <div className="flex flex-col gap-3">
          {(data?.accounts ?? []).map((account: Account) => (
            <div
              key={account.sessionId}
              className="group flex items-center gap-3 p-3 border border-gray-200 rounded-xl bg-white hover:bg-[#F5F8FA] hover:border-[#187ABF] transition-colors"
            >
              <button
                type="button"
                disabled={activate.isPending}
                onClick={() => {
                  setError('')
                  // conta já ativa: só volta pro app
                  if (account.active) {
                    finish()
                    return
                  }
                  activate.mutate(account.sessionId)
                }}
                className="flex flex-1 items-center gap-3 text-left disabled:opacity-60"
              >
                {account.avatarUrl ? (
                  <img src={account.avatarUrl} alt="" className="w-10 h-10 rounded-full object-cover" />
                ) : (
                  <div className="w-10 h-10 rounded-full bg-[#187ABF] text-white flex items-center justify-center font-semibold">
                    {account.name.charAt(0).toUpperCase()}
                  </div>
                )}
                <div className="min-w-0 flex-1">
                  <p className="font-semibold text-[#0E2937] truncate">{account.name}</p>
                  <p className="text-sm text-[#496B84] truncate">{account.email}</p>
                </div>
                {account.active && (
                  <span className="shrink-0 px-2 py-0.5 rounded-full bg-[#0DB88F]/10 text-xs font-semibold text-[#0DB88F]">
                    atual
                  </span>
                )}
              </button>
              <button
                type="button"
                aria-label={`Remover ${account.email}`}
                onClick={() => remove.mutate(account.sessionId)}
                className="p-1.5 rounded-lg text-gray-400 hover:text-red-600 hover:bg-red-50 opacity-0 group-hover:opacity-100 transition-opacity"
              >
                <X size={16} />
              </button>
            </div>
          ))}

          <a
            href={loginUrl}
            className="flex items-center justify-center gap-2 p-3 border border-dashed border-gray-300 rounded-xl text-sm font-medium text-[#187ABF] hover:bg-[#F5F8FA] transition-colors"
          >
            Usar outra conta
          </a>
        </div>
      )}
    </AuthLayout>
  )
}
