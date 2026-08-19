// Santos Hub é só um launcher: abre, lista o catálogo (GET /public/downloads),
// baixa/abre o item escolhido no app/navegador padrão do Windows, fecha. Sem
// bandeja, sem janelas extra, sem estado persistido — ao contrário do
// hour-timer-app (que monitora uma sessão em segundo plano), aqui não há nada
// pra manter rodando entre uma abertura e outra.
#[cfg_attr(mobile, tauri::mobile_entry_point)]
pub fn run() {
    tauri::Builder::default()
        .plugin(tauri_plugin_opener::init())
        .plugin(tauri_plugin_http::init())
        .run(tauri::generate_context!())
        .expect("error while running tauri application");
}
