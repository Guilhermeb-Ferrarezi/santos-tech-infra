@echo off
rem Watchdog do PC de laboratorio (ver installer-hooks.nsh): so inicia o app
rem se ele nao estiver rodando -- evita "roubar" o foco da janela do cliente
rem a cada checagem quando o app ja esta aberto.
tasklist /fi "imagename eq hour-timer-app.exe" | find /i "hour-timer-app.exe" >nul
if errorlevel 1 start "" "%~dp0hour-timer-app.exe"
