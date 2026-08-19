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
  ; hook rodar. MAS o Agendador de Tarefas (schtasks /tr) tem um bug clássico
  ; do Windows: quando o caminho no /tr tem espaço — mesmo entre aspas do
  ; jeito documentado — ele corta errado no primeiro espaço e guarda um
  ; Command/Arguments quebrado (confirmado testando isolado: "Santos Tech -
  ; Cronômetro" tem 2 espaços, falha sempre com 0x80070002/FILE_NOT_FOUND
  ; mesmo o arquivo existindo). $INSTDIR tem espaço — por isso copiamos o
  ; watchdog pra um caminho fixo sem espaço em C:\ProgramData (sem elevar:
  ; ACL padrão de Usuários já permite criar subpasta/gravar) só pra apontar a
  ; tarefa pra lá. Faz sentido também semanticamente: é infra da máquina
  ; (watchdog do PC do laboratório), não dado de um usuário específico —
  ; evita qualquer ACL herdada estranha que a pasta de perfil do usuário
  ; possa ter acumulado. NSIS não tem uma constante "$COMMONAPPDATA" própria
  ; — o jeito documentado de apontar pra ProgramData é $APPDATA depois de
  ; SetShellVarContext all (volta pro contexto "current" no fim do hook, sem
  ; afetar o resto do instalador).
  SetShellVarContext all
  CreateDirectory "$APPDATA\SantosTechWatchdog"
  CopyFiles /SILENT "$INSTDIR\watchdog.exe" "$APPDATA\SantosTechWatchdog\watchdog.exe"
  nsExec::ExecToLog 'schtasks /create /f /sc minute /mo 5 /tn "SantosTechCronometroWatchdog" /tr "\"$APPDATA\SantosTechWatchdog\watchdog.exe\"" /rl limited'
  SetShellVarContext current
!macroend

!macro NSIS_HOOK_PREUNINSTALL
  nsExec::ExecToLog 'schtasks /delete /tn "SantosTechCronometroWatchdog" /f'
!macroend

!macro NSIS_HOOK_POSTUNINSTALL
  Delete "$INSTDIR\watchdog.exe"
  SetShellVarContext all
  Delete "$APPDATA\SantosTechWatchdog\watchdog.exe"
  RMDir "$APPDATA\SantosTechWatchdog"
  SetShellVarContext current
!macroend
