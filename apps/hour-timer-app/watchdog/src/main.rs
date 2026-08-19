// Watchdog do PC de laboratório (ver ../src-tauri/installer-hooks.nsh): a
// Tarefa Agendada roda este binário a cada 5min. `windows_subsystem =
// "windows"` é o que evita o console piscando a cada execução — um
// .bat/.ps1 rodado pelo Agendador sempre abre uma janela de terminal por
// uma fração de segundo; um executável Windows GUI-subsystem nunca abre
// console nenhum, nem brevemente. Só inicia o app se ele não estiver
// rodando (sysinfo, sem shellar tasklist — shellar um processo de console
// também piscaria janela).
#![windows_subsystem = "windows"]

use std::env;
use std::process::Command;
use sysinfo::System;

const TARGET_EXE: &str = "hour-timer-app.exe";

// O instalador (ver ../src-tauri/installer-hooks.nsh) copia este binário pra
// C:\ProgramData\SantosTechWatchdog\ — fora de $INSTDIR — porque o Agendador
// de Tarefas (schtasks /tr) tem um bug conhecido do Windows que corta o
// caminho errado quando ele tem espaço, e $INSTDIR ("...\Santos Tech -
// Cronômetro") tem. Isso significa que watchdog.exe NÃO é mais vizinho de
// hour-timer-app.exe (current_exe().parent() aponta pro ProgramData, não pro
// install dir) — por isso resolve via %LOCALAPPDATA%\<productName> direto,
// hardcoded (é um binário de propósito único pra este app, não genérico).
fn target_exe_path() -> Option<std::path::PathBuf> {
    let local_app_data = env::var("LOCALAPPDATA").ok()?;
    Some(
        std::path::Path::new(&local_app_data)
            .join("Santos Tech - Cronômetro")
            .join(TARGET_EXE),
    )
}

fn main() {
    let mut sys = System::new();
    sys.refresh_processes(sysinfo::ProcessesToUpdate::All, true);
    let already_running = sys
        .processes()
        .values()
        .any(|p| p.name().to_string_lossy().eq_ignore_ascii_case(TARGET_EXE));
    if already_running {
        return;
    }

    if let Some(target) = target_exe_path() {
        let _ = Command::new(target).spawn();
    }
}
