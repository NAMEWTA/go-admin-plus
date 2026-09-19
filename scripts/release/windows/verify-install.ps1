[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)] [string] $InstallerFile,
    [Parameter(Mandatory = $true)] [string] $EvidenceFile,
    [string] $RepositoryRoot = (Resolve-Path (Join-Path $PSScriptRoot '../../..')).Path
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
if ($env:CI -ne 'true' -or $env:GITHUB_ACTIONS -ne 'true' -or [string]::IsNullOrWhiteSpace($env:RUNNER_TEMP)) {
    throw 'Install verification is restricted to an ephemeral GitHub Actions runner.'
}
$repository = (Resolve-Path -LiteralPath $RepositoryRoot).Path
$identity = Get-Content -LiteralPath (Join-Path $repository 'release/windows/identity.json') -Raw | ConvertFrom-Json
$installer = (Resolve-Path -LiteralPath $InstallerFile).Path
$installDirectory = Join-Path $env:RUNNER_TEMP "go-admin-plus-install-$([guid]::NewGuid().ToString('N'))"
$dataRoot = Join-Path $env:LOCALAPPDATA 'com.goadmin.plus/data'
$logRoot = Join-Path $env:LOCALAPPDATA 'com.goadmin.plus/logs'
$configRoot = Join-Path $env:APPDATA 'com.goadmin.plus'
$configFile = Join-Path $configRoot 'connection.json'
if (Test-Path -LiteralPath $configFile) { throw 'Existing connection settings must not be modified.' }
if (Test-Path -LiteralPath $dataRoot) { throw 'Existing user data must not be modified.' }
$credentialTarget = 'desktop-session-vault.com.goadmin.plus.stronghold'
if (Test-Path -LiteralPath $installDirectory) { throw 'Install directory already exists.' }

Add-Type -TypeDefinition @'
using System;
using System.Runtime.InteropServices;

public static class CredentialProbe
{
    [DllImport("advapi32.dll", EntryPoint = "CredReadW", CharSet = CharSet.Unicode, SetLastError = true)]
    private static extern bool CredRead(string target, uint type, uint flags, out IntPtr credential);

    [DllImport("advapi32.dll", SetLastError = false)]
    private static extern void CredFree(IntPtr credential);

    public static bool Exists(string target)
    {
        IntPtr credential;
        if (!CredRead(target, 1, 0, out credential)) return false;
        CredFree(credential);
        return true;
    }
}
'@
if ([CredentialProbe]::Exists($credentialTarget)) { throw 'Release runner contains an unexpected production credential.' }

$install = Start-Process -FilePath $installer -ArgumentList @('/S', "/D=$installDirectory") -Wait -PassThru
if ($install.ExitCode -ne 0) { throw "NSIS install failed with code $($install.ExitCode)." }
$application = Join-Path $installDirectory 'go-admin-plus-desktop.exe'
$sidecar = Join-Path $installDirectory 'go-admin-sidecar.exe'
foreach ($file in @($application, $sidecar)) {
    if (-not (Test-Path -LiteralPath $file)) { throw "Installed payload is missing: $file" }
}
& node (Join-Path $PSScriptRoot 'probe-sidecar.mjs') $sidecar
if ($LASTEXITCODE -ne 0) { throw 'Sidecar environment probe failed.' }

# 在一次性 runner 的真实用户数据目录准备旧基线；安装后的程序负责备份和自动迁移。
$appDataRoot = Split-Path -Parent $dataRoot
New-Item -ItemType Directory -Path $appDataRoot -Force | Out-Null
New-Item -ItemType Directory -Path $configRoot -Force | Out-Null
[IO.File]::WriteAllText($configFile, '{"mode":"local","serverUrl":"","caCertificate":""}', [Text.UTF8Encoding]::new($false))
& go -C (Join-Path $repository 'backend') run ./test/desktop/fixture --root $appDataRoot --mode previous
if ($LASTEXITCODE -ne 0) { throw 'Installed desktop fixture preparation failed.' }
$traceFile = Join-Path $env:RUNNER_TEMP "desktop-trace-$([guid]::NewGuid().ToString('N')).json"
# GitHub Windows runner 使用提升权限；新版 WebView2 忽略环境变量和 HKCU 调试参数。
# 仅在一次性 runner 中为本应用设置 HKLM 参数，退出时立即清除，不改动生产二进制。
$policyPath = 'HKLM:\SOFTWARE\Policies\Microsoft\Edge\WebView2\AdditionalBrowserArguments'
$policyNames = @('go-admin-plus-desktop.exe', $identity.bundleIdentifier)
foreach ($name in $policyNames) {
    if (Test-Path -LiteralPath $policyPath) {
        if ($null -ne (Get-Item -LiteralPath $policyPath).GetValue($name)) { throw 'Existing WebView2 policy must not be modified.' }
    }
}
$listener = [Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback, 0)
$listener.Start()
$debugPort = $listener.LocalEndpoint.Port
$listener.Stop()
$env:GO_ADMIN_WINDOWS_CDP_PORT = [string]$debugPort
if (-not (Test-Path -LiteralPath $policyPath)) { New-Item -Path $policyPath -Force | Out-Null }
try {
    foreach ($name in $policyNames) {
        New-ItemProperty -LiteralPath $policyPath -Name $name -PropertyType String -Value "--remote-debugging-port=$debugPort --remote-debugging-address=127.0.0.1" | Out-Null
    }
    & node (Join-Path $PSScriptRoot 'trace-installed.mjs') --application $application --evidence $traceFile
    if ($LASTEXITCODE -ne 0) { throw 'Installed desktop UI verification failed.' }
} finally {
    foreach ($name in $policyNames) {
        Remove-ItemProperty -LiteralPath $policyPath -Name $name -ErrorAction SilentlyContinue
    }
    Remove-Item Env:GO_ADMIN_WINDOWS_CDP_PORT -ErrorAction SilentlyContinue
}
$trace = Get-Content -LiteralPath $traceFile -Raw | ConvertFrom-Json
$database = Join-Path $dataRoot 'go-admin-plus.db'

$databaseHash = (Get-FileHash -LiteralPath $database -Algorithm SHA256).Hash.ToLowerInvariant()
$uninstallEntry = Get-ItemProperty 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Uninstall\*' |
    Where-Object DisplayName -eq $identity.productName
if (@($uninstallEntry).Count -ne 1) { throw 'Expected exactly one current-user uninstall registration.' }
$uninstallString = [string]$uninstallEntry.UninstallString
if ($uninstallString -match '^"([^"]+\.exe)"') {
    $uninstaller = $Matches[1]
} elseif ($uninstallString -match '^(.+?\.exe)(?:\s|$)') {
    $uninstaller = $Matches[1]
} else {
    throw 'Registered uninstall command is not an executable path.'
}
if (-not (Test-Path -LiteralPath $uninstaller)) { throw 'Registered uninstall command is not an executable path.' }

$sentinel = Join-Path $dataRoot 'uninstall-preservation.txt'
$sentinelValue = [guid]::NewGuid().ToString('N')
Set-Content -LiteralPath $sentinel -Value $sentinelValue -Encoding ascii
$uninstall = Start-Process -FilePath $uninstaller -ArgumentList '/S' -Wait -PassThru
if ($uninstall.ExitCode -ne 0) { throw "NSIS uninstall failed with code $($uninstall.ExitCode)." }
if (Test-Path -LiteralPath $application) { throw 'Uninstall left the installed application.' }
if ((Get-Content -LiteralPath $sentinel -Raw).Trim() -ne $sentinelValue -or -not (Test-Path -LiteralPath $database)) {
    throw 'Uninstall violated the user-data preservation boundary.'
}
[ordered]@{
    schemaVersion = 1
    installScope = 'currentUser'
    installDirectory = $installDirectory
    dataDirectory = $dataRoot
    logDirectory = $logRoot
    installPathSelected = $true
    firstLaunch = 'passed'
    login = $trace.firstLaunchLogin
    crudCreate = $trace.create
    restart = $trace.restart
    persistence = $trace.persistence
    crudDelete = $trace.delete
    sqlitePathStable = $true
    sqliteInitialSha256 = $databaseHash
    appDataPreserved = $true
    installDirectoryRemoved = $true
} | ConvertTo-Json | Set-Content -LiteralPath $EvidenceFile -Encoding utf8
Write-Host 'GO_ADMIN_WINDOWS_INSTALL_PASS'
