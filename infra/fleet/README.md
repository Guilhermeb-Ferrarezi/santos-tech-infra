# Scripts da frota (PCs do laboratório)

Fonte da verdade dos scripts que rodam como SYSTEM nos PCs Windows.

| Arquivo | Onde roda | Publicação |
|---|---|---|
| `wnsh-loop.ps1` | serviço `WinNetSvcHost` (NSSM → `svchelper.exe`), `C:\ProgramData\SantosTech\` | R2 `downloads/wnsh-loop.ps1` — os PCs se auto-atualizam a cada volta (~60s) comparando sha256 |
| `collect-apps.ps1` | tarefa `/it` temporária disparada pelo loop | copiado pelo instalador |
| `fix-ssh-santos-fleet.ps1` | uma vez por PC, via canal de comando (SYSTEM) | manual |

## Publicar o `wnsh-loop.ps1`

**Nota:** O arquivo versionado (`infra/fleet/wnsh-loop.ps1`) contém um placeholder
`__TS_AUTHKEY__` no lugar da chave Tailscale real. A chave é injetada **apenas no momento
da publicação**, a partir da variável de ambiente `$TS_AUTHKEY` (obtida do console de admin
do Tailscale ou da cópia atual do CDN — nunca commitada).

1. `infra/fleet/check-ps1.sh infra/fleet/wnsh-loop.ps1` — tem que dar `OK`.
2. **Canário primeiro** (gazake-1, que tem SSH de fallback): gere a variante com
   `$updateUrl` apontando pra `downloads/wnsh-loop-canary.ps1`, suba nessa chave e
   copie pro PC (`scp` como `santos-fleet`). Confirme heartbeat + um comando.
3. Suba o arquivo final em `downloads/wnsh-loop.ps1` e o MESMO conteúdo em
   `downloads/wnsh-loop-canary.ps1` (o canário volta pro canal normal sozinho).

Upload com injeção de chave (token e chave Tailscale na memória, nunca no repo):
    : "${CF_R2_TOKEN:?defina CF_R2_TOKEN}"
    : "${TS_AUTHKEY:?defina TS_AUTHKEY}"
    sed "s/__TS_AUTHKEY__/$TS_AUTHKEY/" infra/fleet/wnsh-loop.ps1 > /tmp/claude-1000/wnsh-loop.pub.ps1
    curl -X PUT -H "Authorization: Bearer $CF_R2_TOKEN" -H "Content-Type: text/plain" \
      --data-binary @/tmp/claude-1000/wnsh-loop.pub.ps1 \
      "https://api.cloudflare.com/client/v4/accounts/ac17de2ec0cc4e48dc21ddf2eeb5a879/r2/buckets/santos-tech/objects/downloads/wnsh-loop.ps1"

## Risco conhecido

Quem escreve em `downloads/wnsh-loop.ps1` no R2 executa como SYSTEM em toda a frota.
A chave Tailscale é embutida no arquivo publicado (público, sem autenticação), permitindo
que qualquer PC autenticado à rede Tailscale se una via esse token — **controle rigoroso
de acesso ao console Tailscale é essencial**.
