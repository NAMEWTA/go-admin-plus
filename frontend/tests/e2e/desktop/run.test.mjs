import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join, resolve } from 'node:path'
import test from 'node:test'
import { fileURLToPath } from 'node:url'
import {
  desktopNativeControlMarkers,
  desktopProductionPermissions,
  validateDesktopProductionConfiguration,
  verifyDesktopProductionAssets,
  verifyDesktopProductionConfiguration,
  verifyDesktopProductionFiles
} from '../../../apps/admin-desktop/scripts/verify-production.mjs'
import { desktopProductionArtifactPaths } from '../../../apps/admin-desktop/scripts/verify-build.mjs'
import { buttonCurrentScript, clickButtonScript, fillAndSubmitScript, quoteAppleScript, windowBusyScript, windowContainsScript, windowFrameScript, windowValueScript } from './accessibility.mjs'
import { nativeAccessibilityFailure, nativeFailureDiagnostic, nativePhaseFailure } from './diagnostics.mjs'
import { execute, parseSidecarProcesses, reapNewSidecars, sidecarProcesses } from './processes.mjs'

const repositoryRoot = resolve(dirname(fileURLToPath(import.meta.url)), '../../../..')

test('native runner Go fixture dependencies exist in the current backend', () => {
  const runner = readFileSync(new URL('./run.mjs', import.meta.url), 'utf8')
  const fixtures = [...runner.matchAll(/'go', \['run', '([^']+)'/g)].map(match => match[1])
  assert.deepEqual([...new Set(fixtures)], ['./test/desktop/fixture'])
  assert.ok(existsSync(join(repositoryRoot, 'backend', 'test/desktop/fixture/main.go')))
})

test('native runner uses retained IAM role controls', () => {
 const runner=readFileSync(new URL('./run.mjs',import.meta.url),'utf8')
 const page=readFileSync(join(repositoryRoot,'frontend/packages/web-domains/iam/src/administration/AdministrationPage.vue'),'utf8')
 assert.match(runner,/E2E open Roles/)
 assert.match(runner,/编辑角色 e2e-role/)
 assert.match(page,/角色名称/)
})

test('shared workspace composes a persistent theme toggle and native restart verifies it', () => {
  const runner = readFileSync(new URL('./run.mjs', import.meta.url), 'utf8')
  const shell = readFileSync(join(repositoryRoot, 'frontend/packages/app-shell/src/product/ProductWorkspace.vue'), 'utf8')
  const manifest = JSON.parse(readFileSync(join(repositoryRoot, 'frontend/packages/app-shell/package.json'), 'utf8'))
  assert.equal(manifest.dependencies['@go-admin-plus/ui'], 'workspace:*')
  assert.equal(manifest.dependencies['@lucide/vue'], 'catalog:')
  const lucideImports = shell.match(/import \{([\s\S]*?)\} from '@lucide\/vue'/)?.[1] ?? ''
  assert.match(lucideImports, /\bMonitorIcon\b/)
  assert.match(lucideImports, /\bMoonIcon\b/)
  assert.match(lucideImports, /\bSunIcon\b/)
  assert.match(shell, /import \{ createThemeController \} from '@go-admin-plus\/ui'/)
  assert.match(shell, /const theme = createThemeController\(\)/)
  assert.match(shell, /const setThemePreference = \(preference: ThemePreference\) => \{[\s\S]*theme\.setPreference\(preference\)/)
  assert.match(shell, /theme\.destroy\(\)/)
  assert.match(shell, /role="group" aria-label="主题模式"/)
  assert.match(shell, /<MonitorIcon[^>]+aria-hidden="true"/)
  assert.match(shell, /<SunIcon[^>]+aria-hidden="true"/)
  assert.match(shell, /<MoonIcon[^>]+aria-hidden="true"/)
  assert.match(shell, /themeSnapshot\.preference === 'dark' \? '当前使用深色主题' : '使用深色主题'/)
  assert.match(runner, /phase = 'theme-dark-toggle'[\s\S]*clickButton\(app\.child\.pid, 'E2E use dark theme'\)[\s\S]*windowContains\(app\.child\.pid, '当前使用深色主题'\)/)
  assert.match(runner, /phase = 'theme-dark-persistence'[\s\S]*windowContains\(app\.child\.pid, '当前使用深色主题'\)/)
  assert.match(runner, /identifier: 'com\.goadmin\.plus\.native-e2e'/)
  assert.doesNotMatch(runner, /dataStoreIdentifier/)
  assert.match(runner, /TAURI_CONFIG: nativeE2eTauriConfig/)
  const rustHost = readFileSync(join(repositoryRoot, 'frontend/apps/admin-desktop/src-tauri/src/main.rs'), 'utf8')
  assert.match(rustHost, /#\[cfg\(feature = "native-e2e"\)\][\s\S]*NATIVE_E2E_DATA_STORE_IDENTIFIER: \[u8; 16\] = \[[\s\S]*103, 111, 97, 100, 109, 105, 78, 80, 172, 117, 115, 101, 50, 101, 48, 49,[\s\S]*windows\[0\]\.data_store_identifier = Some\(NATIVE_E2E_DATA_STORE_IDENTIFIER\)/)
  assert.match(rustHost, /\.build\(desktop_context\(\)\)/)
  assert.match(runner, /phase = 'theme-storage-cleanup'[\s\S]*clickButton\(app\.child\.pid, 'E2E reset theme'\)/)
  const nativeApp = readFileSync(join(repositoryRoot, 'frontend/apps/admin-desktop/src/native-e2e/App.vue'), 'utf8')
  assert.match(nativeApp, /window\.localStorage\.getItem\(nativeE2eRunStorageKey\) !== nativeE2eRunId[\s\S]*window\.localStorage\.clear\(\)/)
  assert.match(nativeApp, /!themeStorageIsSafe\(\) \? 'local-storage' : ''/)
  assert.match(nativeApp, /window\.localStorage\.removeItem\(ADMIN_THEME_STORAGE_KEY\)[\s\S]*window\.localStorage\.removeItem\(nativeE2eRunStorageKey\)/)
  assert.match(nativeApp, /window\.localStorage\.length === 0[\s\S]*'E2E theme storage cleared'[\s\S]*'E2E control failed: theme-storage-cleanup'/)
  assert.match(nativeApp, /const openFiles = \(\) => \{\n  window\.location\.hash = '#\/files'\n\}/)
  assert.match(nativeApp, /<button type="button" @click="openFiles">E2E open Files<\/button>/)
  assert.match(nativeApp, /const useDarkTheme = \(\) => \{[\s\S]*document\.querySelector<HTMLButtonElement>\('button\[aria-label="使用深色主题"\]'\)[\s\S]*'E2E control failed: theme-dark'[\s\S]*control\.click\(\)/)
  assert.match(nativeApp, /<button type="button" @click="useDarkTheme">E2E use dark theme<\/button>/)
})

test('production Desktop entry contains no native E2E controls', () => {
  const appRoot = join(repositoryRoot, 'frontend/apps/admin-desktop')
  const app = readFileSync(join(appRoot, 'src/App.vue'), 'utf8')
  const main = readFileSync(join(appRoot, 'src/main.ts'), 'utf8')
  const vite = readFileSync(join(appRoot, 'vite.config.ts'), 'utf8')
  const manifest = JSON.parse(readFileSync(join(appRoot, 'package.json'), 'utf8'))
  const runner = readFileSync(new URL('./run.mjs', import.meta.url), 'utf8')
  assert.doesNotMatch(app, /VITE_GO_ADMIN_NATIVE_E2E|__desktop\/test-control|native-e2e|window\.confirm\s*=|E2E open Files|E2E use dark theme|E2E scope self/)
  assert.match(app, /<ProductWorkspace/)
  assert.match(main, /import App from '@desktop-entry'/)
  assert.match(vite, /mode === 'native-e2e' \? '\.\/src\/native-e2e\/App\.vue' : '\.\/src\/App\.vue'/)
  assert.match(runner, /'--mode', 'native-e2e'/)
  assert.match(runner, /verifyDesktopProductionFiles\(\[sidecarBinary, hostBinary\]\)/)
  assert.doesNotMatch(runner, /fileContains/)
  assert.match(manifest.scripts.build, /vite build[^&]+&& node scripts\/verify-production\.mjs/)
})

test('production Desktop capability configuration is exact and rejects privilege growth', async () => {
  const appRoot = join(repositoryRoot, 'frontend/apps/admin-desktop')
  const config = JSON.parse(readFileSync(join(appRoot, 'src-tauri/tauri.conf.json'), 'utf8'))
  const capability = JSON.parse(readFileSync(join(appRoot, 'src-tauri/capabilities/main.json'), 'utf8'))
  assert.deepEqual(capability.permissions, desktopProductionPermissions)
  assert.doesNotThrow(() => validateDesktopProductionConfiguration(config, capability))
  await assert.doesNotReject(verifyDesktopProductionConfiguration(appRoot))
  assert.throws(
    () => validateDesktopProductionConfiguration(config, { ...capability, permissions: [...capability.permissions, 'shell:allow-execute'] }),
    /desktop production capability configuration is invalid/
  )
  assert.throws(
    () => validateDesktopProductionConfiguration({ ...config, app: { ...config.app, windows: [{ ...config.app.windows[0], visible: true }] } }, capability),
    /desktop production capability configuration is invalid/
  )
  assert.throws(
    () => validateDesktopProductionConfiguration(config, { ...capability, remote: { urls: ['https://example.test'] } }),
    /desktop production capability configuration is invalid/
  )
  assert.throws(
    () => validateDesktopProductionConfiguration({ ...config, app: { ...config.app, windows: [{ ...config.app.windows[0], devtools: true }] } }, capability),
    /desktop production capability configuration is invalid/
  )
})

test('production Desktop asset verifier rejects native E2E bytes', async t => {
  const root = mkdtempSync(join(tmpdir(), 'go-admin-desktop-production-'))
  t.after(() => rmSync(root, { recursive: true, force: true }))
  mkdirSync(join(root, 'assets'))
  writeFileSync(join(root, 'index.html'), '<main>Go Admin Plus</main>')
  writeFileSync(join(root, 'assets/app.css'), '.product-shell{display:grid}')
  await assert.doesNotReject(verifyDesktopProductionAssets(root))
  assert.deepEqual(desktopNativeControlMarkers, [
    '/__desktop/test-control',
    'native-e2e',
    'VITE_GO_ADMIN_NATIVE_E2E',
    'GO_ADMIN_DESKTOP_NATIVE_E2E',
    'GO_ADMIN_DESKTOP_E2E_',
    'desktop_native_e2e',
    'E2E authenticated boundary verified',
    'E2E unauthenticated boundary verified',
    'E2E boundary blocked:',
    'E2E self scope enforced',
    'E2E all scope restored',
    'E2E authorization denied',
    'E2E authorization restored',
    'E2E control failed: theme-dark',
    'E2E control failed:',
    'E2E open Files',
    'E2E open Roles',
    'E2E use dark theme',
    'E2E scope self',
    'E2E scope all',
    'E2E permissions off',
    'E2E permissions on',
    'E2E revoke session',
    'E2E reset theme',
    'E2E theme storage cleared',
    'E2E-FOREIGN',
    'e2e-role',
    'native E2E credential identity'
  ])
  for (const marker of desktopNativeControlMarkers) {
    writeFileSync(join(root, 'assets/app.css'), `.product-shell{content:${JSON.stringify(marker)}}`)
    await assert.rejects(verifyDesktopProductionAssets(root), new RegExp(`native test control: ${marker.replaceAll(/[.*+?^${}()|[\]\\]/g, '\\$&')}`))
  }
  writeFileSync(join(root, 'assets/app.css'), '.product-shell{display:grid}')
  await assert.doesNotReject(verifyDesktopProductionFiles([join(root, 'index.html'), join(root, 'assets/app.css')]))
  writeFileSync(join(root, 'assets/app.css'), '.product-shell{content:"E2E permissions on"}')
  await assert.rejects(verifyDesktopProductionFiles([join(root, 'assets/app.css')]), /native test control: E2E permissions on/)
})

test('production Desktop artifact paths are exact for each supported host', () => {
  const arm = desktopProductionArtifactPaths('/repository', 'darwin', 'arm64')
  assert.equal(arm.sidecar, '/repository/frontend/apps/admin-desktop/src-tauri/binaries/go-admin-sidecar-aarch64-apple-darwin')
  assert.equal(arm.host, '/repository/frontend/apps/admin-desktop/src-tauri/target/release/go-admin-plus-desktop')
  const windows = desktopProductionArtifactPaths('C:\\repository', 'win32', 'x64')
  assert.equal(windows.sidecar, 'C:\\repository\\frontend\\apps\\admin-desktop\\src-tauri\\binaries\\go-admin-sidecar-x86_64-pc-windows-msvc.exe')
  assert.equal(windows.host, 'C:\\repository\\frontend\\apps\\admin-desktop\\src-tauri\\target\\release\\go-admin-plus-desktop.exe')
  assert.equal(desktopProductionArtifactPaths('/repository', 'linux', 'x64').sidecar, '/repository/frontend/apps/admin-desktop/src-tauri/binaries/go-admin-sidecar-x86_64-unknown-linux-gnu')
})

test('native runner stops polling after an early host exit', () => {
  const runner = readFileSync(new URL('./run.mjs', import.meta.url), 'utf8')
  assert.match(runner, /const windowContains = async \(pid, value\) => \{\n  if \(!processIsAlive\(pid\)\) throw new Error\('native host exited before UI observation'\)/)
  assert.match(runner, /nativePhaseFailure\(phase, app\?\.output\(\) \?\? ''\)/)
})

test('native diagnostics preserve the failing phase and ignore state transitions', () => {
  const output = [
    'desktop native identity state: vault empty',
    'desktop native identity state: unauthenticated',
  ].join('\n')
  assert.equal(nativeFailureDiagnostic(output), undefined)
  assert.equal(nativePhaseFailure('session-revocation', output).message, 'desktop native session-revocation failed')

  const failed = `${output}\ndesktop native login failed: desktop login rejected\n`
  assert.equal(nativeFailureDiagnostic(failed), 'desktop native login failed: desktop login rejected')
  assert.equal(
    nativePhaseFailure('login-workspace', failed).message,
    'desktop native login-workspace failed: desktop native login failed: desktop login rejected'
  )
})

test('native accessibility diagnostics expose only a fixed unavailable category', () => {
  assert.equal(nativeAccessibilityFailure('native field unavailable: 密码 (-2700)'), 'desktop native accessibility field unavailable')
  assert.equal(nativeAccessibilityFailure('native field action unavailable: 2 (-1719)'), 'desktop native accessibility field-action-2 unavailable')
  assert.equal(nativeAccessibilityFailure('native submit button unavailable: 登录 (-2700)'), 'desktop native accessibility submit-button unavailable')
  assert.equal(nativeAccessibilityFailure('native navigation start unavailable (-2700)'), 'desktop native accessibility navigation-start unavailable')
  assert.equal(nativeAccessibilityFailure('native navigation traversal unavailable (-1719)'), 'desktop native accessibility navigation-traversal unavailable')
  assert.equal(nativeAccessibilityFailure('native navigation action unavailable (-2700)'), 'desktop native accessibility navigation-action unavailable')
  assert.equal(nativeAccessibilityFailure('arbitrary AppleScript failure'), undefined)
})

test('native runner verifies SQLite only while the sidecar is stopped', () => {
  const runner = readFileSync(new URL('./run.mjs', import.meta.url), 'utf8')
  const stopped = runner.indexOf('await assertNoNewSidecars(sidecarBaseline)', runner.indexOf("phase = 'product-update'"))
  const verified = runner.indexOf("phase = 'persistence-verification'", stopped)
  const restarted = runner.indexOf("phase = 'stronghold-restart'", verified)
  assert.ok(stopped > 0 && stopped < verified && verified < restarted)
})

test('native session revocation exposes bounded lifecycle phases', () => {
  const runner = readFileSync(new URL('./run.mjs', import.meta.url), 'utf8')
  for (const phase of ['control', 'login-window', 'relogin-submit', 'workspace-timeout', 'workspace-authentication', 'workspace-unavailable', 'workspace-login-stalled', 'workspace-loading-stalled', 'workspace-forbidden', 'workspace-not-found', 'navigation', 'files']) {
    assert.match(runner, new RegExp(`phase = 'session-revocation-${phase}'`))
  }
})

test('native permission verification handshakes before returning through the real router', () => {
  const runner = readFileSync(new URL('./run.mjs', import.meta.url), 'utf8')
  const nativeApp = readFileSync(join(repositoryRoot, 'frontend/apps/admin-desktop/src/native-e2e/App.vue'), 'utf8')
  assert.match(nativeApp, /if \(action === 'permissions-on'\) nativeAuthorization\.value = 'E2E authorization restored'[\s\S]*workspaceKey\.value \+= 1/)
  assert.match(runner, /phase = 'permission-disable-control'[\s\S]*clickButton\(app\.child\.pid, 'E2E permissions off'\)[\s\S]*phase = 'permission-denied-boundary'[\s\S]*pollControl\(app\.child\.pid, 'revoked permission request denied', 'E2E authorization denied'\)[\s\S]*phase = 'permission-hidden'[\s\S]*poll\('revoked permission capability hidden',[\s\S]*phase = 'permission-enable-control'[\s\S]*clickButton\(app\.child\.pid, 'E2E permissions on'\)[\s\S]*phase = 'permission-enable-boundary'[\s\S]*pollControl\(app\.child\.pid, 'permission capability enabled', 'E2E authorization restored'\)[\s\S]*phase = 'permission-navigation'[\s\S]*openFiles\(app\.child\.pid\)[\s\S]*phase = 'permission-restored'[\s\S]*poll\('permission capability restored'/)
  assert.doesNotMatch(runner, /phase = 'permission-authorization'/)
})

test('native Files waits expose only fixed failure classifications', () => {
  const runner = readFileSync(new URL('./run.mjs', import.meta.url), 'utf8')
  for (const state of ['current-busy', 'current', 'route-load', 'product-unavailable', 'no-projection', 'forbidden', 'not-found', 'runtime-unavailable', 'loading', 'login', 'workspace', 'unknown']) {
    assert.match(runner, new RegExp(`\\$\\{prefix\\}-${state}`))
  }
  for (const phase of ['login-files', 'session-revocation-files', 'stronghold-restart-files']) {
    assert.match(runner, new RegExp(`classifyFilesPageFailure\\(app\\.child\\.pid, '${phase}'\\)`))
  }
  assert.match(runner, /poll\('native Files page'[\s\S]*classifyFilesPageFailure\(app\.child\.pid, 'login-files'\)/)
  assert.match(runner, /poll\('native Files page after relogin'[\s\S]*classifyFilesPageFailure\(app\.child\.pid, 'session-revocation-files'\)/)
  assert.match(runner, /poll\('Stronghold session restart'[\s\S]*classifyFilesPageFailure\(app\.child\.pid, 'stronghold-restart-files'\)/)
  assert.match(runner, /const openFiles = pid => runAppleScript\(clickButtonScript\(pid, 'E2E open Files'\)\)/)
  assert.equal((runner.match(/openFiles\(app\.child\.pid\)/g) ?? []).length, 4)
  assert.doesNotMatch(runner, /clickButton\(app\.child\.pid, '文件管理'\)/)
  assert.ok(runner.indexOf("buttonCurrentScript(pid, '文件管理')") < runner.indexOf("windowContains(pid, '页面加载失败')"))
  assert.ok(runner.indexOf('windowBusyScript(pid)') < runner.indexOf("windowContains(pid, '页面加载失败')"))
})

test('native role deletion is uniquely named',()=>{
 const runner=readFileSync(new URL('./run.mjs',import.meta.url),'utf8')
 assert.match(runner,/删除角色 e2e-role/)
})

test('accessibility query walks the native flattened element collection', () => {
  assert.equal(quoteAppleScript('a\\"b'), '"a\\\\\\"b"')
  const script = windowContainsScript(42, '产品搜索')
  assert.match(script, /set elementsToScan to entire contents of window 1/)
  assert.match(script, /repeat with currentElement in elementsToScan/)
  assert.match(script, /if \(name of currentElement as text\) contains expectedValue/)
  assert.doesNotMatch(script, /every UI element of entire contents/)
  assert.match(clickButtonScript(42, '保存'), /role of currentElement is "AXButton" and name of currentElement is "\u4fdd\u5b58"/)
  assert.match(buttonCurrentScript(42, '文件管理'), /value of attribute "AXARIACurrent" of currentElement as text\) is "page"/)
  assert.match(windowBusyScript(42), /value of attribute "AXBusy" of currentElement is true/)
  const form = fillAndSubmitScript(42, [{ name: '名称', value: 'Native product' }], '保存')
  assert.match(form, /name of currentElement is "\u540d\u79f0"/)
  assert.match(form, /keystroke "Native product"/)
  assert.match(form, /name of currentElement is "\u4fdd\u5b58"/)
  assert.match(form, /set focused of submitControl to true/)
  assert.match(form, /key code 36/)
  assert.doesNotMatch(form, /click submitControl/)
  assert.equal((form.match(/set elementsToScan to entire contents of window 1/g) ?? []).length, 2)
  assert.match(windowValueScript(42, 'E2E boundary blocked:'), /observedName starts with "E2E boundary blocked:"/)
  assert.match(windowFrameScript(42), /set windowPosition to position of window 1/)
  assert.match(windowFrameScript(42), /set windowSize to size of window 1/)
})

test('native runner fails when the required opt-in is missing', () => {
  const result = spawnSync(process.execPath, [fileURLToPath(new URL('./run.mjs', import.meta.url))], {
    env: { PATH: process.env.PATH ?? '' }, encoding: 'utf8'
  })
  assert.equal(result.status, 1)
  assert.equal(result.stdout, '')
  assert.equal(result.stderr, 'desktop native E2E requires GO_ADMIN_DESKTOP_NATIVE_E2E=1\n')
})

test('native runner covers empty first setup, restart, and an exact non-skip pass marker', () => {
  const runner = readFileSync(new URL('./run.mjs', import.meta.url), 'utf8')
  const firstSetupGate = readFileSync(join(repositoryRoot, 'frontend/apps/admin-desktop/src/first-setup/FirstSetupGate.vue'), 'utf8')
  for (const phase of [
    'first-setup-recovery-root', 'first-setup-recovery-window', 'first-setup-recovery-window-frame', 'first-setup-recovery-submit',
    'first-setup-recovery-fault', 'first-setup-recovery-state', 'first-setup-recovery-restore',
    'first-setup-recovery-continue', 'first-setup-recovery-login', 'first-setup-recovery-restart',
    'first-setup-root', 'first-setup-window', 'first-setup-window-frame', 'first-setup-submit', 'first-setup-workspace', 'first-setup-boundary', 'first-setup-restart',
    'login-window-frame'
  ]) {
    assert.match(runner, new RegExp(`phase = '${phase}'`))
  }
  assert.match(runner, /rename\(recoverySnapshot, recoverySnapshotBackup\)[\s\S]*recoveryFaultActive = true[\s\S]*mkdir\(recoverySnapshot, \{ mode: 0o700 \}\)/)
  assert.match(runner, /restoreRecoverySnapshot\(\)[\s\S]*clickButton\(app\.child\.pid, '\u8fdb\u5165\u767b\u5f55'\)/)
  assert.match(runner, /phase = 'cleanup-recovery-permissions'[\s\S]*restoreRecoverySnapshot\(\)[\s\S]*chmod\(recoverySnapshot, 0o600\)/)
  assert.match(runner, /windowContains\(app\.child\.pid, '\u7ba1\u7406\u5458\u5df2\u521b\u5efa'\)/)
  for (const phase of ['setup', 'workspace', 'login', 'unavailable', 'unknown']) {
    assert.match(runner, new RegExp(`return 'first-setup-recovery-state-${phase}'`))
  }
  for (const phase of ['setup', 'recovery', 'login', 'unavailable', 'loading', 'unknown']) {
    assert.match(runner, new RegExp(`return 'first-setup-workspace-${phase}'`))
  }
  assert.match(runner, /await poll\('native first setup workspace',[\s\S]*catch \(error\) \{[\s\S]*phase = await classifyFirstSetupWorkspaceFailure\(app\.child\.pid\)[\s\S]*throw error/)
  assert.match(runner, /await poll\('native first setup workspace',[\s\S]*phase = 'first-setup-boundary'[\s\S]*await pollBoundary\(app\.child\.pid\)[\s\S]*await stopTracked\(app\)/)
  assert.match(runner, /await poll\('native partial setup recovery',[\s\S]*catch \(error\) \{[\s\S]*phase = await classifyFirstSetupRecoveryFailure\(app\.child\.pid\)[\s\S]*throw error/)
  assert.match(runner, /phase = 'first-setup-recovery-restore'[\s\S]*restoreRecoverySnapshot\(\)[\s\S]*phase = 'first-setup-recovery-continue'[\s\S]*clickButton\(app\.child\.pid, '\u8fdb\u5165\u767b\u5f55'\)[\s\S]*phase = 'first-setup-recovery-login'[\s\S]*poll\('native recovery login window'/)
  for (const phase of ['recovery', 'workspace', 'setup', 'unavailable', 'loading', 'unknown']) {
    assert.match(runner, new RegExp(`return 'first-setup-recovery-login-${phase}'`))
  }
  assert.match(runner, /await poll\('native recovery login window',[\s\S]*catch \(error\) \{[\s\S]*phase = await classifyFirstSetupRecoveryLoginFailure\(app\.child\.pid\)[\s\S]*throw error/)
  assert.match(firstSetupGate, /import \{ createDesktopSession \} from '@go-admin-plus\/adapter-desktop'/)
  assert.match(firstSetupGate, /const session = createDesktopSession\(\)/)
  assert.match(firstSetupGate, /const continueToLogin = async \(\) => \{[\s\S]*await session\.logout\(\)[\s\S]*openWorkspace\(\)/)
  assert.match(firstSetupGate, /if \(outcome\.state === 'complete'\) openWorkspace\(\)/)
  assert.doesNotMatch(firstSetupGate, /if \(outcome\.state === 'complete'\) continueToLogin\(\)/)
  assert.match(firstSetupGate, /v-if="error" class="first-setup-error" role="alert"[\s\S]*:disabled="submitting" @click="continueToLogin"/)
  assert.match(runner, /completeFirstSetup\(app\.child\.pid\)/)
  assert.match(runner, /if \(width < 960 \|\| height < 640\)/)
  assert.match(runner, /hostTriple\('darwin', process\.arch\)/)
  assert.match(runner, /DESKTOP_NATIVE_E2E_PASS runtime=tauri-native profile=sqlite skipped=0/)
  assert.doesNotMatch(runner, /state: 'skipped'|process\.exit\(0\)/)
})

test('controlled exit one represents an empty process query', async () => {
  const output = await execute(process.execPath, ['-e', 'process.exit(1)'], { allowedExitCodes: [0, 1] })
  assert.equal(output, '')
  const processes = await sidecarProcesses(async (command, args, options) => {
    assert.equal(command, '/usr/bin/pgrep')
    assert.deepEqual(args, ['-lf', 'go-admin-sidecar'])
    assert.deepEqual(options.allowedExitCodes, [0, 1])
    return ''
  })
  assert.deepEqual([...processes], [])
})

test('process parser accepts only an exact approved sidecar executable', () => {
  const processes = parseSidecarProcesses([
    '101 /tmp/go-admin-sidecar-aarch64-apple-darwin --desktop',
    '102 /tmp/go-admin-sidecar-x86_64-apple-darwin',
    '103 /tmp/go-admin-sidecar-x86_64-pc-windows-msvc.exe',
    '109 /tmp/go-admin-sidecar',
    '110 /tmp/go-admin-sidecar.exe',
    '104 node test.mjs go-admin-sidecar-aarch64-apple-darwin',
    '105 /bin/sh -c /tmp/go-admin-sidecar-aarch64-apple-darwin',
    '106 /tmp/go-admin-sidecar-aarch64-apple-darwin-copy',
    '107 /tmp/go-admin-sidecar-aarch64-apple-darwin.exe',
    '108 /tmp/go-admin-sidecar-x86_64-pc-windows-msvc',
    'not-a-pid /tmp/go-admin-sidecar-aarch64-apple-darwin'
  ].join('\n'))
  assert.deepEqual([...processes], [101, 102, 103, 109, 110])
})

test('bounded command failures wait for killed process pipes to close', async () => {
  await assert.rejects(
    execute(process.execPath, ['-e', 'process.stdout.write("x".repeat(32768));setInterval(()=>{},1000)']),
    /output exceeded/
  )
  await assert.rejects(
    execute(process.execPath, ['-e', 'setInterval(()=>{},1000)'], { timeout: 10 }),
    /timed out/
  )
})

test('cleanup signals only exact sidecars outside the baseline and verifies reaping', async () => {
  const baseline = new Set([100])
  let current = new Set([100, 200, 300])
  const signals = []
  const leaked = await reapNewSidecars(baseline, {
    query: async () => new Set(current),
    signal(pid, name) {
      signals.push([pid, name])
      if (pid === 200 && name === 'SIGTERM') current.delete(pid)
      if (pid === 300 && name === 'SIGKILL') current.delete(pid)
    },
    pause: async () => {},
    timeout: 0
  })
  assert.deepEqual([...leaked], [200, 300])
  assert.deepEqual(signals, [[200, 'SIGTERM'], [300, 'SIGTERM'], [300, 'SIGKILL']])
  assert.deepEqual([...current], [100])
})
