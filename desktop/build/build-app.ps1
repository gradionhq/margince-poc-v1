# Build the Margince-authored half of the Windows bundle: the three process-role
# binaries, the frontend, and the launcher that supervises them.
#
# The server binaries are built through build/composition/, not with a bare
# `go build`, because that wiring is what links the enabled extensions/ units
# in. A bundle built against the vanilla stub would silently ship without the
# first-party packs and look identical from the outside.
#
# There is no signing step. macOS refuses to execute a binary whose signature
# is invalid, so that lane must sign; Windows has no equivalent requirement and
# Authenticode needs a purchased certificate rather than an ad-hoc one. What
# the user sees instead is a SmartScreen warning on first launch -- documented,
# not worked around.
. "$PSScriptRoot\common.ps1"

$out = Join-Path $Stage 'bin'

# Invoke-Go runs the toolchain with one GOWORK setting, restoring whatever the
# session had. Which workspace file is active decides which composition module
# the compiler links, so it is never left to the ambient environment.
function Invoke-Go {
    param(
        [Parameter(Mandatory)][string]$What,
        [Parameter(Mandatory)][string]$GoWork,
        [Parameter(Mandatory)][string]$WorkingDirectory,
        [Parameter(ValueFromRemainingArguments)][string[]]$Arguments
    )
    $previous = $env:GOWORK
    $env:GOWORK = $GoWork
    Push-Location $WorkingDirectory
    try {
        Invoke-Native $What 'go' @Arguments
    } finally {
        Pop-Location
        $env:GOWORK = $previous
    }
}

function Build-ServerBinaries {
    Write-Step 'materializing build/composition'
    Invoke-Go 'gen-composition' (Join-Path $RepoRoot 'go.work') (Join-Path $RepoRoot 'backend') `
        'run' './tools/gen-composition'

    $composition = Join-Path $RepoRoot 'build\composition\go.work'
    if (-not (Test-Path $composition)) {
        throw "gen-composition did not produce $composition"
    }

    New-Item -ItemType Directory -Force -Path $out | Out-Null
    foreach ($role in @('api', 'worker', 'migrate')) {
        Write-Step "building $role"
        Invoke-Go "building $role" $composition (Join-Path $RepoRoot 'backend') `
            'build' '-o' (Join-Path $out "$role.exe") "./cmd/$role"
    }
}

# Build-Frontend builds the COMPOSED SPA, for the same reason the server
# binaries are built through build/composition/: a bare `pnpm build` resolves
# the committed empty-tree registry, so the bundle would ship a server with the
# enabled units linked and a UI that routes none of them.
function Build-Frontend {
    Write-Step 'building the frontend (composed)'
    $registry = Join-Path $RepoRoot 'build\composition\frontend'
    if (-not (Test-Path (Join-Path $registry 'extensions.gen.ts'))) {
        throw "gen-composition did not produce $registry\extensions.gen.ts"
    }

    # The published-event types are the one generated artifact whose composed
    # form is written back into the tracked source tree (frontend/src/api/), so
    # this lane refuses to run it: a throwaway Docker layer can afford a dirty
    # checkout and a developer's cannot. As long as no enabled unit contributes
    # a public event the two documents are identical and there is nothing to
    # generate; the moment one does, this says so instead of shipping types
    # that silently omit it.
    $vanilla = Get-FileHash -Algorithm SHA256 (Join-Path $RepoRoot 'backend\api\public-events.yaml')
    $composed = Get-FileHash -Algorithm SHA256 (Join-Path $RepoRoot 'build\composition\api\public-events.yaml')
    if ($vanilla.Hash -ne $composed.Hash) {
        throw 'an enabled extension contributes public events, so frontend/src/api/public-events.ts must be regenerated (pnpm gen:events:composed) before this bundle can ship correct types'
    }

    # Install at the REPO ROOT, build in frontend/. The lockfile is a root pnpm
    # workspace -- a unit's frontend layer is a member of it -- so there is no
    # frontend\pnpm-lock.yaml to install against, and --frozen-lockfile run
    # from frontend\ would either resolve the root lockfile from a
    # subdirectory or rewrite it. The Dockerfile's web stage splits the two
    # steps for the same reason and is the reference for this lane.
    #
    # --ignore-scripts matches every other frontend install in this repo: the
    # lockfile pins what is installed, and this stops a dependency's lifecycle
    # script from running arbitrary code on the build machine.
    Push-Location $RepoRoot
    try {
        Invoke-Native 'pnpm install' 'pnpm' 'install' '--frozen-lockfile' '--ignore-scripts'
    } finally {
        Pop-Location
    }

    $frontend = Join-Path $RepoRoot 'frontend'
    $previous = $env:MARGINCE_COMPOSITION_FRONTEND
    Push-Location $frontend
    try {
        $env:MARGINCE_COMPOSITION_FRONTEND = $registry
        Invoke-Native 'pnpm gen:composed-types' 'pnpm' 'gen:composed-types'
        Invoke-Native 'pnpm build:composed' 'pnpm' 'build:composed'
    } finally {
        Pop-Location
        $env:MARGINCE_COMPOSITION_FRONTEND = $previous
    }

    $web = Join-Path $Stage 'web'
    Remove-Item -Recurse -Force -ErrorAction SilentlyContinue $web
    Copy-Item -Recurse (Join-Path $frontend 'dist') $web
}

function Build-Launcher {
    Write-Step 'building the launcher'
    # GOWORK=off: the launcher is a standalone stdlib-only module deliberately
    # outside the workspace, so it neither sees nor perturbs the backend's
    # dependency graph.
    Invoke-Go 'building the launcher' 'off' (Join-Path $RepoRoot 'desktop\launcher') `
        'build' '-o' (Join-Path $out 'margince.exe') '.'
}

Build-ServerBinaries
if ($env:SKIP_FRONTEND -ne '1') {
    Build-Frontend
}
Build-Launcher
Write-Step "binaries in $out"
