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

    if let Ok(exe) = env::current_exe() {
        if let Some(dir) = exe.parent() {
            let _ = Command::new(dir.join(TARGET_EXE)).spawn();
        }
    }
}
