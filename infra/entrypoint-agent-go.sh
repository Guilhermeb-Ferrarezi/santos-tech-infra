#!/bin/sh
set -e

# O volume persistente cobre $HOME/.claude, mas o Claude Code CLI grava seu
# config vivo em $HOME/.claude.json (fora dessa pasta). A cada restart do
# container esse arquivo some e só sobra o backup automático dentro de
# .claude/backups/ — sem isso o `claude` CLI falha com exit status 1.
if [ ! -f "$HOME/.claude.json" ] && [ -d "$HOME/.claude/backups" ]; then
  latest=$(ls -t "$HOME/.claude/backups" 2>/dev/null | head -n1)
  if [ -n "$latest" ]; then
    cp "$HOME/.claude/backups/$latest" "$HOME/.claude.json"
    echo "entrypoint-agent-go: restaurado .claude.json a partir do backup $latest"
  fi
fi

exec agent-go "$@"
