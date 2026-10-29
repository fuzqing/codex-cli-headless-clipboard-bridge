param([string]$OutputDirectory = (Join-Path $PSScriptRoot '..\dist\release'))
$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path $PSScriptRoot -Parent
$outputPath = [System.IO.Path]::GetFullPath($OutputDirectory)
$env:GOOS = 'windows'
$env:GOARCH = 'amd64'
$env:CGO_ENABLED = '0'

Push-Location $projectRoot
try {
    New-Item -ItemType Directory -Path $outputPath -Force | Out-Null
    $exePath = Join-Path $outputPath 'clipbridge.exe'
    go build -trimpath -ldflags '-s -w' -o $exePath .
    if ($LASTEXITCODE -ne 0) { throw 'Windows build failed' }
    $packageFiles = @($exePath)
    foreach ($file in @('config.example.json', 'README.md', 'TEST_REPORT.md', 'THIRD_PARTY_NOTICES.md')) {
        $packageFiles += Join-Path $projectRoot $file
    }
    $zipPath = Join-Path $outputPath 'clipbridge-windows-amd64.zip'
    Compress-Archive -LiteralPath $packageFiles -DestinationPath $zipPath -Force
    @($exePath, $zipPath) | ForEach-Object {
        $hash = (Get-FileHash -LiteralPath $_ -Algorithm SHA256).Hash.ToLowerInvariant()
        '{0}  {1}' -f $hash, (Split-Path $_ -Leaf)
    } | Set-Content -LiteralPath (Join-Path $outputPath 'SHA256SUMS.txt') -Encoding ascii
    Write-Output ('Release files: {0}' -f $outputPath)
} finally { Pop-Location }
