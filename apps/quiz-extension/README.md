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

## `activeTab` vs `<all_urls>` — PENDENTE de teste manual em navegador

A injeção do content script usa `scripting.executeScript` sob demanda, com a
permissão `activeTab` concedida pelo próprio atalho (`Alt+Q`) — sem
`content_scripts` declarativo e sem `host_permissions` de página. Isso ainda
**não foi testado em navegador real** (sem Firefox/Zen disponível nas sessões
que implementaram e revisaram esta extensão até agora).

Quem for testar: carregue a extensão (`about:debugging#/runtime/this-firefox`),
selecione uma questão e aperte `Alt+Q`. Duas saídas possíveis:

- **Funcionou** (card apareceu): `activeTab` basta. Não mexer em nada — o
  `manifest.json` atual já está correto. Apagar esta seção ou trocar o título
  por "`activeTab` confirmado suficiente — testado em DD/MM/AAAA".
- **Falhou** com erro de permissão no console do background (botão
  "Inspecionar" em `about:debugging`): trocar, em `manifest.json`, a linha

  ```
  "host_permissions": ["https://api.santos-tech.com/*"],
  ```

  por

  ```
  "host_permissions": ["https://api.santos-tech.com/*", "<all_urls>"],
  ```

  recarregar a extensão e testar de novo. Se resolver, manter essa linha e
  atualizar esta seção anotando que o plano B foi necessário.
