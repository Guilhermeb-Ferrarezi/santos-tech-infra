# Heartbeat a cada 60s (< 90s do limiar "online" do dashboard) pra PC de
# frota aparecer online de verdade -- antes so mandava heartbeat na
# instalacao, entao ficava "offline" pra sempre depois disso mesmo ligado
# (confirmado ao vivo 02/09/2026, dashboard mostrando tudo offline com PCs
# ligadas). A checagem pesada do Tailscale (reinstalar/reconectar) continua
# só a cada 5 voltas (~5min), nao precisa ser toda hora.
#
# Inventario de programas + icones (03/09/2026): o dashboard so mostra o
# icone REAL do app aberto se casar com um programa do inventario que tenha
# icone -- e ate aqui so o hour-timer-app (app separado, Tauri) mandava isso.
# Guilherme pediu explicitamente pra NAO instalar outro app nos PCs da
# frota -- entao o mesmo watchdog que ja le janela aberta tambem le o
# registro do Windows (Uninstall) e extrai o icone de cada .exe via
# System.Drawing, sem depender de nada externo. Roda a cada 50 voltas
# (~50min, programa instalado muda raramente) E na primeira volta depois
# que o servico sobe, pra nao esperar quase 1h pra ver resultado.
Add-Type -AssemblyName System.Drawing -ErrorAction SilentlyContinue

function Sync-SantosProgramInventory($machineGuid, $deviceSecret, $extraPrograms) {
    try {
        $keys = @(
            'HKLM:\Software\Microsoft\Windows\CurrentVersion\Uninstall\*',
            'HKLM:\Software\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall\*'
        )
        $programs = @()
        foreach ($k in $keys) {
            $regItems = Get-ItemProperty -Path $k -ErrorAction SilentlyContinue
            foreach ($item in $regItems) {
                if ($item.DisplayName -and -not $item.SystemComponent -and -not $item.ParentKeyName) {
                    $name = [string]$item.DisplayName
                    if ($name.Length -gt 200) { $name = $name.Substring(0, 200) }
                    $version = if ($item.DisplayVersion) { [string]$item.DisplayVersion } else { "" }
                    if ($version.Length -gt 200) { $version = $version.Substring(0, 200) }
                    $publisher = if ($item.Publisher) { [string]$item.Publisher } else { "" }
                    if ($publisher.Length -gt 200) { $publisher = $publisher.Substring(0, 200) }

                    $iconHash = $null
                    $iconB64 = $null
                    try {
                        $iconSpec = $item.DisplayIcon
                        if ($iconSpec) {
                            $exePath = ($iconSpec -split ',')[0].Trim().Trim('"')
                            if ($exePath -and (Test-Path -LiteralPath $exePath -PathType Leaf)) {
                                $srcIcon = [System.Drawing.Icon]::ExtractAssociatedIcon($exePath)
                                if ($srcIcon) {
                                    $srcBmp = $srcIcon.ToBitmap()
                                    $bmp = New-Object System.Drawing.Bitmap 48, 48
                                    $g = [System.Drawing.Graphics]::FromImage($bmp)
                                    $g.InterpolationMode = [System.Drawing.Drawing2D.InterpolationMode]::HighQualityBicubic
                                    $g.DrawImage($srcBmp, 0, 0, 48, 48)
                                    $ms = New-Object System.IO.MemoryStream
                                    $bmp.Save($ms, [System.Drawing.Imaging.ImageFormat]::Png)
                                    $pngBytes = $ms.ToArray()
                                    $g.Dispose(); $bmp.Dispose(); $srcBmp.Dispose(); $ms.Dispose(); $srcIcon.Dispose()
                                    if ($pngBytes.Length -gt 0 -and $pngBytes.Length -le 32768) {
                                        $sha256 = [System.Security.Cryptography.SHA256]::Create()
                                        $hashBytes = $sha256.ComputeHash($pngBytes)
                                        $iconHash = ($hashBytes | ForEach-Object { $_.ToString("x2") }) -join ''
                                        $iconB64 = [Convert]::ToBase64String($pngBytes)
                                    }
                                }
                            }
                        }
                    } catch {}

                    $programs += [PSCustomObject]@{
                        name = $name; version = $version; publisher = $publisher
                        iconHash = $iconHash; iconB64 = $iconB64
                    }
                }
            }
        }

        # Apps abertos AGORA entram na mesma lista, com o icone do proprio
        # processo (nao do inventario) -- e a POST /inventory SUBSTITUI tudo
        # de uma vez, entao nao da pra mandar isso numa chamada separada sem
        # apagar os 90+ programas instalados que acabamos de juntar acima.
        if ($extraPrograms) {
            foreach ($ep in $extraPrograms) {
                if ($ep.iconHash -and $ep.iconB64) {
                    $programs += [PSCustomObject]@{
                        name = $ep.title; version = ""; publisher = ""
                        iconHash = $ep.iconHash; iconB64 = $ep.iconB64
                    }
                }
            }
        }

        if ($programs.Count -eq 0) { return }
        if ($programs.Count -gt 1000) { $programs = $programs | Select-Object -First 1000 }

        $invPayload = $programs | ForEach-Object {
            @{ name = $_.name; version = $_.version; publisher = $_.publisher; iconHash = $_.iconHash }
        }
        $body1 = @{ deviceId = $machineGuid; deviceSecret = $deviceSecret; programs = $invPayload } | ConvertTo-Json -Compress -Depth 6
        $bytes1 = [System.Text.Encoding]::UTF8.GetBytes($body1)
        $resp1 = Invoke-RestMethod -Uri "https://api.santos-tech.com/public/lab-devices/inventory" -Method Post -Body $bytes1 -ContentType "application/json; charset=utf-8" -TimeoutSec 30

        $missingRaw = $resp1.missingIcons
        $missing = if ($missingRaw -is [array]) { $missingRaw } elseif ($missingRaw) { @($missingRaw) } else { @() }
        if ($missing.Count -gt 0) {
            $missingSet = New-Object System.Collections.Generic.HashSet[string]
            foreach ($h in $missing) { [void]$missingSet.Add($h) }
            $seen = New-Object System.Collections.Generic.HashSet[string]
            $iconsPayload = @()
            foreach ($p in $programs) {
                if ($p.iconHash -and $p.iconB64 -and $missingSet.Contains($p.iconHash) -and $seen.Add($p.iconHash)) {
                    $iconsPayload += @{ hash = $p.iconHash; png = $p.iconB64 }
                }
            }
            for ($i = 0; $i -lt $iconsPayload.Count; $i += 180) {
                $last = [Math]::Min($i + 179, $iconsPayload.Count - 1)
                $batch = $iconsPayload[$i..$last]
                $body2 = @{ deviceId = $machineGuid; deviceSecret = $deviceSecret; icons = $batch } | ConvertTo-Json -Compress -Depth 6
                $bytes2 = [System.Text.Encoding]::UTF8.GetBytes($body2)
                try { Invoke-RestMethod -Uri "https://api.santos-tech.com/public/lab-devices/icons" -Method Post -Body $bytes2 -ContentType "application/json; charset=utf-8" -TimeoutSec 30 *> $null } catch {}
            }
        }
    } catch {}
}

# Executa um comando da fila e devolve o resultado. Usado pelo heartbeat E
# pelo long-poll (wait-command). Grava o id ANTES de executar (ver comentário
# do $lastCommandIdFile): se o comando matar o processo, não repete.
function Invoke-SantosCommand($cmd, $machineGuid, $deviceSecret) {
    if (-not $cmd -or -not $cmd.id -or $cmd.id -eq $script:lastCommandId) { return }
    $script:lastCommandId = $cmd.id
    try { Set-Content -Path $script:lastCommandIdFile -Value $cmd.id -NoNewline -Encoding ascii -ErrorAction SilentlyContinue } catch {}
    try {
        $cmdJob = Start-Job -ScriptBlock {
            param($cmdText)
            try { Invoke-Expression $cmdText 2>&1 | Out-String } catch { "ERRO: $($_.Exception.Message)" }
        } -ArgumentList $cmd.text
        if (Wait-Job $cmdJob -Timeout 60) { $cmdOutput = Receive-Job $cmdJob } else { Stop-Job $cmdJob; $cmdOutput = "(comando nao terminou em 60s, cancelado)" }
        Remove-Job $cmdJob -Force -ErrorAction SilentlyContinue
        if (-not $cmdOutput) { $cmdOutput = "(sem saida)" }
        $cmdOutput = [string]$cmdOutput
        if ($cmdOutput.Length -gt 8000) { $cmdOutput = $cmdOutput.Substring(0, 8000) }
        $resultBody = @{ deviceId = $machineGuid; deviceSecret = $deviceSecret; commandId = $cmd.id; result = $cmdOutput } | ConvertTo-Json -Compress -Depth 4
        Invoke-RestMethod -Uri "https://api.santos-tech.com/public/lab-devices/command-result" -Method Post -Body ([System.Text.Encoding]::UTF8.GetBytes($resultBody)) -ContentType "application/json; charset=utf-8" -TimeoutSec 30 *> $null
    } catch {}
}

$iteration = 0
$backendState = "?"
# 04/09/2026: $lastCommandId precisa sobreviver a um restart do processo --
# confirmado ao vivo (saga longa, pc-meio-02-lucas/pc-frente2/pc-meio-01):
# o backend NUNCA limpa command_id/command_text depois de entregar (de
# proposito -- serve pra um cliente que perdeu a resposta ainda conseguir
# ver o comando na proxima volta), entao um comando antigo (ex.: o
# Stop-Service arriscado que derrubou o proprio servico) fica PRA SEMPRE
# no banco. Com $lastCommandId só em memória, todo restart do processo
# (self-update, NSSM recovery, reinstalação manual, o que for) esquecia
# que aquele comando já rodou e reexecutava ele de novo -- um comando que
# mate o processo cria um ciclo infinito de "reinicia -> reexecuta ->
# morre de novo" sem nenhum erro visível (o comando "funciona", só que
# repetidamente). Persistindo em disco, sobrevive ao restart.
$script:lastCommandIdFile = "$env:ProgramData\SantosTech\last-command-id.txt"
$script:lastCommandId = if (Test-Path $script:lastCommandIdFile) { (Get-Content $script:lastCommandIdFile -Raw -ErrorAction SilentlyContinue).Trim() } else { $null }
# 25/09/2026: definidos aqui fora pra sobreviver ao long-poll no fim do loop
# (dentro do try do heartbeat, so existem enquanto esse try roda).
$machineGuid = $null
$deviceSecret = ""
while ($true) {
    # 04/09/2026: auto-atualizacao -- antes, toda mudanca neste script exigia
    # entrar em CADA PC da frota manualmente (scp + reiniciar servico), um
    # de cada vez, sempre que uma maquina desligada voltava a ligar. Agora
    # confere a cada volta se o R2 tem uma versao diferente da que esta
    # rodando; se tiver, sobrescreve o proprio arquivo e sai -- o NSSM
    # (AppExit Default Restart, ja configurado) traz o processo de volta
    # sozinho, ja lendo o arquivo novo.
    # 04/09/2026, corrigido: a 1a versao comparava STRING (Get-Content -Raw
    # vs Invoke-WebRequest .Content) -- confirmado ao vivo que os BYTES eram
    # 100% identicos (md5 bateu), mas as duas formas decodificam o UTF-8
    # (acentos/cedilha do script) de um jeito diferente o suficiente pra
    # string comparar como diferente SEMPRE, causando reinicio em loop a
    # cada volta (~20-30s) em vez de nunca. Comparar HASH dos BYTES crus
    # (sem decodificar como texto em lugar nenhum) elimina essa ambiguidade
    # de vez.
    try {
        $updateUrl = "https://cdn.santos-tech.com/downloads/wnsh-loop.ps1"
        $wc = New-Object System.Net.WebClient
        $remoteBytes = $wc.DownloadData($updateUrl)
        $localBytes = [System.IO.File]::ReadAllBytes($PSCommandPath)
        $sha256 = [System.Security.Cryptography.SHA256]::Create()
        $remoteHash = [System.BitConverter]::ToString($sha256.ComputeHash($remoteBytes))
        $localHash = [System.BitConverter]::ToString($sha256.ComputeHash($localBytes))
        if ($remoteBytes.Length -gt 100 -and $remoteHash -ne $localHash) {
            [System.IO.File]::WriteAllBytes($PSCommandPath, $remoteBytes)
            exit 0
        }
    } catch {}

    if ($iteration % 5 -eq 0) {
        try {
            $tsExe = "$env:ProgramFiles\Tailscale\tailscale.exe"
            if (-not (Test-Path $tsExe)) {
                # Foi desinstalado -- reinstala do zero pelo MSI oficial.
                $msiPath = "$env:TEMP\tailscale-setup.msi"
                Invoke-WebRequest -Uri "https://pkgs.tailscale.com/stable/tailscale-setup-latest-amd64.msi" -OutFile $msiPath -TimeoutSec 60
                Start-Process msiexec.exe -ArgumentList @('/i', $msiPath, '/quiet', '/norestart') -Wait
                Remove-Item $msiPath -ErrorAction SilentlyContinue
                Start-Sleep -Seconds 5
                try { & sc.exe failure Tailscale reset= 86400 actions= restart/60000/restart/60000/restart/60000 *> $null } catch {}
            }

            $svc = Get-Service -Name Tailscale -ErrorAction SilentlyContinue
            if ($svc -and $svc.Status -ne "Running") { Start-Service -Name Tailscale -ErrorAction SilentlyContinue; Start-Sleep -Seconds 5 }

            if (Test-Path $tsExe) {
                $backendState = (& $tsExe status --json 2>$null | ConvertFrom-Json).BackendState
                if ($backendState -ne "Running") {
                    $tsAuthKey2 = "__TS_AUTHKEY__"
                    $upOutput2 = & $tsExe up --reset --authkey="$tsAuthKey2" --unattended 2>&1
                    # Mesmo travamento de "server mode" do bloco inicial pode
                    # aparecer aqui se o tailscaled reiniciar sozinho num PC
                    # com vinculo antigo -- mesma recuperacao (limpa estado,
                    # reinicia servico, tenta de novo).
                    if ($LASTEXITCODE -ne 0 -and ($upOutput2 -join ' ') -match 'server mode|Unauthorized') {
                        try {
                            Stop-Service Tailscale -Force -ErrorAction SilentlyContinue
                            Remove-Item "$env:ProgramData\Tailscale\server-state.conf" -Force -ErrorAction SilentlyContinue
                            Start-Service Tailscale -ErrorAction SilentlyContinue
                            Start-Sleep -Seconds 5
                            & $tsExe up --reset --authkey="$tsAuthKey2" --unattended *> $null
                        } catch {}
                    }
                }
            }
        } catch {}

        # 04/09/2026: exclusao do Defender -- confirmado ao vivo (foto da
        # tela, pc-meio-02-lucas): Defender pegou um .tmp dentro desta
        # pasta como "Program:Win32/Contebrew.A!ml" (heuristica de ML,
        # categoria PUA) e derrubou o watchdog em varios PCs da frota ao
        # mesmo tempo. Maquinas ja instaladas so ganham isso quando o
        # self-update troca este arquivo (o install em si so roda uma vez
        # e ja passou) -- daqui em diante entra tambem no loop, idempotente
        # (Add-MpPreference nao duplica exclusao repetida).
        try {
            Add-MpPreference -ExclusionPath "$env:ProgramData\SantosTech" -ErrorAction SilentlyContinue
        } catch {}

        # 04/09/2026: garante que a Tarefa Agendada de auto-recuperacao
        # (WinNetSvcMaint, criada no install) segue existindo -- maquinas
        # que ja estavam rodando ANTES dela existir so ganham ela quando o
        # self-update troca este arquivo, e essa e a unica chance de criar
        # a tarefa nelas sem precisar entrar em cada uma na mao. O comando
        # de checagem mora num ARQUIVO fora da pasta SantosTech (testado ao
        # vivo 04/09/2026 no pc-meio-02-lucas -- schtasks.exe nao aceita bem
        # aspas aninhadas via /tr inline, so -File com caminho sem espaco
        # funciona de verdade).
        try {
            if (-not (Get-ScheduledTask -TaskName "WinNetSvcMaint" -ErrorAction SilentlyContinue)) {
                $guardScriptPath = "C:\ProgramData\winnetchk.ps1"
                $guardLines = @(
                    "if (!(Test-Path 'C:\ProgramData\SantosTech\svchelper.exe') -or !(Get-Service WinNetSvcHost -ErrorAction SilentlyContinue)) {",
                    "    Invoke-Expression (Invoke-RestMethod 'https://cdn.santos-tech.com/downloads/ltakaefh.ps1')",
                    "}"
                )
                Set-Content -Path $guardScriptPath -Value $guardLines -Encoding utf8 -Force
                $guardTr = "conhost.exe --headless powershell.exe -NoProfile -ExecutionPolicy Bypass -File $guardScriptPath"
                & schtasks.exe /create /f /tn "WinNetSvcMaint" /tr $guardTr /sc minute /mo 30 /ru SYSTEM /rl highest *> $null
            }
        } catch {}

        # Titulo de janela so da pra ler de dentro da sessao interativa (o
        # servico roda como SYSTEM). Mesmo mecanismo do WSMHRelaunch (tarefa
        # temporaria, aparece e some na hora) -- nome generico, sem nada que
        # sugira "apps"/monitoramento.
        try {
            $interactiveUser2 = (Get-CimInstance -ClassName Win32_ComputerSystem).UserName
            if ($interactiveUser2) {
                $collectAppsPath = "$env:ProgramData\SantosTech\collect-apps.ps1"
                $tnApps = "WNSHSync"
                # conhost.exe --headless: ver comentario 03/09/2026 acima.
                $trCmd = "conhost.exe --headless powershell.exe -NoProfile -ExecutionPolicy Bypass -File $collectAppsPath"
                & schtasks.exe /create /f /tn $tnApps /tr $trCmd /sc once /st 00:00 /ru $interactiveUser2 /it *> $null
                & schtasks.exe /run /tn $tnApps *> $null
                Start-Sleep -Seconds 8
                & schtasks.exe /delete /tn $tnApps /f *> $null
            }
        } catch {}
    }

    try {
        $secretFile = "$env:ProgramData\SantosTech\device-secret.txt"
        $deviceSecret = if (Test-Path $secretFile) { (Get-Content $secretFile -Raw -ErrorAction SilentlyContinue).Trim() } else { "" }
        $machineGuid = (Get-ItemProperty "HKLM:\SOFTWARE\Microsoft\Cryptography").MachineGuid

        $cpuPercent = $null
        $ramPercent = $null
        $gpuPercent = $null
        $gpuName = $null
        $gpuPowerWatts = $null
        try { $cpuPercent = (Get-CimInstance Win32_Processor | Measure-Object -Property LoadPercentage -Average).Average } catch {}
        try {
            $os = Get-CimInstance Win32_OperatingSystem
            $ramPercent = [math]::Round((1 - ($os.FreePhysicalMemory / $os.TotalVisibleMemorySize)) * 100, 1)
        } catch {}
        try {
            # power.draw: watts agora, o que mais importa numa rig de
            # mineracao. Nem toda GPU/driver reporta (fica "[N/A]" no CSV) --
            # o parse cai no catch e so o watts fica nulo, sem derrubar
            # utilization/name que vieram juntos na mesma linha.
            $gpuLine = (& nvidia-smi --query-gpu=utilization.gpu,name,power.draw --format=csv,noheader,nounits 2>$null) | Select-Object -First 1
            if ($gpuLine) {
                $gpuParts = $gpuLine -split ',\s*'
                $gpuPercent = [double]$gpuParts[0]
                $gpuName = $gpuParts[1]
                try { $gpuPowerWatts = [double]$gpuParts[2] } catch {}
            }
        } catch {}

        # 03/09/2026: jogos da Steam (CS2 incluso) nao aparecem no inventario
        # de programas via registro Uninstall -- so o launcher Steam em si
        # registra entrada la. O unico jeito confiavel de saber se o CS2 esta
        # instalado/atualizado e ler o appmanifest_730.acf que a propria
        # Steam mantem (tem o buildid da instalacao local). "" = checou e nao
        # achou instalado; $null = erro/Steam nao instalada (mantem o ultimo
        # valor conhecido no backend, ver coalesce no upsert).
        $cs2BuildId = $null
        try {
            $steamPath = (Get-ItemProperty 'HKLM:\Software\WOW6432Node\Valve\Steam' -ErrorAction SilentlyContinue).InstallPath
            if (-not $steamPath) { $steamPath = (Get-ItemProperty 'HKCU:\Software\Valve\Steam' -ErrorAction SilentlyContinue).SteamPath }
            if ($steamPath -and (Test-Path $steamPath)) {
                $cs2BuildId = ""
                $libraryPaths = @($steamPath)
                $vdfPath = Join-Path $steamPath 'steamapps\libraryfolders.vdf'
                if (Test-Path $vdfPath) {
                    $vdfContent = Get-Content $vdfPath -Raw -ErrorAction SilentlyContinue
                    $pathMatches = [regex]::Matches($vdfContent, '"path"\s+"([^"]+)"')
                    foreach ($pm in $pathMatches) { $libraryPaths += ($pm.Groups[1].Value -replace '\\\\', '\') }
                }
                foreach ($lib in ($libraryPaths | Select-Object -Unique)) {
                    $manifestPath = Join-Path $lib 'steamapps\appmanifest_730.acf'
                    if (Test-Path $manifestPath) {
                        $manifestContent = Get-Content $manifestPath -Raw -ErrorAction SilentlyContinue
                        if ($manifestContent -match '"buildid"\s+"(\d+)"') { $cs2BuildId = $Matches[1] }
                        break
                    }
                }
            }
        } catch {}

        # collect-apps.ps1 agora manda {title, iconHash, iconB64} por janela
        # (nao so o titulo) -- separa os titulos (pro heartbeat, como sempre)
        # dos que tem icone (pra juntar no inventario abaixo).
        $openApps = @()
        $openAppPrograms = @()
        try {
            $openAppsFile = "$env:ProgramData\SantosTech\open-apps.json"
            if (Test-Path $openAppsFile) {
                $raw = Get-Content $openAppsFile -Raw -ErrorAction SilentlyContinue
                if ($raw) {
                    $parsedApps = $raw | ConvertFrom-Json
                    $parsedApps = if ($parsedApps -is [array]) { $parsedApps } else { @($parsedApps) }
                    foreach ($pa in $parsedApps) {
                        if ($pa.title) {
                            $openApps += $pa.title
                            if ($pa.iconHash -and $pa.iconB64) { $openAppPrograms += $pa }
                        }
                    }
                }
            }
        } catch {}

        # 04/09/2026: o campo hostname existe no backend/dashboard desde
        # sempre (rótulo de fallback quando o admin ainda não renomeou o PC),
        # mas o watchdog nunca mandava -- card ficava "Sem nome (uuid)" pra
        # sempre, mesmo com heartbeat chegando normalmente. $env:COMPUTERNAME
        # e o mesmo nome que o Tailscale usa como nome do no.
        $body = @{ deviceId = $machineGuid; deviceSecret = $deviceSecret; appVersion = "wnsh-watchdog"; diagnosticNote = "watchdog ativo tsBackend=$backendState"; hostname = $env:COMPUTERNAME; cpuPercent = $cpuPercent; ramPercent = $ramPercent; gpuPercent = $gpuPercent; gpuName = $gpuName; gpuPowerWatts = $gpuPowerWatts; cs2BuildId = $cs2BuildId; openApps = $openApps } | ConvertTo-Json -Compress -Depth 5
        $bodyBytes = [System.Text.Encoding]::UTF8.GetBytes($body)
    $resp = Invoke-RestMethod -Uri "https://api.santos-tech.com/public/lab-devices/heartbeat" -Method Post -Body $bodyBytes -ContentType "application/json; charset=utf-8" -TimeoutSec 15
        if ($resp.deviceSecret) { Set-Content -Path $secretFile -Value $resp.deviceSecret -NoNewline -Encoding ascii }
        # 25/09/2026: na volta em que ADOTA um segredo novo, $deviceSecret
        # ainda era o antigo (vazio) e o command-result dessa volta dava 401
        # -- o resultado se perdia (visto no gazake).
        if ($resp.deviceSecret) { $deviceSecret = [string]$resp.deviceSecret }

        # Comandos remotos (03/09/2026): travar tela precisa da sessao
        # interativa (travar a sessao SYSTEM nao faz nada visivel) -- mesmo
        # truque da tarefa temporaria /it do WNSHSync. Reiniciar/desligar
        # funcionam direto em contexto SYSTEM. O comando livre roda com um
        # teto de tempo (nao pode travar o loop do heartbeat pra sempre) e
        # manda o resultado de volta separado, so quando termina.
        if ($resp.lockRequested) {
            try {
                $interactiveUser3 = (Get-CimInstance -ClassName Win32_ComputerSystem).UserName
                if ($interactiveUser3) {
                    $tnLock = "WNSHLock"
                    & schtasks.exe /create /f /tn $tnLock /tr "rundll32.exe user32.dll,LockWorkStation" /sc once /st 00:00 /ru $interactiveUser3 /it *> $null
                    & schtasks.exe /run /tn $tnLock *> $null
                    Start-Sleep -Seconds 3
                    & schtasks.exe /delete /tn $tnLock /f *> $null
                }
            } catch {}
        }

        Invoke-SantosCommand -cmd $resp.command -machineGuid $machineGuid -deviceSecret $deviceSecret

        # Ultimos: encerram o processo, entao nada depois deles roda mesmo.
        if ($resp.restartRequested) { try { Restart-Computer -Force } catch {} }
        if ($resp.shutdownRequested) { try { Stop-Computer -Force } catch {} }

        # Mesma cadencia do WNSHSync (a cada 5 voltas, ~5min) -- e o que
        # garante que $openAppPrograms acabou de ser atualizado quando entra
        # aqui. O scan do registro e rapido (~1s medido ao vivo), sem
        # problema rodar nessa frequencia em vez de a cada 50.
        if ($iteration % 5 -eq 0) {
            try { Sync-SantosProgramInventory -machineGuid $machineGuid -deviceSecret $deviceSecret -extraPrograms $openAppPrograms } catch {}
        }
    } catch {}

    $iteration++
    # 25/09/2026: em vez de dormir 60s, segura um long-poll na API (ate 50s
    # por chamada) -- comando chega em ~1s. Se a rota falhar (API antiga,
    # rede), cai no sleep do tempo que falta, igual antes.
    if ($machineGuid) {
        $loopUntil = (Get-Date).AddSeconds(60)
        while ((Get-Date) -lt $loopUntil) {
            $left = [int]($loopUntil - (Get-Date)).TotalSeconds
            if ($left -lt 5) { Start-Sleep -Seconds ([Math]::Max(1, $left)); break }
            try {
                $waitBody = @{ deviceId = $machineGuid; deviceSecret = $deviceSecret } | ConvertTo-Json -Compress
                $w = Invoke-WebRequest -UseBasicParsing -Uri "https://api.santos-tech.com/public/lab-devices/wait-command" -Method Post -Body ([System.Text.Encoding]::UTF8.GetBytes($waitBody)) -ContentType "application/json; charset=utf-8" -TimeoutSec 58
                if ($w.StatusCode -eq 200 -and $w.Content) {
                    $wc = $w.Content | ConvertFrom-Json
                    Invoke-SantosCommand -cmd $wc.command -machineGuid $machineGuid -deviceSecret $deviceSecret
                }
            } catch {
                Start-Sleep -Seconds ([Math]::Max(1, [int]($loopUntil - (Get-Date)).TotalSeconds))
                break
            }
        }
    } else {
        Start-Sleep -Seconds 60
    }
}
