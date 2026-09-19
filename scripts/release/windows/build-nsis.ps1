[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)] [string] $Version,
    [Parameter(Mandatory = $true)] [string] $OutputDirectory
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
if (-not $IsWindows -or $env:PROCESSOR_ARCHITECTURE -ne 'AMD64') { throw 'Windows x64 build host required.' }
if ($Version -notmatch '^\d+\.\d+\.\d+([.-][0-9A-Za-z.-]+)?$') { throw 'Invalid release version.' }
if (Test-Path -LiteralPath $OutputDirectory) { throw 'Release output already exists.' }

$repository = (Resolve-Path (Join-Path $PSScriptRoot '../../..')).Path
$identity = Get-Content -LiteralPath (Join-Path $repository 'release/windows/identity.json') -Raw | ConvertFrom-Json
if ($identity.releaseClass -ne 'private-release' -or $identity.signingRequired -or
    $identity.targetTriple -ne 'x86_64-pc-windows-msvc') {
    throw 'Windows release identity is not the unsigned x64 self-use contract.'
}

node (Join-Path $repository 'release/shared/sidecar/build.mjs') --target $identity.targetTriple
if ($LASTEXITCODE -ne 0) { throw 'Windows sidecar build failed.' }
pnpm --dir (Join-Path $repository 'frontend') --filter '@go-admin-plus/admin-desktop' build
if ($LASTEXITCODE -ne 0) { throw 'Desktop frontend build failed.' }

$config = [ordered]@{
    version = $Version
    bundle = [ordered]@{
        targets = @('nsis')
        windows = [ordered]@{
            webviewInstallMode = [ordered]@{ type = 'offlineInstaller' }
            nsis = [ordered]@{ installMode = 'currentUser'; displayLanguageSelector = $false }
        }
    }
} | ConvertTo-Json -Depth 12 -Compress
$configPath = Join-Path ([System.IO.Path]::GetTempPath()) "go-admin-plus-tauri-$PID-$Version.json"
try {
    $config | Set-Content -LiteralPath $configPath -Encoding utf8NoBOM
    pnpm --dir (Join-Path $repository 'frontend') --filter '@go-admin-plus/admin-desktop' exec tauri build `
        --target $identity.targetTriple --features custom-protocol --bundles nsis --config $configPath
    if ($LASTEXITCODE -ne 0) { throw 'Unsigned Tauri NSIS build failed.' }
} finally {
    Remove-Item -LiteralPath $configPath -Force -ErrorAction SilentlyContinue
}

$target = Join-Path $repository "frontend/apps/admin-desktop/src-tauri/target/$($identity.targetTriple)/release"
$application = Join-Path $target 'go-admin-plus-desktop.exe'
$sidecar = Join-Path $repository 'frontend/apps/admin-desktop/src-tauri/binaries/go-admin-sidecar-x86_64-pc-windows-msvc.exe'
$installer = @(Get-ChildItem -LiteralPath (Join-Path $target 'bundle/nsis') -Filter '*-setup.exe' -File)
if (-not (Test-Path -LiteralPath $application) -or -not (Test-Path -LiteralPath $sidecar) -or $installer.Count -ne 1) {
    throw 'Expected Tauri outputs are incomplete.'
}
node (Join-Path $repository 'frontend/apps/admin-desktop/scripts/verify-production.mjs') --files $application $sidecar
if ($LASTEXITCODE -ne 0) { throw 'Production Desktop artifacts retained native test controls.' }

New-Item -ItemType Directory -Path $OutputDirectory | Out-Null
$canonicalInstaller = Join-Path $OutputDirectory "$($identity.artifactBasename)-$Version-windows-x64-setup.exe"
Copy-Item -LiteralPath $application -Destination (Join-Path $OutputDirectory 'go-admin-plus-desktop.exe')
Copy-Item -LiteralPath $installer[0].FullName -Destination $canonicalInstaller
& (Join-Path $PSScriptRoot 'verify-artifacts.ps1') -Version $Version -InstallerFile $canonicalInstaller -ApplicationFile (Join-Path $OutputDirectory 'go-admin-plus-desktop.exe')
Write-Host 'GO_ADMIN_WINDOWS_NSIS_BUILD_PASS'
