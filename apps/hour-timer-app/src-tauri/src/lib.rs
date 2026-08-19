use tauri::{
    menu::{Menu, MenuItem},
    tray::{MouseButton, MouseButtonState, TrayIconBuilder, TrayIconEvent},
    AppHandle, Manager, WebviewUrl, WebviewWindowBuilder, WindowEvent,
};

const OVERLAY_LABEL: &str = "overlay";
const OVERLAY_WIDTH: f64 = 220.0;
const OVERLAY_HEIGHT: f64 = 70.0;

const TOAST_LABEL: &str = "toast";
const TOAST_WIDTH: f64 = 340.0;
const TOAST_HEIGHT: f64 = 120.0;

// Janela flutuante com o tempo, canto inferior direito — mostra/esconde ao
// clicar de novo no item do menu da bandeja. Só é criada na primeira vez.
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

    let (x, y) = app
        .primary_monitor()
        .ok()
        .flatten()
        .map(|m| {
            let scale = m.scale_factor();
            let size = m.size();
            let w = size.width as f64 / scale;
            let h = size.height as f64 / scale;
            (w - OVERLAY_WIDTH - 16.0, h - OVERLAY_HEIGHT - 56.0)
        })
        .unwrap_or((900.0, 700.0));

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

    let (x, y) = app
        .primary_monitor()
        .ok()
        .flatten()
        .map(|m| {
            let scale = m.scale_factor();
            let size = m.size();
            let w = size.width as f64 / scale;
            let h = size.height as f64 / scale;
            (w - TOAST_WIDTH - 16.0, h - TOAST_HEIGHT - 16.0)
        })
        .unwrap_or((900.0, 700.0));

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

fn show_main(app: &AppHandle) {
    if let Some(win) = app.get_webview_window("main") {
        let _ = win.show();
        let _ = win.set_focus();
    }
}

#[cfg_attr(mobile, tauri::mobile_entry_point)]
pub fn run() {
    tauri::Builder::default()
        .plugin(tauri_plugin_opener::init())
        .plugin(tauri_plugin_store::Builder::default().build())
        .plugin(tauri_plugin_notification::init())
        .setup(|app| {
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
        .run(tauri::generate_context!())
        .expect("error while running tauri application");
}
