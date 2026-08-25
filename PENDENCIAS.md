# Pendências

Itens adiados **de propósito** para não travar o projeto. Voltar e fechar.
(Convenção do Modo Henrique, em `~/.claude/CLAUDE.md`.)

## Aberto

- [ ] **Endpoints de `/links` e `/public/links` (vitrine de links da bio) sem documentação em `docs/openapi.yaml` e no `llms.txt` central** — achado em 2026-08-25 ao adicionar o campo `titleGradient` no `LinkShowcaseItem`. Os handlers existem desde a feat `a570b28` (2026-08-08) mas nunca foram documentados no contrato — não é regressão desta sessão, é lacuna pré-existente (mesmo padrão falta em outros domínios pequenos do repo, ex. `/glossary`). Documentar retroativamente os 4 endpoints (`GET/POST /links`, `PUT/DELETE /links/{id}`, `GET/PUT /links/settings`, `GET /public/links`) ficou fora do escopo da tarefa (era só adicionar 1 campo booleano) pra não inflar o PR. Retomar assim: escrever os paths no `docs/openapi.yaml` seguindo o padrão de outro domínio já documentado, incluindo `titleGradient` nos schemas, e replicar um resumo no `llms.txt` (`apps/api-go/llms.txt`).
