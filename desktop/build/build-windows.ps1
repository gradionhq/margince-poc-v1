# Build the whole Windows desktop folder.
#
# The equivalent of `make desktop` on macOS, and it exists because a Windows
# build host is not required to have GNU make. `make desktop-win*` from the
# repo root calls into the same scripts.
#
# Postgres and the bus are staged only when they are missing: unpacking a
# 310 MB archive and compiling Redis takes minutes and changes only when the
# pinned versions do, so a routine rebuild of the app must not pay for it.
# -Force rebuilds them anyway.
param([switch]$Force)

. "$PSScriptRoot\common.ps1"

$postgres = Join-Path $Stage 'pgsql\bin\postgres.exe'
$bus = Join-Path $Stage 'bus\redis-server.exe'

if ($Force -or -not (Test-Path $postgres)) {
    & "$PSScriptRoot\build-postgres.ps1"
} else {
    Write-Step 'reusing the staged postgres (build-windows.ps1 -Force to rebuild)'
}

if ($Force -or -not (Test-Path $bus)) {
    & "$PSScriptRoot\build-bus.ps1"
} else {
    Write-Step 'reusing the staged event bus (build-windows.ps1 -Force to rebuild)'
}

& "$PSScriptRoot\build-app.ps1"
& "$PSScriptRoot\build-dist.ps1"
