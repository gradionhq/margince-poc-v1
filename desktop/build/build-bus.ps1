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

# Where the source unpacks. Named once at script scope rather than returned from
# Build-Redis: a PowerShell function returns EVERYTHING on the success stream,
# so a returned path is only as trustworthy as every command in the function
# being quiet. This path is derived from the pinned version and needs no such
# assumption.
$src = Join-Path $Work "redis-$REDIS_VERSION"

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

    Remove-Item -Recurse -Force -ErrorAction SilentlyContinue $src
    Write-Step "unpacking redis $REDIS_VERSION"
    Invoke-Native 'unpacking redis' 'tar.exe' '-xzf' $tar '-C' $Work

    Write-Step 'building redis (MSYS2)'
    # MALLOC=libc because jemalloc does not build on this layer, and
    # BUILD_TLS=no because the bus is reached only over loopback by processes
    # this launcher started -- the same choice the macOS lane makes for Valkey.
    #
    # REDIS_CFLAGS=-D_GNU_SOURCE is what makes debug.c compile here. It calls
    # dladdr() and declares Dl_info unconditionally -- no #if guards that code
    # -- and this layer's dlfcn.h puts both behind `#if __GNU_VISIBLE`, which is
    # 0 until _GNU_SOURCE is defined. Redis never defines it and relies on the
    # platform's default visibility instead, so the file compiles everywhere it
    # is normally built and fails here with "unknown type name 'Dl_info'".
    # REDIS_CFLAGS is the Makefile's own hook for this: FINAL_CFLAGS appends it
    # last, so it adds to Redis's flags rather than replacing them.
    # DEBUG_FLAGS carries -Wno-char-subscripts into the bundled hiredis, which
    # otherwise fails the build outright rather than warning. Its sds.c calls
    # isprint(*p) on a char*, and this layer's ctype implementation is an array
    # lookup indexed by the argument, so a plain char index raises
    # -Wchar-subscripts -- which hiredis promotes with its own -Werror. (The
    # index is in fact safe: this libc pads its ctype table with 128 leading
    # entries precisely so a signed char cannot run off the front. The
    # diagnostic is conservative, not a defect being hidden.)
    #
    # DEBUG_FLAGS is the lever because it is the ONLY one that lands after
    # hiredis's WARNINGS in REAL_CFLAGS, and gcc lets the last flag win; CFLAGS
    # is placed before them, so -Wall would simply switch the warning back on.
    # It is declared `?=` for exactly this kind of override, and reaches the
    # nested make through the environment.
    #
    # Redis's own src/ build only WARNS on the same construct, and the other
    # bundled deps compile clean, so this is the one dependency that needs it.
    $srcPosix = ConvertTo-MsysPath $src
    Invoke-Msys 'building redis' ("cd '$srcPosix' && " +
        "DEBUG_FLAGS='-g -ggdb -Wno-char-subscripts' " +
        "make -j`$(nproc) MALLOC=libc BUILD_TLS=no REDIS_CFLAGS=-D_GNU_SOURCE")
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
    # tried. A miss is a BUILD FAILURE, not a hand-written summary.
    #
    # The obligation is that the recipient receives the TERMS, and a paragraph
    # naming the licence with a link to it is not the terms: LGPLv3 requires the
    # licence text to be conveyed with the work, and it incorporates GPLv3 by
    # reference, so a compliant copy is both texts and not a summary of either.
    # The previous fallback produced a bundle that looked complete and was not,
    # which is the worst of the three outcomes -- worse than failing, because
    # nobody finds out.
    #
    # Failing is also the cheap fix for the real cause: these paths move, and a
    # maintainer who sees this error updates one array. A silent summary meant
    # nobody learned the path had moved at all.
    $candidates = @('/usr/share/licenses/msys2-runtime/COPYING', '/usr/share/doc/Cygwin/COPYING')
    $target = ConvertTo-MsysPath (Join-Path $licenses 'msys2-runtime-COPYING.txt')
    $found = & (Get-MsysBash) '-lc' "for f in $($candidates -join ' '); do if [ -f `"`$f`" ]; then cp `"`$f`" '$target'; echo `"`$f`"; break; fi; done"
    if (-not $found) {
        throw @"
the MSYS2 runtime licence text was not found, so this bundle cannot ship msys-2.0.dll.

Looked in:
  $($candidates -join "`n  ")

msys-2.0.dll is LGPLv3 and its terms have to travel with it. Find where the
MSYS2 package now installs COPYING and add that path to `$candidates in
desktop/build/build-bus.ps1.
"@
    }
}

New-Item -ItemType Directory -Force -Path $Work | Out-Null
Remove-Item -Recurse -Force -ErrorAction SilentlyContinue $out
New-Item -ItemType Directory -Force -Path $out | Out-Null

Build-Redis
Copy-Runtime -Source $src
Copy-Licenses -Source $src

# Running it out of the staged folder is the only check that proves the DLLs
# travelled: from the build tree it would find them on PATH and pass anyway.
Write-Step 'verifying the staged bus runs standalone'
& (Join-Path $out 'redis-server.exe') --version
if ($LASTEXITCODE -ne 0) {
    throw 'the staged redis-server.exe could not run -- a runtime DLL is missing from the folder'
}
Write-Step "size: $(Get-DirectorySize $out)"
