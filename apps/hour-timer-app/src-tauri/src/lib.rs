use tauri::{
    image::Image,
    menu::{CheckMenuItem, Menu, MenuItem, PredefinedMenuItem, Submenu},
    tray::{MouseButton, MouseButtonState, TrayIcon, TrayIconBuilder, TrayIconEvent},
    AppHandle, LogicalPosition, Manager, Position, WebviewUrl, WebviewWindowBuilder, WindowEvent,
};
use tauri_plugin_autostart::{MacosLauncher, ManagerExt};
use tauri_plugin_store::StoreExt;

// Bolinhas de status geradas por icons/gen-tray-dots.mjs (embutidas no
// binário em tempo de compilação — sem depender de arquivo solto instalado).
// .rgba = RGBA cru 32x32 (não .png): Image::new espera pixels já decodificados
// — essa versão do Tauri não tem construtor que decodifique PNG a partir de
// bytes embutidos, só via arquivo em disco (feature opcional não habilitada).
const TRAY_ICON_SIZE: u32 = 32;
const TRAY_ICON_GRAY: &[u8] = include_bytes!("../icons/tray-gray.rgba");
const TRAY_ICON_GREEN: &[u8] = include_bytes!("../icons/tray-green.rgba");
const TRAY_ICON_AMBER: &[u8] = include_bytes!("../icons/tray-amber.rgba");
const TRAY_ICON_RED: &[u8] = include_bytes!("../icons/tray-red.rgba");

const OVERLAY_CORNER_KEY: &str = "overlayCorner";

const OVERLAY_LABEL: &str = "overlay";
const OVERLAY_WIDTH: f64 = 220.0;
const OVERLAY_HEIGHT: f64 = 70.0;

const TOAST_LABEL: &str = "toast";
const TOAST_WIDTH: f64 = 340.0;
const TOAST_HEIGHT: f64 = 120.0;

// Posição num CANTO da ÁREA ÚTIL do monitor (work_area exclui a barra de
// tarefas — usar monitor.size() aqui fazia a janela nascer atrás da barra,
// já que size() é a resolução cheia). work_area já vem em pixels físicos,
// mesma unidade de scale_factor()/inner_size(), então a mesma conta de
// escala do resto do cálculo se aplica igual. corner: "top-left" |
// "top-right" | "bottom-left" | "bottom-right" (default se não reconhecido).
fn corner_position_in_work_area(app: &AppHandle, width: f64, height: f64, margin: f64, corner: &str) -> (f64, f64) {
    app.primary_monitor()
        .ok()
        .flatten()
        .map(|m| {
            let scale = m.scale_factor();
            let area = m.work_area();
            let left = area.position.x as f64 / scale;
            let top = area.position.y as f64 / scale;
            let right = left + area.size.width as f64 / scale;
            let bottom = top + area.size.height as f64 / scale;
            match corner {
                "top-left" => (left + margin, top + margin),
                "top-right" => (right - width - margin, top + margin),
                "bottom-left" => (left + margin, bottom - height - margin),
                _ => (right - width - margin, bottom - height - margin),
            }
        })
        .unwrap_or((900.0, 700.0))
}

// Canto salvo pelo usuário (ver comando set_overlay_position) — "bottom-right"
// se nunca configurado.
fn read_overlay_corner(app: &AppHandle) -> String {
    app.store("config.json")
        .ok()
        .and_then(|s| s.get(OVERLAY_CORNER_KEY))
        .and_then(|v| v.as_str().map(str::to_string))
        .unwrap_or_else(|| "bottom-right".to_string())
}

// Janela flutuante com o tempo, no canto salvo — criada já no boot (escondida,
// mesmo padrão do ensure_toast_window logo abaixo) e nunca mais recriada.
// Criar uma WebviewWindow DE DENTRO de um #[tauri::command] (era o que
// set_overlay_position fazia antes, sob demanda) trava a UI — o comando roda
// numa thread do runtime async e a criação de janela precisa despachar pra
// thread principal; nessa combinação específica (primeiro uso, ainda sem
// janela) o await nunca voltava e o botão "Aplicar" ficava girando pra
// sempre. Criando no setup() (sem estar dentro de um comando invocado pelo
// front) esse problema não existe — depois disso, toggle_overlay e
// set_overlay_position só mostram/escondem/reposicionam uma janela que já
// existe, nunca criam nada.
fn ensure_overlay_window(app: &AppHandle) {
    if app.get_webview_window(OVERLAY_LABEL).is_some() {
        return;
    }

    let corner = read_overlay_corner(app);
    let (x, y) = corner_position_in_work_area(app, OVERLAY_WIDTH, OVERLAY_HEIGHT, 16.0, &corner);

    let _ = WebviewWindowBuilder::new(app, OVERLAY_LABEL, WebviewUrl::App("overlay.html".into()))
        .title("Cronômetro")
        .inner_size(OVERLAY_WIDTH, OVERLAY_HEIGHT)
        .position(x, y)
        .decorations(false)
        .always_on_top(true)
        .skip_taskbar(true)
        .resizable(false)
        .shadow(false)
        .transparent(true)
        .visible(false)
        .build();
}

fn hide_overlay(app: &AppHandle) {
    if let Some(win) = app.get_webview_window(OVERLAY_LABEL) {
        let _ = win.hide();
    }
}

// Os 4 itens marcáveis (✓) do submenu "Cronômetro no canto" — guardados como
// managed state pra sync_corner_checkmarks conseguir achar e atualizar o
// check de cada um depois (tanto ao clicar um canto no próprio submenu
// quanto ao clicar "Aplicar" na janela principal, ver set_overlay_position).
struct OverlayCornerMenuItems {
    top_left: CheckMenuItem<tauri::Wry>,
    top_right: CheckMenuItem<tauri::Wry>,
    bottom_left: CheckMenuItem<tauri::Wry>,
    bottom_right: CheckMenuItem<tauri::Wry>,
}

fn sync_corner_checkmarks(app: &AppHandle, active_corner: &str) {
    let Some(items) = app.try_state::<OverlayCornerMenuItems>() else {
        return;
    };
    let _ = items.top_left.set_checked(active_corner == "top-left");
    let _ = items.top_right.set_checked(active_corner == "top-right");
    let _ = items.bottom_left.set_checked(active_corner == "bottom-left");
    let _ = items.bottom_right.set_checked(active_corner == "bottom-right");
}

// Salva o canto, reposiciona+mostra o overlay (janela já existe desde o
// boot) e mantém os checkmarks do submenu da bandeja em sincronia — chamado
// tanto pelo comando set_overlay_position (botão "Aplicar" da janela
// principal) quanto pelos itens do submenu da bandeja diretamente.
fn apply_overlay_corner(app: &AppHandle, corner: &str) {
    if let Ok(store) = app.store("config.json") {
        store.set(OVERLAY_CORNER_KEY, corner);
    }
    if let Some(win) = app.get_webview_window(OVERLAY_LABEL) {
        let (x, y) = corner_position_in_work_area(app, OVERLAY_WIDTH, OVERLAY_HEIGHT, 16.0, corner);
        let _ = win.set_position(Position::Logical(LogicalPosition { x, y }));
        let _ = win.show();
    }
    sync_corner_checkmarks(app, corner);
}

// Janela de notificação estilo "aviso de antivírus" — canto inferior direito,
// por cima de tudo. Criada já no boot (escondida): o próprio toast.tsx decide
// quando mostrar/esconder (poll no store por mensagem nova do admin, ver
// useDeviceHeartbeat.ts), sem precisar de IPC entre janelas — mesmo padrão
// autocontido do overlay.tsx.
fn ensure_toast_window(app: &AppHandle) {
    if app.get_webview_window(TOAST_LABEL).is_some() {
        return;
    }

    let (x, y) = corner_position_in_work_area(app, TOAST_WIDTH, TOAST_HEIGHT, 16.0, "bottom-right");

    let _ = WebviewWindowBuilder::new(app, TOAST_LABEL, WebviewUrl::App("toast.html".into()))
        .title("Aviso")
        .inner_size(TOAST_WIDTH, TOAST_HEIGHT)
        .position(x, y)
        .decorations(false)
        .always_on_top(true)
        .skip_taskbar(true)
        .resizable(false)
        // shadow(false) igual ao overlay: com janela sem decoração e fundo
        // transparente, a sombra do DWM aparece como uma moldura clara em
        // volta do card — não é borda do CSS, é a própria janela.
        .shadow(false)
        .transparent(true)
        .visible(false)
        .build();
}

// Comando chamado pelo botão "Aplicar" da tela principal — mesma lógica dos
// itens do submenu da bandeja (ver apply_overlay_corner), só que disparada
// pelo front em vez de um clique direto no menu.
#[tauri::command]
fn set_overlay_position(app: AppHandle, corner: String) {
    apply_overlay_corner(&app, &corner);
}

// Comando chamado sempre que o front detecta uma mudança relevante de status
// (poll da sessão em TimerScreen, sucesso/falha do heartbeat em
// useDeviceHeartbeat.ts) — troca a cor do ícone da bandeja e o texto do
// tooltip (aparece ao passar o mouse, padrão nativo do Windows). status:
// "no-session" | "ok" | "low" | "empty" | "offline" — qualquer valor não
// reconhecido cai no cinza.
#[tauri::command]
fn update_tray_status(app: AppHandle, status: String, tooltip: String) {
    let Some(tray) = app.try_state::<TrayIcon>() else {
        return;
    };
    let bytes: &[u8] = match status.as_str() {
        "ok" => TRAY_ICON_GREEN,
        "low" => TRAY_ICON_AMBER,
        "empty" | "offline" => TRAY_ICON_RED,
        _ => TRAY_ICON_GRAY,
    };
    let icon = Image::new(bytes, TRAY_ICON_SIZE, TRAY_ICON_SIZE);
    let _ = tray.set_icon(Some(icon));
    let _ = tray.set_tooltip(Some(tooltip));
}

// Inventário de software do PC: uma entrada de "Adicionar ou remover programas".
// O que sai daqui vai inteiro pro servidor (POST /public/lab-devices/inventory),
// que é quem sabe quais programas são esperados — assim a lista de expectativa
// muda no dashboard, sem instalador novo.
#[derive(serde::Serialize)]
pub struct InstalledProgram {
    name: String,
    version: String,
    publisher: String,
    // PNG do ícone em base64 (sem o prefixo data:), extraído do executável que
    // o registro aponta em DisplayIcon. Vazio quando o programa não declara
    // ícone ou o arquivo sumiu — o dashboard cai num placeholder.
    //
    // Não vai pro servidor no inventário: o front separa hash e bytes e só
    // envia as imagens que o servidor pedir (ver inventory.ts).
    icon: String,
    // sha256 do PNG acima. É o que identifica o ícone no servidor: o Blender é
    // o mesmo arquivo em todo PC, então o conteúdo só sobe uma vez.
    icon_hash: String,
}

// Tira caracteres de controle e espaços das pontas de um valor do registro.
// O NUL é o que importa: entradas como "Roblox Player for guibf\0" existem de
// verdade, e o Postgres do outro lado recusa 0x00 em coluna text — uma entrada
// assim derrubava o inventário inteiro da máquina. O servidor limpa de novo
// (não confia no cliente); aqui é higiene na origem.
#[cfg(windows)]
fn clean_reg_string(s: &str) -> String {
    s.chars().filter(|c| !c.is_control()).collect::<String>().trim().to_string()
}

// Tamanho do ícone coletado. O dashboard mostra a ~24px; Medium (48px) cobre
// tela retina sem engordar o payload — são ~80 programas por PC, e Large (96px)
// quadruplicaria os bytes de cada um à toa.

// Extrai o ícone que o registro aponta em DisplayIcon, como PNG base64.
//
// O valor vem em formatos variados: "C:\app\x.exe", "C:\app\x.exe,0" (índice do
// recurso), ou com aspas. Só o caminho interessa — a crate escolhe o ícone
// padrão do arquivo. Falha (arquivo removido, .ico corrompido, DLL sem ícone)
// devolve vazio: um programa sem ícone na tela é melhor que uma coleta perdida.
#[cfg(windows)]
fn extract_icon_base64(display_icon: &str) -> String {
    let raw = display_icon.trim().trim_matches('"');
    // ",0" / ",-15" no fim é índice do recurso, não parte do caminho. Cuidado
    // com "C:" — a vírgula tem que estar depois da letra de unidade.
    let path = match raw.rfind(',') {
        Some(i) if i > 2 => &raw[..i],
        _ => raw,
    };
    let path = path.trim().trim_matches('"');
    if path.is_empty() || !std::path::Path::new(path).exists() {
        return String::new();
    }
    windows_icons::get_icon_base64_by_path_with_size(path, windows_icons::IconSize::Medium)
        .unwrap_or_default()
}

// Captura da tela principal, em JPEG base64, para o admin ver o que está
// acontecendo no PC (pedida no heartbeat, ver useDeviceHeartbeat.ts).
//
// Só roda quando o servidor pede — nunca em laço. Quem chama também mostra o
// aviso na tela do PC: captura silenciosa numa máquina de uso compartilhado é
// exatamente o que não queremos.
#[derive(serde::Serialize)]
pub struct ScreenCapture {
    jpeg: String,
    width: u32,
    height: u32,
}

// Qualidade do JPEG. 70 mantém texto de tela legível (é pra isso que serve) e
// deixa uma tela 1080p em ~200-400 KB, em vez dos ~3 MB de um PNG.
#[cfg(windows)]
const SCREENSHOT_JPEG_QUALITY: u8 = 70;

#[cfg(windows)]
#[tauri::command]
fn capture_screen() -> Result<ScreenCapture, String> {
    use base64::Engine;
    use image::codecs::jpeg::JpegEncoder;
    use xcap::Monitor;

    let monitors = Monitor::all().map_err(|e| format!("monitores: {e}"))?;
    // Monitor principal; num PC de laboratório com dois monitores, é onde a
    // pessoa está trabalhando.
    let monitor = monitors
        .into_iter()
        .find(|m| m.is_primary().unwrap_or(false))
        .ok_or_else(|| "nenhum monitor principal".to_string())?;

    let image = monitor.capture_image().map_err(|e| format!("captura: {e}"))?;
    let (width, height) = (image.width(), image.height());

    // A captura vem em RGBA e o JPEG não tem canal alfa — codificar direto
    // falha com "does not support the color type Rgba8". Descartar o alfa aqui
    // é o certo: a tela é opaca, não há transparência real pra preservar.
    let rgb = image::DynamicImage::ImageRgba8(image).to_rgb8();

    let mut jpeg = Vec::new();
    JpegEncoder::new_with_quality(&mut jpeg, SCREENSHOT_JPEG_QUALITY)
        .encode(&rgb, width, height, image::ExtendedColorType::Rgb8)
        .map_err(|e| format!("codificar jpeg: {e}"))?;

    Ok(ScreenCapture {
        jpeg: base64::engine::general_purpose::STANDARD.encode(&jpeg),
        width,
        height,
    })
}

#[cfg(not(windows))]
#[tauri::command]
fn capture_screen() -> Result<ScreenCapture, String> {
    Err("captura de tela só no Windows".to_string())
}

// Aplicativos abertos agora no PC, pelo NOME — nunca o título da janela.
//
// O título carrega conteúdo ("Conversa com Fulano", "contrato.docx", a URL da
// aba) e o que o admin precisa saber é o que a máquina está rodando, não o que
// a pessoa está escrevendo. O nome do app entrega isso inteiro sem carregar
// junto o que não é da conta de ninguém.
//
// Vai junto do heartbeat (a cada ~30s), então é sempre a foto do momento — sem
// histórico: o que estava aberto meia hora atrás não ajuda a decidir nada e
// viraria um rastro de uso por pessoa.
// Identidade ESTÁVEL do PC, derivada do MachineGuid do Windows.
//
// Antes o device_uuid era sorteado no primeiro boot e vivia só no config.json:
// perder esse arquivo numa reinstalação criava um dispositivo NOVO no
// dashboard, e o mesmo computador aparecia duas vezes — aconteceu de verdade,
// com um registro virando fantasma. Pior no caso de uso real: o config é por
// usuário do Windows, então num PC de laboratório cada conta viraria um
// dispositivo diferente.
//
// O MachineGuid é criado na instalação do Windows e não muda com reinstalação
// de programa nem com troca de conta. Não vai cru: o id é o sha256 dele com um
// prefixo do app, formatado como UUID — assim não dá pra correlacionar esta
// máquina com nada fora daqui a partir do id.
#[cfg(windows)]
#[tauri::command]
fn stable_device_id() -> String {
    use sha2::{Digest, Sha256};
    use winreg::enums::{HKEY_LOCAL_MACHINE, KEY_READ, KEY_WOW64_64KEY};
    use winreg::RegKey;

    // KEY_WOW64_64KEY: sem isso um processo 32 bits leria a view redirecionada
    // e acharia outro valor (ou nenhum).
    let Ok(key) = RegKey::predef(HKEY_LOCAL_MACHINE)
        .open_subkey_with_flags(r"SOFTWARE\Microsoft\Cryptography", KEY_READ | KEY_WOW64_64KEY)
    else {
        return String::new();
    };
    let Ok(guid) = key.get_value::<String, _>("MachineGuid") else {
        return String::new();
    };
    let guid = clean_reg_string(&guid);
    if guid.is_empty() {
        return String::new();
    }

    let mut h = Sha256::new();
    h.update(b"santos-tech:hour-timer-app:device:");
    h.update(guid.as_bytes());
    let d = h.finalize();
    let hex: String = d.iter().take(16).map(|b| format!("{b:02x}")).collect();
    // Formato UUID (o servidor valida com regex de uuid), com versão 4 e
    // variante RFC 4122 — é um id derivado, não um UUID sorteado, mas precisa
    // passar pela mesma validação.
    format!(
        "{}-{}-4{}-a{}-{}",
        &hex[0..8],
        &hex[8..12],
        &hex[13..16],
        &hex[17..20],
        &hex[20..32]
    )
}

#[cfg(not(windows))]
#[tauri::command]
fn stable_device_id() -> String {
    String::new()
}

// Gera a IDENTIDADE deste PC (não dá acesso a ninguém — é a chave pública do
// próprio PC, não a do admin). No primeiro startup sem ~/.ssh/id_ed25519 (ou
// %USERPROFILE%\.ssh no Windows), gera um par ed25519 e devolve só a
// PÚBLICA. O heartbeat manda ela no campo sshPublicKey, que o servidor já
// aceita e persiste ("grava uma vez, não apaga com vazio" — ver
// handlers_lab_devices.go/hour_lab_devices.go).
//
// Quem dá acesso de SSH DE FORA pra dentro do PC é a função irmã
// sync_authorized_keys, logo abaixo, com a chave do ADMIN (direção oposta).
//
// Não é cfg(windows): a lógica em si (std::fs + crate ssh-key, sem API do
// Windows) roda igual em qualquer SO — só o nome da variável de ambiente do
// diretório do usuário muda.
#[tauri::command]
fn ensure_ssh_public_key() -> Result<String, String> {
    use ssh_key::{Algorithm, LineEnding, PrivateKey};
    use std::fs;
    use std::path::PathBuf;

    let home = std::env::var("USERPROFILE")
        .or_else(|_| std::env::var("HOME"))
        .map_err(|_| "sem USERPROFILE nem HOME".to_string())?;
    let ssh_dir = PathBuf::from(home).join(".ssh");
    let priv_path = ssh_dir.join("id_ed25519");
    let pub_path = ssh_dir.join("id_ed25519.pub");

    // Já existe: nunca sobrescrever (invalidaria acesso configurado à mão).
    // .trim() importa: o servidor valida sshPublicKey com regex ancorado em
    // $ (fim de string literal, RE2 não trata como "antes de \n" por
    // padrão) — o \n do arquivo gravado abaixo quebraria a validação em
    // todo heartbeat a partir do segundo, já que aqui é onde ele volta a
    // ser lido.
    if pub_path.exists() {
        return fs::read_to_string(&pub_path)
            .map(|s| s.trim().to_string())
            .map_err(|e| format!("ler chave existente: {e}"));
    }

    fs::create_dir_all(&ssh_dir).map_err(|e| format!("criar {}: {e}", ssh_dir.display()))?;

    let private_key = PrivateKey::random(&mut rand::rngs::OsRng, Algorithm::Ed25519)
        .map_err(|e| format!("gerar par de chaves: {e}"))?;
    let private_pem = private_key
        .to_openssh(LineEnding::LF)
        .map_err(|e| format!("serializar chave privada: {e}"))?;
    let public_line = private_key
        .public_key()
        .to_openssh()
        .map_err(|e| format!("serializar chave publica: {e}"))?;

    fs::write(&priv_path, private_pem.as_bytes()).map_err(|e| format!("gravar {}: {e}", priv_path.display()))?;
    // Permissão restrita na chave privada — sem isso o próprio ssh cliente
    // recusa usar o arquivo (Unix; no Windows a ACL padrão do perfil do
    // usuário já restringe o suficiente, não há equivalente direto de chmod).
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        let _ = fs::set_permissions(&priv_path, fs::Permissions::from_mode(0o600));
    }
    fs::write(&pub_path, format!("{public_line}\n")).map_err(|e| format!("gravar {}: {e}", pub_path.display()))?;

    Ok(public_line)
}

// Instala a(s) chave(s) pública(s) do ADMIN da frota (Guilherme) no
// authorized_keys deste PC — é isso que permite `ssh` de fora pra dentro
// (a ensure_ssh_public_key acima é a direção oposta: identidade do PC, não
// dá acesso a ninguém). Chamada a cada heartbeat com o que a API devolver em
// adminSSHPublicKeys (useDeviceHeartbeat.ts), pra convergir sozinho se a
// chave rodar — sem precisar reinstalar o app.
//
// Idempotente e só ADITIVO: nunca remove uma linha existente (mesmo que não
// tenha sido esta função quem a colocou lá) — evita trancar o próprio acesso
// fora se uma rotação de chave passar despercebida, e preserva qualquer
// chave que alguém tenha adicionado à mão pra debug.
//
// Nota Windows: sshd trata contas do grupo Administrators diferente — usa
// %ProgramData%\ssh\administrators_authorized_keys (arquivo do sistema, não
// por usuário) em vez do authorized_keys normal. Esta função só escreve o
// arquivo por-usuário; numa conta administradora local, pode não bastar.
#[tauri::command]
fn sync_authorized_keys(keys: Vec<String>) -> Result<(), String> {
    use std::fs;
    use std::path::PathBuf;

    if keys.is_empty() {
        return Ok(());
    }

    let home = std::env::var("USERPROFILE")
        .or_else(|_| std::env::var("HOME"))
        .map_err(|_| "sem USERPROFILE nem HOME".to_string())?;
    let ssh_dir = PathBuf::from(home).join(".ssh");
    let authorized_keys_path = ssh_dir.join("authorized_keys");

    fs::create_dir_all(&ssh_dir).map_err(|e| format!("criar {}: {e}", ssh_dir.display()))?;

    let existing = fs::read_to_string(&authorized_keys_path).unwrap_or_default();
    let existing_lines: Vec<&str> = existing.lines().map(str::trim).collect();

    let missing: Vec<&str> = keys
        .iter()
        .map(|k| k.trim())
        .filter(|k| !k.is_empty() && !existing_lines.contains(k))
        .collect();

    if missing.is_empty() {
        return Ok(());
    }

    let mut updated = existing;
    if !updated.is_empty() && !updated.ends_with('\n') {
        updated.push('\n');
    }
    for key in missing {
        updated.push_str(key);
        updated.push('\n');
    }

    fs::write(&authorized_keys_path, &updated)
        .map_err(|e| format!("gravar {}: {e}", authorized_keys_path.display()))?;
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        let _ = fs::set_permissions(&authorized_keys_path, fs::Permissions::from_mode(0o600));
    }

    Ok(())
}

// Nome de exibição do app. Para programa comum o Windows devolve o nome
// ("Zen", "NVIDIA App"), mas para app empacotado da Store devolve o CAMINHO do
// executável — e aí a lista mostrava
// "C:\Program Files\WindowsApps\Microsoft.ScreenSketch_11.26...\SnippingTool\Sni",
// cortado no meio pelo limite de tamanho. Nesses casos fica só o nome do
// arquivo, sem a extensão.
#[cfg(windows)]
fn app_display_name(raw: &str) -> String {
    let cleaned = clean_reg_string(raw);
    if !cleaned.contains('\\') && !cleaned.contains('/') {
        return cleaned;
    }
    let base = cleaned.rsplit(['\\', '/']).next().unwrap_or(&cleaned);
    base.trim_end_matches(".exe").trim_end_matches(".EXE").trim().to_string()
}

#[cfg(windows)]
#[tauri::command]
fn list_open_apps() -> Vec<String> {
    use xcap::Window;

    let Ok(windows) = Window::all() else {
        return Vec::new();
    };
    let mut names: Vec<String> = windows
        .iter()
        .filter_map(|w| w.app_name().ok())
        .map(|n| app_display_name(&n))
        .filter(|n| !n.is_empty())
        .collect();
    names.sort_by_key(|n| n.to_lowercase());
    names.dedup_by(|a, b| a.eq_ignore_ascii_case(b));
    names
}

#[cfg(not(windows))]
#[tauri::command]
fn list_open_apps() -> Vec<String> {
    Vec::new()
}

// sha256 dos BYTES do PNG (não do base64) — o servidor recalcula igual, a
// partir do que recebe, e recusa quando não bate. Ícone vazio não tem hash.
#[cfg(windows)]
fn icon_sha256(icon_base64: &str) -> String {
    use base64::Engine;
    use sha2::{Digest, Sha256};

    if icon_base64.is_empty() {
        return String::new();
    }
    let Ok(png) = base64::engine::general_purpose::STANDARD.decode(icon_base64) else {
        return String::new();
    };
    let mut h = Sha256::new();
    h.update(&png);
    h.finalize().iter().map(|b| format!("{b:02x}")).collect()
}

// Lê as três chaves Uninstall do registro: HKLM (64 bits), HKLM\WOW6432Node
// (programas de 32 bits numa máquina de 64) e HKCU (instalado só pro usuário
// atual, caso do VS Code e do Unity Hub). É a mesma fonte que o painel do
// Windows usa — não existe API melhor sem instalar agente.
#[cfg(windows)]
#[tauri::command]
fn list_installed_programs() -> Vec<InstalledProgram> {
    use winreg::enums::{HKEY_CURRENT_USER, HKEY_LOCAL_MACHINE, KEY_READ};
    use winreg::RegKey;

    const UNINSTALL: &str = r"SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall";
    const UNINSTALL_WOW: &str = r"SOFTWARE\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall";

    let roots = [
        (HKEY_LOCAL_MACHINE, UNINSTALL),
        (HKEY_LOCAL_MACHINE, UNINSTALL_WOW),
        (HKEY_CURRENT_USER, UNINSTALL),
    ];

    let mut out: Vec<InstalledProgram> = Vec::new();
    for (hive, path) in roots {
        let Ok(root) = RegKey::predef(hive).open_subkey_with_flags(path, KEY_READ) else {
            continue; // WOW6432Node não existe em Windows 32 bits
        };
        for key_name in root.enum_keys().flatten() {
            let Ok(entry) = root.open_subkey_with_flags(&key_name, KEY_READ) else {
                continue;
            };
            let name: String = match entry.get_value("DisplayName") {
                Ok(v) => v,
                Err(_) => continue, // sem nome exibido = não aparece nem pro usuário
            };
            let name = clean_reg_string(&name);
            if name.is_empty() {
                continue;
            }
            // SystemComponent=1 e ReleaseType de atualização são o ruído do
            // registro: runtimes, hotfix do Office, KB do Windows. Sem esse
            // filtro o inventário vem com centenas de linhas que ninguém quer
            // ver na tela do dashboard.
            if entry.get_value::<u32, _>("SystemComponent").unwrap_or(0) == 1 {
                continue;
            }
            let release_type: String = entry.get_value("ReleaseType").unwrap_or_default();
            if matches!(
                release_type.as_str(),
                "Security Update" | "Update Rollup" | "Hotfix" | "Update"
            ) {
                continue;
            }
            // Entrada filha de outra (patch de um programa já listado).
            if entry.get_value::<String, _>("ParentKeyName").is_ok() {
                continue;
            }
            let display_icon: String = entry.get_value("DisplayIcon").unwrap_or_default();
            let icon = extract_icon_base64(&clean_reg_string(&display_icon));
            out.push(InstalledProgram {
                name,
                version: clean_reg_string(
                    &entry.get_value::<String, _>("DisplayVersion").unwrap_or_default(),
                ),
                publisher: clean_reg_string(
                    &entry.get_value::<String, _>("Publisher").unwrap_or_default(),
                ),
                icon_hash: icon_sha256(&icon),
                icon,
            });
        }
    }
    // A mesma entrada costuma aparecer em HKLM e HKCU; o servidor também
    // deduplica, mas mandar duas vezes só gasta payload.
    out.sort_by(|a, b| a.name.to_lowercase().cmp(&b.name.to_lowercase()));
    out.dedup_by(|a, b| a.name == b.name && a.version == b.version);
    out
}

// Fora do Windows o app só roda em dev (o alvo real é o PC do laboratório):
// devolve lista vazia pra manter a mesma interface pro front.
#[cfg(not(windows))]
#[tauri::command]
fn list_installed_programs() -> Vec<InstalledProgram> {
    Vec::new()
}

fn show_main(app: &AppHandle) {
    if let Some(win) = app.get_webview_window("main") {
        let _ = win.show();
        let _ = win.set_focus();
    }
}

#[cfg_attr(mobile, tauri::mobile_entry_point)]
pub fn run() {
    tauri::Builder::default()
        // Precisa ser o primeiro plugin registrado (requisito do Tauri no
        // Windows). O watchdog (watchdog.bat/Tarefa Agendada, ver
        // installer-hooks.nsh) já evita chamar o .exe quando ele está
        // rodando, então isso aqui é rede de segurança pra um duplo-clique
        // manual no atalho/instalador com o app já aberto: só foca a janela
        // principal existente em vez de abrir uma instância nova.
        .plugin(tauri_plugin_single_instance::init(|app, _args, _cwd| {
            show_main(app);
        }))
        .plugin(tauri_plugin_opener::init())
        .plugin(tauri_plugin_store::Builder::default().build())
        .plugin(tauri_plugin_autostart::init(MacosLauncher::LaunchAgent, None))
        .plugin(tauri_plugin_updater::Builder::new().build())
        .plugin(tauri_plugin_process::init())
        .setup(|app| {
            // PC de laboratório: o app precisa estar sempre rodando sem
            // depender de alguém ir fisicamente em cada máquina religar depois
            // de reiniciar o Windows. `enable()` é idempotente — chamar de
            // novo a cada boot não duplica o registro de autostart.
            let _ = app.autolaunch().enable();

            // Glifos Unicode como "ícone" de cada item — menu nativo da
            // bandeja no Windows não aceita widget/imagem arbitrária dentro
            // dele (só texto, ícone de imagem por item ou checkmark), e um
            // glifo simples (◰◳◱◲) já entrega a mesma leitura visual rápida
            // de "em qual canto" sem precisar gerar 4 ícones de imagem novos.
            let show_item = MenuItem::with_id(app, "show", "🗖  Abrir", true, None::<&str>)?;

            let initial_corner = read_overlay_corner(&app.handle());
            let corner_top_left = CheckMenuItem::with_id(
                app, "overlay_top_left", "◰  Superior esquerdo", true,
                initial_corner == "top-left", None::<&str>,
            )?;
            let corner_top_right = CheckMenuItem::with_id(
                app, "overlay_top_right", "◳  Superior direito", true,
                initial_corner == "top-right", None::<&str>,
            )?;
            let corner_bottom_left = CheckMenuItem::with_id(
                app, "overlay_bottom_left", "◱  Inferior esquerdo", true,
                initial_corner == "bottom-left", None::<&str>,
            )?;
            let corner_bottom_right = CheckMenuItem::with_id(
                app, "overlay_bottom_right", "◲  Inferior direito", true,
                initial_corner == "bottom-right", None::<&str>,
            )?;
            let hide_overlay_item = MenuItem::with_id(app, "overlay_hide", "✕  Esconder", true, None::<&str>)?;
            let overlay_separator = PredefinedMenuItem::separator(app)?;
            let overlay_submenu = Submenu::with_id_and_items(
                app,
                "overlay_submenu",
                "⏱  Cronômetro no canto",
                true,
                &[
                    &corner_top_left,
                    &corner_top_right,
                    &corner_bottom_left,
                    &corner_bottom_right,
                    &overlay_separator,
                    &hide_overlay_item,
                ],
            )?;
            app.manage(OverlayCornerMenuItems {
                top_left: corner_top_left,
                top_right: corner_top_right,
                bottom_left: corner_bottom_left,
                bottom_right: corner_bottom_right,
            });

            let quit_item = MenuItem::with_id(app, "quit", "⏻  Sair", true, None::<&str>)?;
            let menu = Menu::with_items(app, &[&show_item, &overlay_submenu, &quit_item])?;

            let tray = TrayIconBuilder::new()
                .icon(app.default_window_icon().unwrap().clone())
                .tooltip("Santos Tech")
                .menu(&menu)
                .show_menu_on_left_click(false)
                .on_menu_event(|app, event| match event.id.as_ref() {
                    "show" => show_main(app),
                    "overlay_top_left" => apply_overlay_corner(app, "top-left"),
                    "overlay_top_right" => apply_overlay_corner(app, "top-right"),
                    "overlay_bottom_left" => apply_overlay_corner(app, "bottom-left"),
                    "overlay_bottom_right" => apply_overlay_corner(app, "bottom-right"),
                    "overlay_hide" => hide_overlay(app),
                    "quit" => app.exit(0),
                    _ => {}
                })
                .on_tray_icon_event(|tray, event| {
                    if let TrayIconEvent::Click {
                        button: MouseButton::Left,
                        button_state: MouseButtonState::Up,
                        ..
                    } = event
                    {
                        show_main(tray.app_handle());
                    }
                })
                .build(app)?;
            // Guardado como managed state pra update_tray_status conseguir
            // achar o mesmo ícone depois (troca cor/tooltip ao vivo conforme
            // o front reporta status da sessão/heartbeat).
            app.manage(tray);

            ensure_toast_window(&app.handle());
            ensure_overlay_window(&app.handle());

            Ok(())
        })
        .on_window_event(|window, event| {
            // Fechar a janela principal (X) só esconde — o app continua rodando
            // na bandeja pra avisar quando o tempo tiver acabando. "Sair" no
            // menu da bandeja é o único jeito de encerrar de verdade.
            if window.label() == "main" {
                if let WindowEvent::CloseRequested { api, .. } = event {
                    api.prevent_close();
                    let _ = window.hide();
                }
            }
        })
        .invoke_handler(tauri::generate_handler![
            set_overlay_position,
            update_tray_status,
            list_installed_programs,
            capture_screen,
            list_open_apps,
            stable_device_id,
            ensure_ssh_public_key,
            sync_authorized_keys
        ])
        .run(tauri::generate_context!())
        .expect("error while running tauri application");
}
