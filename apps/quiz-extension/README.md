# Quiz Jev — extensão

Seleciona a questão na página, `Alt+Q`, e a resposta aparece num overlay.
Questão sem alternativas (preencher lacuna, dissertativa) também é respondida —
nesse caso o card mostra a resposta em texto corrido, sem a letra em destaque.

## Captura de imagem

Quando a seleção encosta em algo visual (`img`, `canvas`, `svg`, `table`,
`math`, `picture`, `video`, `figure`, ou elemento com `background-image`),
maior que 40×40px, a extensão recorta essa região da tela e manda junto com
o texto — o backend usa o modelo com visão nesse caso.

**Sem conteúdo visual na seleção, o comportamento é idêntico ao de hoje:**
nenhum print é tirado, nenhuma requisição extra é feita. Esse filtro existe
de propósito — sem ele, toda questão custaria 5-8s de visão em vez dos
150-400ms de hoje, e a maioria das questões é puro texto.

`tabs.captureVisibleTab` só fotografa o que está visível na viewport. Se a
região a recortar (seleção + elementos visuais) não couber inteira na tela,
a extensão **não manda imagem cortada** — mostra um aviso pedindo para rolar
até a questão ficar totalmente visível e tentar de novo, sem gastar a
chamada ao backend.

Não foi preciso nenhuma permissão nova no `manifest.json` para isso:
`tabs.captureVisibleTab` aceita a mesma `activeTab` que já é concedida pelo
atalho `Alt+Q` (a mesma que permite o `scripting.executeScript` de hoje) —
não precisa de `tabs` nem de `<all_urls>`. **Vale para o Chrome também**: a
documentação do `chrome.tabs.captureVisibleTab` lista `activeTab` como uma
das permissões aceitas (junto de `<all_urls>` ou uma host permission
específica) — não é preciso declarar nada a mais no `manifest.json` só por
causa do Chrome. Isso não foi testado num Chrome real nesta tarefa (sem
navegador disponível no ambiente); é o comportamento documentado.

## Instalar

Funciona nos dois navegadores a partir dos mesmos arquivos — sem build, sem
passo de empacotamento (exceto o `.xpi` do Zen Flatpak, ver abaixo).

### Zen / Firefox

1. `about:debugging#/runtime/this-firefox`
2. "Carregar extensão temporária…" → escolher `manifest.json` desta pasta.
3. Abrir as opções da extensão (ver "Login", abaixo) e fazer login com a
   conta santos-tech.

Extensão temporária some ao fechar o navegador; recarregar pelo mesmo caminho.

### Chrome

1. `chrome://extensions`
2. Ativar "Modo do desenvolvedor" (canto superior direito).
3. "Carregar sem compactação" → escolher a **pasta** `apps/quiz-extension`
   inteira (não um arquivo).
4. Abrir as opções da extensão (ver "Login", abaixo) e fazer login com a
   conta santos-tech.

Extensão carregada assim também some ao fechar o Chrome (a não ser que você
a fixe); recarregar pelo mesmo caminho, ou usar o botão de recarregar no
card da extensão em `chrome://extensions` depois de editar os arquivos.

### Login

Feito na tela de **Opções** da extensão (ícone da extensão → "Opções", ou
o link que aparece no próprio `chrome://extensions`/`about:addons`), com
usuário e senha da conta santos-tech. Não precisa de console nem de nenhum
passo manual — é a mesma tela nos dois navegadores.

Se o login responder OK mas a sessão continuar "Sem sessão", a mensagem de
erro que aparece já diz o motivo mais provável: o servidor não reconheceu a
origem desta extensão (`moz-extension://…` ou `chrome-extension://…`) como
cliente nativo — nesse caso o ajuste é no `api-go` (`isNativeClient`), não
na extensão.

## Arquitetura

A extensão não conhece Jev nem Claude: ela manda o texto selecionado para
`POST /quiz/answer` da `api-go`, que decide qual modelo responde. Nenhuma chave
de API vive aqui.

## Firefox e Chrome, mesmos arquivos

Sem bundler, sem `webextension-polyfill` (dependência externa) e sem build —
só um shim de uma linha no topo de cada arquivo que usa a API do navegador:

```js
const api = globalThis.browser ?? globalThis.chrome;
```

Duas diferenças reais entre os navegadores, cobertas assim:

- **`manifest.json` → `background`:** declara `scripts` (Firefox) e
  `service_worker` (Chrome MV3) juntos, no mesmo objeto — cada navegador usa
  a chave que entende e ignora a outra.
- **Listeners de `runtime.onMessage` que respondem algo** (`ask`/`login`/
  `status`/`print`, em `src/background.js`): usam `sendResponse(...)` +
  `return true`, não `return algumaPromise.then(...)`. O padrão de Promise é
  só Firefox — no Chrome a resposta nunca chega e quem espera trava. O
  listener de `start` em `src/content.js` não chama `sendResponse` (o
  background só espera a entrega da mensagem), então não precisou dessa
  mudança — só do shim.

O recorte de imagem continua só no `content.js` (que tem DOM); o
`background.js`/service worker não usa `document`/`Image`/`canvas` — no
Chrome MV3 o service worker não tem DOM, então isso é obrigatório, não só
estilo.

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
