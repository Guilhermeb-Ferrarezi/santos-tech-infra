import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { X } from 'lucide-react'
import AuthLayout from '@/components/AuthLayout'
import { listAccounts, removeAccount, confirmAuthorize, type Account } from '@/lib/auth'
import { ApiError } from '@/lib/api'

// Chooser de contas do fluxo OAuth ("Entrar com Santos Tech"), estilo Google:
// lista as contas logadas neste navegador; escolher uma emite o code e volta
// pro app. "Usar outra conta" vai pro login preservando o request_id.
export default function OAuthChoosePage() {
  const requestId = new URLSearchParams(window.location.search).get('request_id')
  const queryClient = useQueryClient()
  const [error, setError] = useState('')
  const [expired, setExpired] = useState(false)

  const { data, isLoading } = useQuery({
    queryKey: ['accounts'],
    queryFn: listAccounts,
    enabled: !!requestId,
  })

  const confirm = useMutation({
    mutationFn: (sessionId: string) => confirmAuthorize(requestId!, sessionId),
    onSuccess: (res) => { window.location.href = res.redirectTo },
    onError: (err) => {
      if (err instanceof ApiError && err.code === 'REQUEST_EXPIRED') {
        setExpired(true)
        return
      }
      if (err instanceof ApiError && err.code === 'SESSION_EXPIRED') {
        // sessão morreu entre o render e o clique: recarrega a lista (já podada)
        queryClient.invalidateQueries({ queryKey: ['accounts'] })
        setError('Essa sessão expirou. Escolha outra conta ou entre novamente.')
        return
      }
      setError(err instanceof ApiError ? err.message : 'Erro ao continuar')
    },
  })

  const remove = useMutation({
    mutationFn: removeAccount,
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['accounts'] }),
  })

  const loginUrl = `/?request_id=${encodeURIComponent(requestId ?? '')}`

  if (!requestId || expired) {
    return (
      <AuthLayout>
        <h2 className="text-3xl font-bold text-[#0E2937] mb-1">Sessão de autorização expirou</h2>
        <p className="text-base text-[#496B84] mb-8">
          Volte ao aplicativo de origem e tente entrar novamente.
        </p>
      </AuthLayout>
    )
  }

  return (
    <AuthLayout>
      <h2 className="text-3xl font-bold text-[#0E2937] mb-1">Escolha uma conta</h2>
      <p className="text-base text-[#496B84] mb-8">para continuar no aplicativo</p>

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
                disabled={confirm.isPending}
                onClick={() => { setError(''); confirm.mutate(account.sessionId) }}
                className="flex flex-1 items-center gap-3 text-left disabled:opacity-60"
              >
                {account.avatarUrl ? (
                  <img src={account.avatarUrl} alt="" className="w-10 h-10 rounded-full object-cover" />
                ) : (
                  <div className="w-10 h-10 rounded-full bg-[#187ABF] text-white flex items-center justify-center font-semibold">
                    {account.name.charAt(0).toUpperCase()}
                  </div>
                )}
                <div className="min-w-0">
                  <p className="font-semibold text-[#0E2937] truncate">{account.name}</p>
                  <p className="text-sm text-[#496B84] truncate">{account.email}</p>
                </div>
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
