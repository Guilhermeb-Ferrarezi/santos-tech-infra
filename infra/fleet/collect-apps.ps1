Add-Type -AssemblyName System.Drawing -ErrorAction SilentlyContinue
$procs = Get-Process | Where-Object { $_.MainWindowTitle -ne "" }
$results = @()
foreach ($p in $procs) {
    $iconHash = $null
    $iconB64 = $null
    try {
        $exePath = $p.MainModule.FileName
        if ($exePath -and (Test-Path -LiteralPath $exePath -PathType Leaf)) {
            $srcIcon = [System.Drawing.Icon]::ExtractAssociatedIcon($exePath)
            if ($srcIcon) {
                $srcBmp = $srcIcon.ToBitmap()
                $bmp = New-Object System.Drawing.Bitmap 48, 48
                $g = [System.Drawing.Graphics]::FromImage($bmp)
                $g.DrawImage($srcBmp, 0, 0, 48, 48)
                $ms = New-Object System.IO.MemoryStream
                $bmp.Save($ms, [System.Drawing.Imaging.ImageFormat]::Png)
                $pngBytes = $ms.ToArray()
                $g.Dispose(); $bmp.Dispose(); $srcBmp.Dispose(); $ms.Dispose(); $srcIcon.Dispose()
                if ($pngBytes.Length -gt 0 -and $pngBytes.Length -le 32768) {
                    $sha256 = [System.Security.Cryptography.SHA256]::Create()
                    $hashBytes = $sha256.ComputeHash($pngBytes)
                    $iconHash = [BitConverter]::ToString($hashBytes).Replace("-", "").ToLower()
                    $iconB64 = [Convert]::ToBase64String($pngBytes)
                }
            }
        }
    } catch {}
    $results += [PSCustomObject]@{ title = $p.MainWindowTitle; iconHash = $iconHash; iconB64 = $iconB64 }
}
$results | ConvertTo-Json -Compress -Depth 4 | Set-Content -Path "$env:ProgramData\SantosTech\open-apps.json" -Encoding UTF8
