$ErrorActionPreference='Stop'
$u='santos-fleet'; $key='ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAILvjagsM6sER61aOHmLAsUYjZh+rn59J0ygAN+1Y2VQp guilherme@fleet-admin'
$d="$env:ProgramData\ssh"; $cfg="$d\sshd_config"; $ak="$d\administrators_authorized_keys"
$svc=Get-CimInstance Win32_Service -Filter "Name='sshd'"
if (-not $svc) { 'ERRO: sshd nao instalado'; exit 1 }
$sshd=$svc.PathName.Trim('"')
# 1. conta dedicada, senha aleatoria descartada, fora da tela de logon
if (-not (Get-LocalUser $u -ErrorAction SilentlyContinue)) {
  $b=New-Object byte[] 48; [Security.Cryptography.RandomNumberGenerator]::Create().GetBytes($b)
  $pw=ConvertTo-SecureString ([Convert]::ToBase64String($b)+'aA1!') -AsPlainText -Force
  New-LocalUser $u -Password $pw -PasswordNeverExpires -AccountNeverExpires -Description 'Acesso SSH da frota Santos Tech' | Out-Null
  'conta criada'
} else { 'conta ja existia' }
$adm=Get-LocalGroup -SID S-1-5-32-544
if (-not (Get-LocalGroupMember $adm | Where-Object { $_.Name -like "*\$u" })) { Add-LocalGroupMember $adm -Member $u }
$hk='HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion\Winlogon\SpecialAccounts\UserList'
New-Item $hk -Force | Out-Null; New-ItemProperty $hk -Name $u -Value 0 -PropertyType DWord -Force | Out-Null
# 2. ACL do ProgramData\ssh: so SYSTEM+Admins escrevem, usuarios autenticados so leem
icacls $d /inheritance:r /grant:r '*S-1-5-18:(OI)(CI)F' '*S-1-5-32-544:(OI)(CI)F' '*S-1-5-11:(OI)(CI)RX' /Q | Out-Null
# 3. chave (aditivo) + ACL estrita
$lines=@(); if (Test-Path $ak) { $lines=@(Get-Content $ak) }
if ($lines -notcontains $key) { $lines+=$key; Set-Content $ak $lines -Encoding ascii }
icacls $ak /inheritance:r /grant:r '*S-1-5-18:F' '*S-1-5-32-544:F' /Q | Out-Null; icacls $ak /setowner '*S-1-5-32-544' /Q | Out-Null
# 4. sshd_config: bloco Match User antes do Match Group (independe do idioma), com backup + validacao
$orig=Get-Content $cfg -Raw
if ($orig -notmatch "(?m)^Match User $u") {
  Copy-Item $cfg "$cfg.bak-santos-fleet" -Force; icacls "$cfg.bak-santos-fleet" /inheritance:r /grant:r "*S-1-5-18:F" "*S-1-5-32-544:F" /Q | Out-Null
  $blk="Match User $u`r`n       AuthorizedKeysFile __PROGRAMDATA__/ssh/administrators_authorized_keys`r`n       PasswordAuthentication no`r`n       KbdInteractiveAuthentication no`r`n"
  $m=[regex]::Match($orig,'(?m)^Match\s'); $new= if ($m.Success) { $orig.Insert($m.Index,$blk) } else { $orig+"`r`n"+$blk }
  Set-Content $cfg $new -Encoding ascii -NoNewline
  $t=& $sshd -t 2>&1
  if ($LASTEXITCODE -ne 0) { Copy-Item "$cfg.bak-santos-fleet" $cfg -Force; "ERRO sshd -t, config restaurado: $t"; exit 1 }
  'sshd_config ok'
} else { 'sshd_config ja tinha o bloco' }
# 5. firewall porta 22 em qualquer perfil
$r=Get-NetFirewallRule -Name 'OpenSSH-Server-In-TCP' -ErrorAction SilentlyContinue
if ($r) { Set-NetFirewallRule -Name 'OpenSSH-Server-In-TCP' -Profile Any -Enabled True } else { New-NetFirewallRule -Name 'OpenSSH-Server-In-TCP' -DisplayName 'OpenSSH Server (sshd)' -Protocol TCP -LocalPort 22 -Direction Inbound -Action Allow -Profile Any | Out-Null }
Set-Service sshd -StartupType Automatic; Restart-Service sshd -Force
"OK sshd=$((Get-Service sshd).Status) host=$env:COMPUTERNAME ts=$((& 'C:\Program Files\Tailscale\tailscale.exe' ip -4 2>$null) -join ',')"
(Get-Acl $d).Access | ForEach-Object { "acl $($_.IdentityReference) $($_.FileSystemRights)" }
