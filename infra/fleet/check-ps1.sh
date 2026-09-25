#!/usr/bin/env bash
# Valida sintaxe de .ps1 com o parser real do PowerShell (container oficial).
# Um erro de sintaxe no wnsh-loop.ps1 derruba o heartbeat da frota INTEIRA
# (ele se auto-atualiza pelo CDN) — rodar SEMPRE antes de publicar.
set -euo pipefail
[ $# -ge 1 ] || { echo "uso: $0 arquivo.ps1..." >&2; exit 2; }

# Copia todos os arquivos para um diretório temporário comum
tmpdir=$(mktemp -d)
tmpscript=$(mktemp)
ec=0
cleanup() {
  rm -rf "$tmpdir" "$tmpscript"
  exit $ec
}
trap cleanup EXIT

for f in "$@"; do
  cp "$f" "$tmpdir/$(basename "$f")"
done

cat > "$tmpscript" << 'PWSH_SCRIPT'
$bad=0
foreach ($f in $args) {
  $e=$null; $t=$null
  [void][System.Management.Automation.Language.Parser]::ParseFile("/w/$f",[ref]$t,[ref]$e)
  if ($e) { $bad=1; $e | ForEach-Object { "ERRO {0}:{1}: {2}" -f $f,$_.Extent.StartLineNumber,$_.Message } } else { "OK $f" }
}
exit $bad
PWSH_SCRIPT

# Extrai nomes dos arquivos
names=(); for f in "$@"; do names+=("$(basename "$f")"); done

docker run --rm -v "$tmpdir":/w:ro -v "$tmpscript":/tmp/check.ps1:ro mcr.microsoft.com/powershell:latest pwsh -NoProfile /tmp/check.ps1 "${names[@]}" || ec=$?
