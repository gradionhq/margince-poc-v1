# Assemble the distributable Windows folder from what the other build scripts
# staged.
#
# Run build-postgres.ps1, build-bus.ps1 and build-app.ps1 first; this only
# stages what already exists, so a missing input is an error rather than a
# silently incomplete build.
#
# The output layout is the update contract, and it is the same one the macOS
# bundle uses so that one document describes both:
#
#   margince\
#   |-- margince.exe             replaced by an update
#   |-- Start Margince.cmd       replaced by an update
#   |-- runtime\                 replaced by an update
#   |-- margince.yaml            the user's - created on first run
#   |-- margince.env             the user's - created on first run
#   \-- data\                    the user's - database, logs, uploads
#
# Only the first three are shipped. An update replaces exactly those and leaves
# the rest, so "copy the new files over the old folder" cannot destroy the
# records the installation exists to hold.
. "$PSScriptRoot\common.ps1"

$dist = Join-Path $RepoRoot 'build\desktop\margince-windows'

function Test-Input {
    param([Parameter(Mandatory)][string]$Path, [Parameter(Mandatory)][string]$Hint)
    if (-not (Test-Path $Path)) {
        throw "missing $Path -- run $Hint first"
    }
}

# Write-Starter writes the double-clickable entry point.
#
# `start` rather than running the launcher inline: a batch file that hosts a
# child process answers Ctrl-C with "Terminate batch job (Y/N)?", which is a
# question a non-technical user should never be asked in order to quit an app.
# Handing the launcher its own console window puts Ctrl-C where it belongs.
function Write-Starter {
    $starter = @'
@echo off
rem Double-click this file to start Margince.
cd /d "%~dp0"
start "Margince" "%~dp0margince.exe"
'@
    # ASCII, CRLF: a batch file is read by cmd.exe, which does not want a BOM.
    $bytes = [System.Text.Encoding]::ASCII.GetBytes(($starter -replace "`r?`n", "`r`n") + "`r`n")
    [System.IO.File]::WriteAllBytes((Join-Path $dist 'Start Margince.cmd'), $bytes)
}

function Copy-Dist {
    Write-Step 'assembling the distributable folder'
    Remove-Item -Recurse -Force -ErrorAction SilentlyContinue $dist
    $runtime = Join-Path $dist 'runtime'
    New-Item -ItemType Directory -Force -Path $runtime | Out-Null

    Copy-Item -Recurse (Join-Path $Stage 'pgsql') (Join-Path $runtime 'pgsql')
    # The bus's DLLs and licence files live beside its executable, because the
    # Windows loader resolves a DLL from the directory of the .exe that needs
    # it -- which is what makes the folder relocatable without any patching.
    Copy-Item -Recurse (Join-Path $Stage 'bus\*') $runtime
    foreach ($role in @('api', 'worker', 'migrate')) {
        Copy-Item (Join-Path $Stage "bin\$role.exe") $runtime
    }
    Copy-Item -Recurse (Join-Path $Stage 'web') (Join-Path $runtime 'web')
    Copy-Item (Join-Path $Stage 'bin\margince.exe') (Join-Path $dist 'margince.exe')
    Write-Starter
}

# Test-Dist runs the two third-party executables out of the ASSEMBLED folder.
#
# This is the Windows equivalent of the macOS lane's signature check, and it
# answers the question that actually matters here: does everything each binary
# needs sit inside the folder the user will copy? Building in one place and
# running in another is exactly how a missing DLL escapes notice.
function Test-Dist {
    Write-Step 'verifying the assembled folder runs standalone'
    $checks = @(
        @{ Name = 'postgres'; Path = 'runtime\pgsql\bin\postgres.exe' },
        @{ Name = 'the event bus'; Path = 'runtime\redis-server.exe' }
    )
    foreach ($check in $checks) {
        $exe = Join-Path $dist $check.Path
        if (-not (Test-Path $exe)) {
            throw "$($check.Name) is missing from the assembled folder ($($check.Path))"
        }
        & $exe --version | Out-Null
        if ($LASTEXITCODE -ne 0) {
            throw "$($check.Name) could not run from the assembled folder -- a runtime DLL did not travel with it"
        }
    }

    # The launcher refuses to start without these, and finding that out here
    # costs a second rather than a support conversation.
    foreach ($required in @('margince.exe', 'Start Margince.cmd', 'runtime\api.exe',
            'runtime\worker.exe', 'runtime\migrate.exe', 'runtime\web\index.html')) {
        if (-not (Test-Path (Join-Path $dist $required))) {
            throw "$required is missing from the assembled folder"
        }
    }
}

Test-Input (Join-Path $Stage 'pgsql\bin\postgres.exe') 'build-postgres.ps1'
Test-Input (Join-Path $Stage 'bus\redis-server.exe') 'build-bus.ps1'
Test-Input (Join-Path $Stage 'bin\api.exe') 'build-app.ps1'
Test-Input (Join-Path $Stage 'bin\margince.exe') 'build-app.ps1'
Test-Input (Join-Path $Stage 'web\index.html') 'build-app.ps1'

Copy-Dist
Test-Dist

Write-Step "built $dist ($(Get-DirectorySize $dist))"
Write-Step "run it:  `"$dist\margince.exe`""
