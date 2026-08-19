use tauri::{
    menu::{Menu, MenuItem},
    tray::{MouseButton, MouseButtonState, TrayIconBuilder, TrayIconEvent},
    AppHandle, LogicalPosition, Manager, Position, WebviewUrl, WebviewWindowBuilder, WindowEvent,
};
use tauri_plugin_autostart::{MacosLauncher, ManagerExt};
use tauri_plugin_store::StoreExt;

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

// Janela flutuante com o tempo, no canto configurado (ver set_overlay_position)
// — mostra/esconde ao clicar de novo no item do menu da bandeja. Só é criada
// na primeira vez.
fn toggle_overlay(app: &AppHandle) {
    if let Some(win) = app.get_webview_window(OVERLAY_LABEL) {
        let visible = win.is_visible().unwrap_or(false);
        if visible {
            let _ = win.hide();
        } else {
            let _ = win.show();
        }
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
        .build();
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

// Comando chamado pelo botão "Posição na tela" da janela principal — salva a
// escolha (pra próxima vez que o overlay for criado) e, se ele já estiver
// aberto agora, reposiciona ao vivo em vez de esperar a próxima abertura.
#[tauri::command]
fn set_overlay_position(app: AppHandle, corner: String) {
    if let Ok(store) = app.store("config.json") {
        store.set(OVERLAY_CORNER_KEY, corner.clone());
    }
    if let Some(win) = app.get_webview_window(OVERLAY_LABEL) {
        let (x, y) = corner_position_in_work_area(&app, OVERLAY_WIDTH, OVERLAY_HEIGHT, 16.0, &corner);
        let _ = win.set_position(Position::Logical(LogicalPosition { x, y }));
    }
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

            let show_item = MenuItem::with_id(app, "show", "Abrir", true, None::<&str>)?;
            let overlay_item =
                MenuItem::with_id(app, "overlay", "Mostrar tempo no canto", true, None::<&str>)?;
            let quit_item = MenuItem::with_id(app, "quit", "Sair", true, None::<&str>)?;
            let menu = Menu::with_items(app, &[&show_item, &overlay_item, &quit_item])?;

            TrayIconBuilder::new()
                .icon(app.default_window_icon().unwrap().clone())
                .menu(&menu)
                .show_menu_on_left_click(false)
                .on_menu_event(|app, event| match event.id.as_ref() {
                    "show" => show_main(app),
                    "overlay" => toggle_overlay(app),
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

            ensure_toast_window(&app.handle());

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
        .invoke_handler(tauri::generate_handler![set_overlay_position])
        .run(tauri::generate_context!())
        .expect("error while running tauri application");
}
