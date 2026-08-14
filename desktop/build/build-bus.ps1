# Build the event bus for windows-x64: Redis 7.2, from pinned upstream source.
#
# WHY REDIS AND NOT VALKEY. The macOS bundle ships Valkey because this binary
# is redistributed inside a BUSL-1.1 product and Redis 7.4 onward is
# RSALv2/SSPL. Valkey has no Windows build and upstream declines to add one,
# pointing Windows users at WSL -- which a bundle whose whole promise is "no
# prerequisites" cannot ask for. Redis 7.2 is the last BSD-3 line before the
# relicense and the exact lineage Valkey forked from, so it is redistributable
# on the same terms and speaks the protocol platform/events already uses.
#
# WHY NOT AN OLDER WINDOWS PORT. The long-standing native Windows ports are
# stuck on Redis 5.0. The outbox subscriber uses XAUTOCLAIM, which arrived in
# 6.2, so those builds would fail at runtime on the first stalled message
# rather than at the build -- the worst place to find out.
#
# WHY MSYS2. There is no MSVC-native Redis: it wants fork(), unix sockets and
# an event loop Windows does not have, and every working Windows build gets
# them from a POSIX emulation layer. That layer is one DLL, msys-2.0.dll,
# which travels beside redis-server.exe. It is LGPLv3 and is shipped
# unmodified with its license text -- see Copy-Licenses below, which is the
# obligation being met rather than described.
. "$PSScriptRoot\common.ps1"

$REDIS_VERSION = '7.2.15'
$REDIS_SHA256 = '7bf7975331511fdb788e85dae63964b128fccee1df026a10db57444babc9c9c4'

$out = Join-Path $Stage 'bus'

function Get-MsysBash {
    $roots = @($env:MSYS2_ROOT, 'C:\msys64', 'C:\tools\msys64') | Where-Object { $_ }
    foreach ($root in $roots) {
        $bash = Join-Path $root 'usr\bin\bash.exe'
        if (Test-Path $bash) { return $bash }
    }
    throw @'
MSYS2 is not installed (looked for usr\bin\bash.exe under $env:MSYS2_ROOT, C:\msys64, C:\tools\msys64).

Redis has no MSVC build: every working Windows Redis is compiled against a
POSIX emulation layer. Install MSYS2 from https://www.msys2.org/ and then, in
the MSYS2 shell:

    pacman -S --needed --noconfirm base-devel gcc

Set MSYS2_ROOT if it is installed somewhere other than C:\msys64.
'@
}

# Invoke-Msys runs one command in the MSYS environment.
#
# MSYSTEM=MSYS selects the POSIX-emulating environment rather than one of the
# mingw ones: mingw targets the native Win32 API, which is precisely what Redis
# cannot be built against.
function Invoke-Msys {
    param(
        [Parameter(Mandatory)][string]$What,
        [Parameter(Mandatory)][string]$Command
    )
    $bash = Get-MsysBash
    $previous = $env:MSYSTEM
    $env:MSYSTEM = 'MSYS'
    try {
        Invoke-Native $What $bash '-lc' $Command
    } finally {
        $env:MSYSTEM = $previous
    }
}

# ConvertTo-MsysPath turns a Windows path into the POSIX one MSYS2 tools take.
function ConvertTo-MsysPath {
    param([Parameter(Mandatory)][string]$Path)
    $bash = Get-MsysBash
    $converted = & $bash '-lc' "cygpath -u '$Path'"
    if ($LASTEXITCODE -ne 0) { throw "cygpath could not convert $Path" }
    return $converted.Trim()
}

function Build-Redis {
    $tar = Join-Path $Work "redis-$REDIS_VERSION.tar.gz"
    Get-Pinned -Url "https://download.redis.io/releases/redis-$REDIS_VERSION.tar.gz" `
        -Destination $tar -Sha256 $REDIS_SHA256

    $src = Join-Path $Work "redis-$REDIS_VERSION"
    Remove-Item -Recurse -Force -ErrorAction SilentlyContinue $src
    Write-Step "unpacking redis $REDIS_VERSION"
    Invoke-Native 'unpacking redis' 'tar.exe' '-xzf' $tar '-C' $Work

    Write-Step 'building redis (MSYS2)'
    # MALLOC=libc because jemalloc does not build on this layer, and
    # BUILD_TLS=no because the bus is reached only over loopback by processes
    # this launcher started -- the same choice the macOS lane makes for Valkey.
    $srcPosix = ConvertTo-MsysPath $src
    Invoke-Msys 'building redis' "cd '$srcPosix' && make -j`$(nproc) MALLOC=libc BUILD_TLS=no"
    return $src
}

# Copy-Runtime copies redis-server.exe together with every MSYS DLL it loads.
#
# Asking the linker rather than hardcoding a list: the set changes with the
# build options, and a DLL left behind turns into a dialog box on the user's
# machine, not an error here.
function Copy-Runtime {
    param([Parameter(Mandatory)][string]$Source)

    Copy-Item (Join-Path $Source 'src\redis-server.exe') (Join-Path $out 'redis-server.exe')

    $exePosix = ConvertTo-MsysPath (Join-Path $out 'redis-server.exe')
    $bash = Get-MsysBash
    $lines = & $bash '-lc' "ldd '$exePosix'"
    if ($LASTEXITCODE -ne 0) { throw 'ldd could not inspect the built redis-server.exe' }

    $copied = 0
    foreach ($line in $lines) {
        if ($line -match '=>\s*(/usr/bin/\S+\.dll)') {
            $dll = $Matches[1]
            Invoke-Msys 'copying a runtime DLL' "cp '$dll' '$(ConvertTo-MsysPath $out)/'"
            $copied++
        }
    }
    if ($copied -eq 0) {
        throw 'redis-server.exe reported no MSYS runtime dependency, which cannot be right -- the DLL scan is broken and the bundle would ship incomplete'
    }
    Write-Step "copied $copied runtime DLL(s) beside redis-server.exe"
}

# Copy-Licenses ships the notices the two licenses require.
#
# Redis 7.2 is BSD-3 (retain the copyright notice in a binary distribution);
# msys-2.0.dll is LGPLv3 (convey the library unmodified with its license). Both
# are met by putting the text in the bundle, so this is a build step and not a
# line in a document nobody ships.
function Copy-Licenses {
    param([Parameter(Mandatory)][string]$Source)

    $licenses = Join-Path $out 'licenses'
    New-Item -ItemType Directory -Force -Path $licenses | Out-Null
    Copy-Item (Join-Path $Source 'COPYING') (Join-Path $licenses 'redis-COPYING.txt')

    # MSYS2 has moved this file between releases, so both known locations are
    # tried and a miss writes the notice by hand rather than failing a build
    # over a path -- the obligation is that the user receives the terms.
    $candidates = @('/usr/share/licenses/msys2-runtime/COPYING', '/usr/share/doc/Cygwin/COPYING')
    $target = ConvertTo-MsysPath (Join-Path $licenses 'msys2-runtime-COPYING.txt')
    $found = & (Get-MsysBash) '-lc' "for f in $($candidates -join ' '); do if [ -f `"`$f`" ]; then cp `"`$f`" '$target'; echo `"`$f`"; break; fi; done"
    if (-not $found) {
        Set-Content -LiteralPath (Join-Path $licenses 'msys2-runtime-COPYING.txt') -Value @'
msys-2.0.dll is the MSYS2 runtime, a fork of the Cygwin library, distributed
here unmodified under the GNU Lesser General Public License version 3.

Full text:   https://www.gnu.org/licenses/lgpl-3.0.txt
Source code: https://github.com/msys2/msys2-runtime
'@
    }
}

New-Item -ItemType Directory -Force -Path $Work | Out-Null
Remove-Item -Recurse -Force -ErrorAction SilentlyContinue $out
New-Item -ItemType Directory -Force -Path $out | Out-Null

$source = Build-Redis
Copy-Runtime -Source $source
Copy-Licenses -Source $source

# Running it out of the staged folder is the only check that proves the DLLs
# travelled: from the build tree it would find them on PATH and pass anyway.
Write-Step 'verifying the staged bus runs standalone'
& (Join-Path $out 'redis-server.exe') --version
if ($LASTEXITCODE -ne 0) {
    throw 'the staged redis-server.exe could not run -- a runtime DLL is missing from the folder'
}
Write-Step "size: $(Get-DirectorySize $out)"
