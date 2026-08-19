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
        .shadow(true)
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
        .invoke_handler(tauri::generate_handler![set_overlay_position, update_tray_status])
        .run(tauri::generate_context!())
        .expect("error while running tauri application");
}
