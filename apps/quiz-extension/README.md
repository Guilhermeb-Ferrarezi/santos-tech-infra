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

## `activeTab` confirmado suficiente — testado em 22/09/2026

A injeção do content script usa `scripting.executeScript` sob demanda, com a
permissão `activeTab` concedida pelo próprio atalho (`Alt+Q`) — sem
`content_scripts` declarativo e sem permissão de página.

**Testado no Zen 1.22b (Flatpak) e funciona.** O atalho concede `activeTab`, o
script é injetado e o card aparece. O `manifest.json` atual está correto; NÃO
adicione `<all_urls>`.

## Zen via Flatpak: carregue o `.xpi`, não o `manifest.json`

Armadilha específica deste setup, e custa meia hora se ninguém avisar.

Selecionar o `manifest.json` em `about:debugging` **carrega uma extensão
quebrada**: o portal de documentos do Flatpak expõe ao navegador apenas o
arquivo escolhido, então a pasta `src/` fica fora do sandbox e o background
falha com

    Loading failed for the <script> with source "moz-extension://.../src/background.js"

A extensão aparece na lista como se estivesse tudo certo — "Background script:
Running" — mas nada funciona.

Empacote a pasta inteira num arquivo só e carregue ele:

```bash
cd apps/quiz-extension && zip -r /tmp/quiz-jev.xpi manifest.json src/
```

Depois, em `about:debugging#/runtime/this-firefox` → "Carregar extensão
temporária…" → escolha `/tmp/quiz-jev.xpi`.

Firefox instalado nativamente (fora do Flatpak) não tem esse problema: ali o
`manifest.json` funciona direto.
