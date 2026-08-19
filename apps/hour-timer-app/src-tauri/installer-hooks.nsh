; Watchdog do PC de laboratório: além do autostart-no-login (tauri-plugin-
; autostart, cobre reinício do PC), uma Tarefa Agendada do Windows roda
; watchdog.exe a cada 5 minutos — cobre o caso de alguém matar o processo
; pelo Gerenciador de Tarefas com o Windows ainda ligado, sem precisar ir
; fisicamente na máquina. watchdog.exe é um binário GUI-subsystem (ver
; src/bin/watchdog.rs) — nunca abre janela de console, ao contrário de um
; .bat/.ps1 rodado pelo Agendador (que pisca terminal a cada execução). Só
; inicia o app se ele NÃO estiver rodando — evita reabrir/roubar foco.
!macro NSIS_HOOK_POSTINSTALL
  ; watchdog.exe já foi copiado pra $INSTDIR pela seção principal do
  ; instalador (é um "resource" do bundle, ver tauri.conf.json) antes deste
  ; hook rodar — não precisa de File aqui.
  nsExec::ExecToLog 'schtasks /create /f /sc minute /mo 5 /tn "SantosTechCronometroWatchdog" /tr "\"$INSTDIR\watchdog.exe\"" /rl limited'
!macroend

!macro NSIS_HOOK_PREUNINSTALL
  nsExec::ExecToLog 'schtasks /delete /tn "SantosTechCronometroWatchdog" /f'
!macroend

!macro NSIS_HOOK_POSTUNINSTALL
  Delete "$INSTDIR\watchdog.exe"
!macroend
