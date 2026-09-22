# Quiz Jev — extensão

Seleciona a questão na página, `Alt+Q`, e a resposta aparece num overlay.

## Instalar (Zen / Firefox)

1. `about:debugging#/runtime/this-firefox`
2. "Carregar extensão temporária…" → escolher `manifest.json` desta pasta.
3. Abrir as opções da extensão e fazer login com a conta santos-tech.

Extensão temporária some ao fechar o navegador; recarregar pelo mesmo caminho.

## Arquitetura

A extensão não conhece Jev nem Claude: ela manda o texto selecionado para
`POST /quiz/answer` da `api-go`, que decide qual modelo responde. Nenhuma chave
de API vive aqui.

## `activeTab` vs `<all_urls>`

A injeção do content script usa `scripting.executeScript` sob demanda, com a
permissão `activeTab` concedida pelo próprio atalho (`Alt+Q`) — sem
`content_scripts` declarativo e sem `host_permissions` de página. Isso não foi
testado em navegador real nesta rodada (sem ambiente de browser disponível na
sessão que implementou); caso a injeção falhe com erro de permissão no console
do background, o plano B é acrescentar `"host_permissions": ["<all_urls>"]` ao
manifest e recarregar a extensão. Anotar aqui qual dos dois valeu depois do
teste manual.
