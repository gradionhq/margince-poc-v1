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

# Get-VsShellOutput runs a command in the MSVC environment and RETURNS its
# output, where Invoke-InVsShell only shows it. Two functions rather than a
# switch: a helper that sometimes returns output is the shape that put make's
# entire log into a return value once already.
function Get-VsShellOutput {
    param(
        [Parameter(Mandatory)][string]$What,
        [Parameter(Mandatory)][string]$Command
    )
    $devCmd = Get-VsDevCmd
    $out = & cmd.exe '/c' "call `"$devCmd`" -arch=amd64 -host_arch=amd64 >nul && $Command"
    if ($LASTEXITCODE -ne 0) {
        throw "$What failed (exit $LASTEXITCODE): $Command"
    }
    return $out
}

# DLLs Windows itself provides. A binary asking for one of these is satisfied by
# the operating system, so their absence from the folder is never a defect.
# Matched as prefixes, which is what covers the api-ms-win-* CRT façade set.
$script:SystemDlls = @(
    'kernel32', 'advapi32', 'user32', 'ws2_32', 'shell32', 'secur32', 'netapi32',
    'userenv', 'version', 'crypt32', 'dbghelp', 'wldap32', 'bcrypt', 'ole32',
    'oleaut32', 'gdi32', 'comdlg32', 'winspool', 'wsock32', 'iphlpapi', 'msvcrt',
    'api-ms-win', 'rpcrt4', 'shlwapi', 'psapi', 'winmm', 'comctl32', 'imm32',
    'uxtheme', 'dwmapi', 'setupapi', 'cfgmgr32', 'powrprof', 'ntdll', 'pdh',
    'oleacc', 'msimg32', 'ktmw32', 'mpr', 'winhttp', 'wintrust', 'authz'
)

# Test-NativeDependencies fails the build when a binary in Directory imports a
# DLL that is neither beside it nor supplied by Windows.
#
# THIS IS THE CHECK THAT WAS MISSING. Running `postgres.exe --version` proves the
# binary starts ON THE BUILD MACHINE, and a build machine has the MSVC runtime
# because the C++ workload this lane requires installs it system-wide. So the
# bundle shipped without vcruntime140.dll and every Postgres binary failed on a
# clean Windows with 0xC0000135 (STATUS_DLL_NOT_FOUND) -- a status code that
# names no file. Reading the import tables asks the question the smoke test
# cannot: not "does it run here" but "does the FOLDER carry what it needs".
function Test-NativeDependencies {
    param(
        [Parameter(Mandatory)][string]$Directory,
        [string[]]$Ignore = @()
    )
    Write-Step "checking the import tables under $(Split-Path $Directory -Leaf)"

    $present = @{}
    foreach ($f in Get-ChildItem -LiteralPath $Directory -File) {
        $present[$f.Name.ToLowerInvariant()] = $true
    }

    $binaries = Get-ChildItem -LiteralPath $Directory -File |
        Where-Object { $_.Extension -in @('.exe', '.dll') } |
        Where-Object { $_.Name -notin $Ignore }
    if ($binaries.Count -eq 0) {
        throw "no binaries under $Directory to check -- this gate cannot pass by scanning nothing"
    }

    $missing = @{}
    foreach ($bin in $binaries) {
        $out = Get-VsShellOutput "reading the imports of $($bin.Name)" `
            "dumpbin /nologo /dependents `"$($bin.FullName)`""
        foreach ($line in $out) {
            if ($line -notmatch '^\s{4}(\S+\.dll)\s*$') { continue }
            $dll = $Matches[1]
            $low = $dll.ToLowerInvariant()
            if ($present.ContainsKey($low)) { continue }
            if ($SystemDlls | Where-Object { $low.StartsWith($_) }) { continue }
            if (-not $missing.ContainsKey($dll)) { $missing[$dll] = @() }
            $missing[$dll] += $bin.Name
        }
    }

    if ($missing.Count -gt 0) {
        $report = ($missing.GetEnumerator() | Sort-Object Key | ForEach-Object {
                "  $($_.Key) -- needed by $($_.Value.Count) file(s), e.g. $($_.Value[0])"
            }) -join "`n"
        throw @"
the bundle is missing $($missing.Count) DLL(s) it imports:

$report

They are neither in the folder nor supplied by Windows, so every binary that
needs one fails on a clean machine with 0xC0000135 and no file named. Ship them
beside the executables.
"@
    }
    Write-Step "every imported DLL is either in the folder or supplied by Windows"
}
