# Stage PostgreSQL 16 + pgvector for windows-x64.
#
# The macOS lane compiles Postgres from source because it has to: nothing
# prebuilt for macOS is relocatable, and making it so means rewriting Mach-O
# load commands and re-signing every file. Windows has neither problem. The
# loader resolves a DLL from the directory of the executable that needs it, so
# an extracted tree already runs from wherever it is put, and the upstream
# community build is a plain zip of exactly that tree.
#
# So this lane pins and verifies that zip instead of rebuilding it. What it
# CANNOT skip is pgvector: the extension must be compiled against the exact
# Postgres it loads into, no prebuilt Windows binary of it exists, and
# `CREATE EXTENSION vector` is migration 22 -- a Postgres without it fails on
# the user's first launch rather than degrading. That is the one compile step
# here, and it is why this needs MSVC.
#
# The other three extensions the schema requires -- unaccent, pg_trgm,
# btree_gist -- are contrib, and the upstream zip already ships them.
. "$PSScriptRoot\common.ps1"

$PG_VERSION = '16.14'
$PG_BUILD = '1'
$PG_SHA256 = '98af1417ba6a8dc30543e560e5407833a3b9e7cc7ed20e73b2006f3aa2f04663'
$PGVECTOR_VERSION = '0.8.6'
$PGVECTOR_SHA256 = '10bf9938906e5d643bbc4a7eea104b6f57ba4898e5b76b20e60484ea1d5a7f8f'

$out = Join-Path $Stage 'pgsql'

# The zip carries the whole product: pgAdmin, StackBuilder, debug symbols,
# documentation and the server headers. Only these three directories are the
# database, and the bundle is something a user downloads.
$keep = @('bin', 'lib', 'share')

function Expand-Postgres {
    $zip = Join-Path $Work "postgresql-$PG_VERSION-$PG_BUILD-windows-x64-binaries.zip"
    Get-Pinned `
        -Url "https://get.enterprisedb.com/postgresql/postgresql-$PG_VERSION-$PG_BUILD-windows-x64-binaries.zip" `
        -Destination $zip -Sha256 $PG_SHA256

    Write-Step "unpacking postgresql $PG_VERSION"
    $unpacked = Join-Path $Work 'pgsql-unpacked'
    Remove-Item -Recurse -Force -ErrorAction SilentlyContinue $unpacked
    Expand-Archive -LiteralPath $zip -DestinationPath $unpacked -Force

    # The archive holds one top-level pgsql/ directory.
    $inner = Join-Path $unpacked 'pgsql'
    if (-not (Test-Path (Join-Path $inner 'bin\postgres.exe'))) {
        throw "the archive did not contain pgsql\bin\postgres.exe -- its layout changed"
    }
    Remove-Item -Recurse -Force -ErrorAction SilentlyContinue $out
    Move-Item $inner $out
}

function Build-PgVector {
    $tar = Join-Path $Work "pgvector-$PGVECTOR_VERSION.tar.gz"
    Get-Pinned `
        -Url "https://github.com/pgvector/pgvector/archive/refs/tags/v$PGVECTOR_VERSION.tar.gz" `
        -Destination $tar -Sha256 $PGVECTOR_SHA256

    Write-Step "unpacking pgvector $PGVECTOR_VERSION"
    $src = Join-Path $Work "pgvector-$PGVECTOR_VERSION"
    Remove-Item -Recurse -Force -ErrorAction SilentlyContinue $src
    # tar has shipped in Windows since 1803, so this needs nothing installed.
    Invoke-Native 'unpacking pgvector' 'tar.exe' '-xzf' $tar '-C' $Work

    Write-Step 'building pgvector against the staged postgres'
    # PGROOT points at the tree we just staged, so vector.dll links against
    # that server's headers and import library and matches its ABI exactly.
    # Its Makefile.win installs into %PGROOT%\lib and %PGROOT%\share\extension.
    Invoke-InVsShell 'building pgvector' `
        "cd /d `"$src`" && set `"PGROOT=$out`" && nmake /F Makefile.win && nmake /F Makefile.win install"
}

# Remove-Extras prunes AFTER pgvector is built, because the compile needs
# include\server, which is not part of what ships.
function Remove-Extras {
    Write-Step 'pruning to the server tree'
    Get-ChildItem -LiteralPath $out -Force |
        Where-Object { $_.Name -notin $keep } |
        ForEach-Object { Remove-Item -Recurse -Force -LiteralPath $_.FullName }
}

# Copy-VcRuntime puts the Microsoft C runtime beside the binaries that need it.
#
# WHY THIS IS NOT OPTIONAL. Every PostgreSQL binary here imports
# VCRUNTIME140.dll, and postgres.exe and initdb.exe reach MSVCP140.dll through
# icuuc67.dll. Neither ships with Windows -- they arrive with the Visual C++
# redistributable, which a developer machine has because the C++ workload
# installs it and a user's machine very often does not. Without them the launcher
# dies on its first command with 0xC0000135, STATUS_DLL_NOT_FOUND, a status that
# names no file and reads like a corrupt download.
#
# App-local deployment is the documented, licensed way to ship these: the
# redistributable files list permits copying them beside the application, which
# is also what makes the folder self-contained rather than dependent on what the
# user installed years ago.
function Copy-VcRuntime {
    Write-Step 'copying the Visual C++ runtime beside the server binaries'
    $vswhere = Join-Path ${env:ProgramFiles(x86)} 'Microsoft Visual Studio\Installer\vswhere.exe'
    $vsRoot = & $vswhere -latest -products '*' `
        -requires Microsoft.VisualStudio.Component.VC.Tools.x86.x64 `
        -property installationPath
    if (-not $vsRoot) { throw 'could not locate Visual Studio to copy the C runtime from' }

    # The redist tree is versioned (VC\Redist\MSVC\<ver>\x64\Microsoft.VC14x.CRT).
    # Newest wins, which is the one the toolchain just linked against.
    $crt = Get-ChildItem -Path (Join-Path $vsRoot 'VC\Redist\MSVC') -Directory -ErrorAction SilentlyContinue |
        Sort-Object Name -Descending |
        ForEach-Object { Get-ChildItem -Path (Join-Path $_.FullName 'x64') -Directory -Filter 'Microsoft.VC*.CRT' -ErrorAction SilentlyContinue } |
        Select-Object -First 1
    if (-not $crt) {
        throw @'
no Visual C++ redistributable files found under the Visual Studio installation.
Install the "Desktop development with C++" workload, which includes them.
'@
    }

    # VCRUNTIME140_1 is not imported by anything the launcher runs, but it is
    # part of the same runtime and costs 40 KB; shipping the set rather than the
    # subset means a future Postgres build cannot need one we left behind.
    foreach ($dll in @('vcruntime140.dll', 'vcruntime140_1.dll', 'msvcp140.dll')) {
        $from = Join-Path $crt.FullName $dll
        if (-not (Test-Path $from)) { throw "the redistributable is missing $dll ($($crt.FullName))" }
        Copy-Item $from (Join-Path $out 'bin') -Force
    }
    Write-Step "copied the C runtime from $($crt.Name)"
}

# Remove-GuiExtras drops what the EDB tree carries for pgAdmin and StackBuilder.
#
# bin/ is kept wholesale because that is where Postgres keeps its own DLLs, and
# it also holds a wxWidgets GUI stack and a StackBuilder the bundle never starts:
# megabytes of code a user downloads and nothing runs, and the only files that
# import VCRUNTIME140_1. Named individually rather than pattern-swept, because a
# wildcard over bin/ is one typo away from deleting the server.
function Remove-GuiExtras {
    Write-Step 'dropping the pgAdmin and StackBuilder leftovers'
    $bin = Join-Path $out 'bin'
    Get-ChildItem -LiteralPath $bin -File |
        Where-Object { $_.Name -like 'wx*_vc_x64_custom.dll' -or $_.Name -eq 'stackbuilder.exe' } |
        ForEach-Object { Remove-Item -Force -LiteralPath $_.FullName }
}

function Test-Staged {
    Write-Step 'verifying the tree is self-contained'

    # The extensions are the reason this build exists at all; a missing control
    # file means the migrations fail on the user's first launch.
    foreach ($ext in @('vector', 'unaccent', 'pg_trgm', 'btree_gist')) {
        $control = Join-Path $out "share\extension\$ext.control"
        if (-not (Test-Path $control)) {
            throw "extension '$ext' is missing from the build ($control)"
        }
    }

    # Running the binary out of the staged tree is the only check that proves
    # the DLLs it needs travelled with it. A missing runtime dependency looks
    # exactly like a working build until someone else unpacks the folder.
    #
    # One blind spot this cannot cover: the Microsoft Visual C++ runtime is
    # installed machine-wide, and the C++ workload this build already requires
    # puts it there. So a missing redistributable passes here and fails on a
    # user's machine. It is on nearly every Windows install for that same
    # reason, which is why it is a documented limit rather than a bundled file
    # -- see the known limits in docs/explanation/desktop-distribution.md.
    Write-Step "postgres $(& (Join-Path $out 'bin\postgres.exe') --version)"
    if ($LASTEXITCODE -ne 0) {
        throw 'the staged postgres.exe could not run -- a runtime DLL is missing from the tree'
    }
    # Reading the import tables, not just starting one binary: the smoke test
    # above runs on a machine that HAS the C runtime, so it cannot see a folder
    # that lacks it. See Test-NativeDependencies.
    Test-NativeDependencies -Directory (Join-Path $out 'bin')

    Write-Step 'extensions present: vector unaccent pg_trgm btree_gist'
    Write-Step "size: $(Get-DirectorySize $out)"
}

New-Item -ItemType Directory -Force -Path $Work | Out-Null
Expand-Postgres
Build-PgVector
Remove-Extras
Remove-GuiExtras
Copy-VcRuntime
Test-Staged
