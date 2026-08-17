# Shared helpers for the Windows desktop build scripts.
#
# Dot-sourced, not run: `. "$PSScriptRoot\common.ps1"`.
#
# The macOS scripts repeat their own `log` and `fetch` because bash makes
# sharing awkward; PowerShell does not, and four scripts is enough callers that
# one copy of "download, verify, fail loudly" is the honest shape.

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

# Repo root, from this file's location.
$script:RepoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path
$script:Stage = Join-Path $RepoRoot 'build\desktop\.stage-windows'
$script:Work = Join-Path $Stage '.work'

function Write-Step {
    param([Parameter(Mandatory)][string]$Message)
    Write-Host "==> $Message" -ForegroundColor Blue
}

# Invoke-Native runs an external program, shows its output, and fails the build
# when the program fails. It RUNS a program; it never captures one -- a caller
# that wants the output calls the executable directly (see ConvertTo-MsysPath).
#
# PowerShell's $ErrorActionPreference does not apply to native executables: a
# compiler that exits 1 leaves the script running happily, and the first sign
# of trouble is a missing file three steps later. Every external call in this
# lane goes through here so a failure stops where it happened.
#
# Out-Host is what keeps the contract above true. A native command's stdout goes
# to the SUCCESS stream, and a PowerShell function returns everything on that
# stream -- not just what it names in `return`. So a compiler's entire output
# would become part of the return value of whatever function ran it, and the
# caller assigning that to a [string] parameter gets "Cannot convert value to
# type System.String" from a build that in fact just succeeded. Out-Host writes
# to the console and puts nothing on the stream.
function Invoke-Native {
    param(
        [Parameter(Mandatory)][string]$What,
        [Parameter(Mandatory)][string]$Exe,
        [Parameter(ValueFromRemainingArguments)][string[]]$Arguments
    )
    & $Exe @Arguments | Out-Host
    if ($LASTEXITCODE -ne 0) {
        throw "$What failed (exit $LASTEXITCODE): $Exe $($Arguments -join ' ')"
    }
}

# Get-Pinned downloads Url to Destination once, and verifies its SHA-256 EVERY
# time -- so a truncated or tampered file cached in .work can never silently
# reach a build, which is the whole reason the checksums are pinned in the
# first place.
function Get-Pinned {
    param(
        [Parameter(Mandatory)][string]$Url,
        [Parameter(Mandatory)][string]$Destination,
        [Parameter(Mandatory)][string]$Sha256
    )
    if (-not (Test-Path $Destination)) {
        Write-Step "downloading $(Split-Path $Destination -Leaf)"
        $partial = "$Destination.part"
        # The progress bar costs more than the download on a fast link, and it
        # renders as noise in a build log.
        $previous = $ProgressPreference
        $ProgressPreference = 'SilentlyContinue'
        try {
            Invoke-WebRequest -Uri $Url -OutFile $partial -UseBasicParsing
        } finally {
            $ProgressPreference = $previous
        }
        Move-Item -Force $partial $Destination
    }

    $actual = (Get-FileHash -Algorithm SHA256 -Path $Destination).Hash.ToLowerInvariant()
    if ($actual -ne $Sha256.ToLowerInvariant()) {
        throw @"
checksum mismatch for $Destination
  expected $Sha256
  actual   $actual
"@
    }
}

# Get-VsDevCmd locates the Visual Studio developer command file.
#
# pgvector has no build system other than nmake against the MSVC toolchain, so
# this is a hard requirement rather than a nicety, and saying so by name beats
# letting `nmake` fail as "not recognized".
function Get-VsDevCmd {
    $vswhere = Join-Path ${env:ProgramFiles(x86)} 'Microsoft Visual Studio\Installer\vswhere.exe'
    if (-not (Test-Path $vswhere)) {
        throw @'
Visual Studio Build Tools are not installed (vswhere.exe not found).
pgvector must be compiled with MSVC against the exact Postgres it loads into.
Install the "Desktop development with C++" workload:
  https://visualstudio.microsoft.com/downloads/  ->  Build Tools for Visual Studio
'@
    }
    $root = & $vswhere -latest -products '*' `
        -requires Microsoft.VisualStudio.Component.VC.Tools.x86.x64 `
        -property installationPath
    if (-not $root) {
        throw @'
Visual Studio is installed but without the C++ toolset.
Add the "Desktop development with C++" workload in the Visual Studio Installer.
'@
    }
    $devCmd = Join-Path $root 'Common7\Tools\VsDevCmd.bat'
    if (-not (Test-Path $devCmd)) {
        throw "found Visual Studio at $root but no Common7\Tools\VsDevCmd.bat"
    }
    return $devCmd
}

# Invoke-InVsShell runs one cmd.exe command line inside the x64 MSVC
# environment. The environment cannot be imported into this session and made to
# stick, so the whole command runs in the shell that has it.
function Invoke-InVsShell {
    param(
        [Parameter(Mandatory)][string]$What,
        [Parameter(Mandatory)][string]$Command
    )
    $devCmd = Get-VsDevCmd
    Invoke-Native $What 'cmd.exe' '/c' "call `"$devCmd`" -arch=amd64 -host_arch=amd64 >nul && $Command"
}

function Get-DirectorySize {
    param([Parameter(Mandatory)][string]$Path)
    $bytes = (Get-ChildItem -Recurse -File -LiteralPath $Path | Measure-Object -Property Length -Sum).Sum
    return '{0:N0} MB' -f ($bytes / 1MB)
}
