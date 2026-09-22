# Extensão de navegador — responder questões de múltipla escolha (Jev + fallback Claude)

**Data:** 2026-09-22 · **Projetos:** `apps/api-go` (rota nova + provider Jev), `apps/quiz-extension` (extensão Firefox/Zen, nova)

## Problema

Selecionar uma questão de múltipla escolha em qualquer página e receber a
alternativa correta ali mesmo, sem sair da página nem copiar/colar em outro
lugar.

O modelo **Jev** (TypeSafe AI) encaixa bem nessa forma: ele não gera texto,
devolve julgamento tipado a partir de um `state` + perguntas tipadas. Múltipla
escolha vira literalmente uma pergunta `type: "choice"`, com as alternativas
como `criteria`, e a resposta traz `probabilities` e `confidence` — dois sinais
que um LLM de texto não entrega de graça. É também muito mais barato e rápido
que um LLM de texto.

O que o Jev não faz é raciocinar em várias etapas. Daí o desenho em dois
estágios: Jev responde o caso comum, e um LLM de texto (Claude) entra só quando
o Jev demonstra insegurança.

## Objetivo (v1)

- Selecionar enunciado + alternativas em qualquer site, apertar `Alt+Q`, e ver
  um overlay com a alternativa, a confiança e a distribuição de probabilidades.
- Jev responde por padrão; Claude entra por escalonamento quando a confiança
  ficar baixa (limiar calibrado com dados reais, não chutado).
- Nenhuma credencial de API dentro do navegador: a extensão fala com o
  `api-go`, que já tem o API Router com as chaves cifradas e rotação.

### Fora da v1 (decidido, não esquecido)

Histórico de questões · cache de respostas repetidas · questão em imagem/print ·
"marque todas que se aplicam" · auto-detecção da questão sem seleção · Chrome.

## Decisões (alinhadas com o usuário, 2026-09-22)

1. **Jev com fallback para Claude**, não Jev sozinho nem Claude sozinho.
2. **Gatilho:** seleção manual do texto + atalho de teclado. Não auto-detectar a
   questão no DOM — heurística por site é o que mais quebra nesse tipo de
   extensão.
3. **Resposta:** overlay flutuante ancorado na seleção. Não side panel (o Zen é
   Firefox, não tem `chrome.sidePanel`) e não realce no DOM da página (voltaria
   o problema de casar texto com elemento).
4. **Navegador:** só Zen/Firefox (navegador padrão da máquina). WebExtension
   MV3 com `browser.*`.
5. **Credenciais:** via proxy próprio, nunca no cliente. Reaproveita o **API
   Router** que já existe no `api-go` em vez de criar serviço novo.
6. **Fallback pelo agent-go, não pelo API Router** (decidido em 2026-09-22,
   durante a implementação): o Claude Code já roda em container no ecossistema
   e o `api-go` já tem cliente para ele. Usar o API Router exigiria uma chave
   de API da Anthropic que não existe cadastrada, e ainda duplicaria um caminho
   que o Pós-aula já usa.
7. **Superfície:** rota nova dedicada `POST /quiz/answer`, não a rota
   `/auth/admin/api-router/providers/{id}/proxy`. A rota admin permite disparar
   *qualquer* requisição contra *qualquer* provider com as chaves da empresa;
   uma extensão que roda em todo site que o usuário abrir não deve alcançar
   isso. A rota nova só sabe responder questão.
7. **Auth:** login santos-tech dentro da extensão (access + refresh em
   `storage.local`, renovação automática). Não token estático em env (criaria
   segredo paralelo, sem revogação) nem JWT colado à mão (expira no pior
   momento).
8. **Parsing das alternativas no servidor**, não na extensão. É a parte que mais
   quebra em site novo; corrigir no servidor é um deploy, corrigir na extensão é
   reinstalar no meio do uso.
9. **Extensão mora no monorepo** (`apps/quiz-extension`), ao lado do `api-go`
   que a serve — mesmo padrão dos apps desktop (`hour-timer-app`,
   `santos-hub`), que também não têm deploy no Coolify.

## Arquitetura

```
Zen (extensão)                    api.santos-tech.com (api-go)            externos
─────────────                     ────────────────────────────            ────────
seleciona texto
   ↓ Alt+Q
content script capta
window.getSelection()
   ↓ runtime.sendMessage
background            POST /quiz/answer  (authGuard, Bearer)
   ─────────────────────────────►  1. separa enunciado × alternativas
                                   2. pergunta `choice` do Jev ───────► api.typesafe.ai
                                   3. confiança OK → devolve      ◄───   /v1/systemone
                                   4. confiança baixa → escala ───────► api.anthropic.com
   ◄─────────────────────────────  {answer, confidence, source}  ◄──     /v1/messages
overlay na seleção
```

### Responsabilidades

| Peça | Faz | Não faz |
|---|---|---|
| `content script` | lê a seleção, desenha o overlay, fecha no Esc | não fala com API, não conhece Jev |
| `background` | guarda tokens, faz o fetch, renova sessão | não interpreta a questão |
| `options page` | login, atalho, endpoint | nada em runtime |
| `POST /quiz/answer` | parse, Jev, decidir escalar, Claude, responder | não guarda histórico (v1) |
| API Router (existente) | chaves cifradas, rotação, failover | intocado — só ganha o provider Jev |

## Backend — `POST /quiz/answer`

Registro em `routes.go`:

```go
mux.HandleFunc("POST /quiz/answer", s.rateLimit(30, min, s.authGuard(s.handleQuizAnswer)))
```

`authGuard` (qualquer usuário logado), **não** `adminGuard`. Corpo limitado a
64KB via `http.MaxBytesReader`.

### Request

```json
{
  "raw": "Qual a capital da Mongólia?\nA) Astana\nB) Ulan Bator\nC) Bishkek",
  "question": "…",
  "options": { "A": "…", "B": "…" },
  "explain": false
}
```

- `raw` — caminho normal: o bloco cru selecionado. O servidor separa.
- `question` + `options` — caminho alternativo, se o cliente já separou. Quando
  presentes, o parser não roda.
- `explain` — força o escalonamento e pede justificativa.

Erro `INVALID_BODY` se `raw` e `options` vierem ambos vazios.

### Response 200

```json
{
  "answer": "B",
  "answerText": "Ulan Bator",
  "confidence": 0.93,
  "probabilities": { "A": 0.01, "B": 0.93, "C": 0.06 },
  "source": "jev",
  "escalated": false,
  "degraded": false,
  "reasoning": "",
  "parsed": { "question": "…", "options": { "A": "…", "B": "…" } },
  "timings": { "jevMs": 180, "claudeMs": 0, "totalMs": 195 }
}
```

- `source` — `"jev"` ou `"claude"`.
- `degraded` — `true` quando o Jev respondeu mas o escalonamento falhou; a
  resposta é o palpite de baixa confiança do Jev.
- `reasoning` — preenchido só quando `source == "claude"`.
- `parsed` — **existe por necessidade de diagnóstico**: sem ele não dá pra
  distinguir "o modelo errou" de "o parser cortou a alternativa D". O overlay
  mostra isso quando a resposta parecer estranha.

### Erros

| Código | HTTP | Quando |
|---|---|---|
| `INVALID_BODY` | 400 | JSON inválido, ou `raw` e `options` ambos vazios |
| `UNPARSEABLE` | 422 | menos de 2 alternativas reconhecidas |
| `NO_ACTIVE_KEYS` | 503 | provider sem chave ativa |
| `UPSTREAM_FAILED` | 502 | Jev e fallback falharam |
| `UPSTREAM_TIMEOUT` | 504 | estourou o orçamento total da rota (25s) |

Todos via `appErr`/`writeErr`, padrão do repositório.

### Orçamento de tempo

O API Router tem tetos próprios e largos demais para uso interativo:
`apiRouterHTTP.Timeout` de 30s por tentativa e `apiRouterRotationBudget` de 60s
de rotação (`apirouter.go`). Sem deadline próprio, uma questão poderia travar o
overlay por um minuto.

A rota impõe os seus, por `context.WithTimeout` — o `ctx` chega até o request do
provider, então o menor prevalece:

| Etapa | Deadline |
|---|---|
| chamada ao Jev | 8s |
| chamada ao Claude | 15s |
| requisição inteira | 25s |

O deadline curto no Jev existe para que sobre tempo de escalar: um Jev lento não
pode consumir o orçamento que o fallback vai precisar.

### Regra de escalonamento

Função pura, testável sem rede:

```
escala para o Claude se:
    confidence < QUIZ_MIN_CONFIDENCE        (default 0.75)
    ou (p1 − p2) < QUIZ_MIN_MARGIN          (default 0.15)
    ou explain == true
    ou o Jev falhou/deu timeout
```

Os dois defaults são provisórios: **os valores reais saem da Fase 0**.

Regra de degradação: se o Jev respondeu e o Claude falhou, devolve o Jev com
`degraded: true` em vez de erro. No meio de uma questão, um palpite com
confiança 0.6 vale mais que uma tela de erro.

### Parsing (server-side)

1. Normaliza quebras de linha e espaços, remove bullets.
2. Rótulo reconhecido no início da linha: `^\s*\(?([A-Ea-e]|[1-9])\s*[\)\.\-:]\s+`
3. A primeira linha rotulada fecha o enunciado; tudo antes é enunciado.
4. Exige 2 a 9 alternativas; fora disso → `UNPARSEABLE`. (O limite
   superior acompanha o rótulo de um dígito da regex acima.)
5. Sem nenhum rótulo: se houver uma linha terminada em `?` seguida de ≥2 linhas,
   ela vira enunciado e as seguintes viram alternativas com rótulos gerados.
6. **Os rótulos originais são preservados**: prova numerada de 1 a 5 devolve
   `"3"`, não `"C"`.

### Chamada ao Jev

Via `executeAPIRouterRequest` (ganha rotação de chave e failover de graça),
método `POST`, path `/v1/systemone`:

```json
{
  "state": "<enunciado>",
  "model": "jev-latest",
  "questions": {
    "resposta": {
      "type": "choice",
      "instructions": "Qual alternativa responde corretamente à questão?",
      "criteria": { "A": "<texto A>", "B": "<texto B>" }
    }
  }
}
```

### Chamada ao Claude

**Não passa pelo API Router.** O ecossistema já roda o Claude Code em container
(`apps/agent-go`), e o `api-go` já fala com ele por `claudeRaw`/`claudeRawCom`
(`agent_client.go`), que manda `{task:"raw", brief, model}` para
`POST {AGENT_URL}/claude/generate` e devolve **texto cru**. É assim que o
Pós-aula gera práticas hoje.

Consequências, todas simplificações:

- Nenhuma chave de API da Anthropic é necessária — o container roda com a
  assinatura da empresa. (O provider Anthropic do API Router está, de fato,
  sem chave nenhuma cadastrada.)
- Não existe `QUIZ_FALLBACK_PROVIDER_ID`; o modelo vem de `QUIZ_FALLBACK_MODEL`
  (`sonnet` por padrão, como o resto do ecossistema).
- `parseFallbackAnswer` recebe **texto**, não envelope nativo de provider, e
  portanto não precisa de adapter nem de `parseChatResponse`.

O prompt continua pedindo JSON estrito (`{"answer": "B", "reasoning": "…"}`), e
a extração continua pegando o **primeiro** objeto JSON completo do texto.

A chamada usa `claudeRawCom` com o orçamento de 15s do fallback, e não o teto
padrão de 2 minutos do cliente — um overlay não pode ficar dois minutos
"consultando".

### Configuração

| Env | Para quê |
|---|---|
| `QUIZ_JEV_PROVIDER_ID` | id do provider Jev no API Router (produção: 26) |
| `QUIZ_FALLBACK_MODEL` | modelo pedido ao agent-go (default `sonnet`) |
| `QUIZ_MIN_CONFIDENCE` | limiar de escalonamento (default 0.75) |
| `QUIZ_MIN_MARGIN` | margem mínima p1−p2 (default 0.15) |

Provider por **id via env**, e não busca por nome: `name` é campo editável na UI
admin, e renomear o provider quebraria a extensão em silêncio. Custo aceito:
trocar de provider exige mexer no env do Coolify.

**Nenhuma chave de API entra neste repositório.** As chaves do Jev e da
Anthropic são cadastradas pela UI admin do API Router, que já as cifra no banco.

### Ajuste necessário em `isNativeClient`

O login só devolve os tokens no corpo quando `isNativeClient(r)`, hoje definido
como `Origin == ""` (`handlers_auth.go:42`). Um fetch de extensão Firefox sempre
manda `Origin: moz-extension://<uuid>` — a extensão faria login com sucesso e
receberia apenas cookies `httpOnly` `SameSite=Lax`, que ela não pode ler nem
reenviar. Ficaria logada e sem token, permanentemente.

```go
func isNativeClient(r *http.Request) bool {
	o := r.Header.Get("Origin")
	return o == "" || strings.HasPrefix(o, "moz-extension://")
}
```

Seguro porque `Origin` é preenchido pelo navegador e uma página web não
consegue forjar `moz-extension://` — só código de extensão produz essa origem.
Não muda quem pode autenticar; muda o formato da resposta para um cliente que
já provou a senha.

**CORS não precisa de mudança:** no Firefox, `fetch` do background com
`host_permissions` para o host não passa por checagem de CORS — mesmo motivo de
o app Tauri já viver fora da allowlist (`server.go:181`).

## Extensão — `apps/quiz-extension`

```
apps/quiz-extension/
  manifest.json        MV3 Firefox + browser_specific_settings.gecko.id
  src/background.js    tokens, fetch, refresh, comando Alt+Q
  src/content.js       injetado sob demanda: lê a seleção, desenha o overlay
  src/overlay.css      estilos do card
  src/options.html
  src/options.js       login santos-tech, atalho, endpoint
```

Vanilla JS, sem bundler e sem dependências (~400 linhas no total). Carrega
direto por `about:debugging`. Se crescer, `bun build` entra depois sem refazer
nada.

### Permissões

```json
"permissions": ["storage", "activeTab", "scripting"],
"host_permissions": ["https://api.santos-tech.com/*"],
"commands": { "answer-selection": { "suggested_key": { "default": "Alt+Q" } } }
```

Sem `<all_urls>` e sem content script declarativo: o background injeta o script
com `scripting.executeScript` apenas quando o atalho é pressionado, usando o
acesso concedido pelo `activeTab` naquele gesto. **A extensão não lê nenhuma
página enquanto o usuário não mandar.**

Risco a validar na Fase 2: se o Zen/Firefox não conceder `activeTab` para um
comando customizado (a documentação garante para clique na ação e menu de
contexto; para atalho é menos explícito), o plano B é `host_permissions:
["<all_urls>"]` com a mesma UX e permissão mais ampla.

### Overlay

Renderizado em **Shadow DOM** com `all: initial` — sem isso o CSS da página
deforma o card, e sites de prova costumam ter CSS agressivo. Posicionado por
`getSelection().getRangeAt(0).getBoundingClientRect()`, com clamp para não sair
do viewport.

Estados: carregando → resposta (letra grande, barra de probabilidades, badge
`Jev` ou `Claude`, `reasoning` quando houver) → erro. Fecha com `Esc` ou clique
fora.

### Sessão

Login na options page (`POST /auth/login`) grava `accessToken` e `refreshToken`
em `browser.storage.local`. Toda chamada vai com `Authorization: Bearer`.

Em 401: o background chama `POST /auth/refresh` com o refresh token no Bearer e
repete a requisição **uma vez**. Dois cuidados obrigatórios:

- **Gravar o par novo antes de qualquer outra coisa.** O refresh é rotativo e
  fail-closed: reusar um token já rotacionado faz o servidor revogar *todas* as
  sessões do usuário (`handlers_auth.go:399`).
- **Nunca disparar dois refresh concorrentes** — fila de uma promessa só no
  background.

MFA não está implementado (a conta não usa). Se o login responder
`mfaRequired`, a extensão mostra "sua conta pede 2FA, a extensão precisa ser
atualizada" em vez de falhar em silêncio.

## Comportamento sob falha

| Situação | Comportamento |
|---|---|
| Jev inseguro | escala pro Claude, badge muda, +1–2s |
| Jev fora do ar | vai direto no Claude, transparente |
| Claude falha, Jev respondeu | palpite do Jev com aviso de baixa confiança |
| Ambos falham | erro curto no overlay, com o motivo |
| Token expirado | refresh silencioso e repete; só avisa se o refresh falhar |
| Seleção sem alternativas | "selecione o enunciado **e** as alternativas" |
| Sem seleção | não faz nada, nem chama a API |

## Testes

- Tabela de parsing com os formatos reais: `A)`, `(a`, `1.`, `3 -`, sem rótulo,
  alternativa de várias linhas, menos de 2 alternativas.
- Tabela da decisão de escalonamento (função pura, sem rede).
- Handler com provider falso cobrindo cada linha da tabela de erros.

Segue o padrão já existente em `apirouter_test.go`. A extensão não terá teste
automatizado na v1: o custo de montar harness de WebExtension não se paga em 400
linhas exercitadas a cada uso manual.

Antes de qualquer commit/deploy, o obrigatório do repositório: `gofmt -l .`
(vazio), `go vet ./...`, `go build ./...`, `go test ./...`.

## Fases

### Fase 0 — calibração (bloqueia todo o resto)

Cadastrar o Jev como provider e rodar ~20 questões reais pelo `/proxy` admin que
já existe, por script, medindo **taxa de acerto** e **distribuição de
`confidence`**.

Três saídas possíveis:

- **(a) Jev acerta bem** → segue o design como está; o limiar sai do dado real.
- **(b) Jev acerta mal, mas erra com confiança baixa** → a confiança dele é
  honesta; o projeto vira "Jev como filtro barato, Claude como resposta", e o
  limiar sobe.
- **(c) Jev acerta mal e erra com confiança alta** → **o Jev não serve para este
  caso de uso**. O certo então é a rota chamar o Claude direto, e o desvio pelo
  Jev não é construído.

Requer: ~20 questões reais do usuário e a API key do Jev (não está salva em
lugar nenhum — pedir na hora; ver `reference_typesafe_jev_api` na memória).

### Fase 1 — backend

Provider Jev cadastrado · `POST /quiz/answer` · ajuste do `isNativeClient` ·
testes · verificação obrigatória · deploy no Coolify.

### Fase 2 — extensão

Manifest, atalho, overlay, options/login, refresh. Carregar no Zen e testar.
Validar aqui a questão do `activeTab` por comando.

### Fase 3 — ajuste fino

Uso real; calibrar limiar e parsing com o que quebrar de verdade.

## Riscos registrados

1. **O Jev pode não servir.** Ele é um classificador de julgamento tipado, não
   uma base de conhecimento. Não há evidência de que acerte questões de prova.
   Mitigado pela Fase 0, que é barata e acontece antes do código de produção.
2. **`activeTab` por comando customizado** pode não valer no Zen. Plano B
   definido (`<all_urls>`), com custo de permissão mais ampla.
3. **Parsing em site novo.** Mitigado por ficar no servidor (corrigível por
   deploy) e por `parsed` voltar na resposta (diagnóstico imediato).
4. **Rotação de refresh token.** Um bug de concorrência no background derruba
   todas as sessões do usuário. Mitigado pela fila de refresh única e pela
   gravação-antes-de-tudo.
