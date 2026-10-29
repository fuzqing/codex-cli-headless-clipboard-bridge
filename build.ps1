param([switch]$Background)
$ErrorActionPreference = 'Stop'
$env:GOCACHE = Join-Path $PSScriptRoot '.cache\go-build'
$env:GOMODCACHE = Join-Path $PSScriptRoot '.cache\go-mod'
$env:GOOS = 'windows'
$env:GOARCH = 'amd64'
Push-Location $PSScriptRoot
try {
    New-Item -ItemType Directory -Path dist -Force | Out-Null
    go test ./...
    if ($LASTEXITCODE -ne 0) { throw 'Tests failed' }
    if ($Background) {
        go build -trimpath -ldflags '-s -w -H=windowsgui' -o dist/clipbridge-background.exe .
    } else {
        go build -trimpath -o dist/clipbridge.exe .
    }
    if ($LASTEXITCODE -ne 0) { throw 'Build failed' }
} finally { Pop-Location }
