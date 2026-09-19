import { spawn, spawnSync } from 'node:child_process'
import { existsSync, mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { request as httpsRequest } from 'node:https'
import { tmpdir } from 'node:os'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { browserDiagnostic, runnerFailureLine, safeRunnerDiagnostic } from './diagnostics.mjs'
import { activeChildren, assertChildHealthy, CDPClient, delay, spawnTracked, terminateChild, withTimeout } from './runner-support.mjs'

const required = process.env.GO_ADMIN_REQUIRE_IAM_ADMIN_E2E === '1'
if (!required) { console.log('IAM_ADMIN_E2E_SKIP required opt-in is disabled'); process.exit(0) }
const chromium = process.env.GO_ADMIN_TEST_CHROMIUM_EXECUTABLE
const postgresKey = 'GO_ADMIN_TEST_POSTGRES_DISPOSABLE_DSN'
const postgres = process.env[postgresKey]
if (!chromium || !postgres) { console.error('IAM_ADMIN_E2E_RUN_FAIL|required environment is missing'); process.exit(1) }

const root = dirname(fileURLToPath(import.meta.url)); const uiRoot = resolve(root, '../../../..'); const backend = resolve(uiRoot, '../backend')
const temporary = mkdtempSync(join(tmpdir(), 'go-admin-iam-admin-e2e-')); const staticRoot = join(temporary, 'static')
const overallDeadline = Date.now() + 5 * 60_000
const allowedKeys = ['APPDATA', 'CC', 'CGO_ENABLED', 'COMSPEC', 'COREPACK_HOME', 'CXX', 'GOCACHE', 'GOENV', 'GOMODCACHE', 'GONOPROXY', 'GONOSUMDB', 'GOPATH', 'GOPRIVATE', 'GOPROXY', 'GOROOT', 'GOSUMDB', 'GOTOOLCHAIN', 'HOME', 'LANG', 'LC_ALL', 'LOCALAPPDATA', 'NO_COLOR', 'PATH', 'PATHEXT', 'PNPM_HOME', 'SSL_CERT_DIR', 'SSL_CERT_FILE', 'SystemRoot', 'TEMP', 'TMP', 'TMPDIR', 'USERPROFILE', 'WINDIR', 'XDG_CACHE_HOME', 'XDG_CONFIG_HOME', 'XDG_RUNTIME_DIR']
const remaining = (maximum) => { const value = Math.min(maximum, overallDeadline-Date.now()); if (value <= 0) throw new Error('overall deadline exceeded'); return value }
const environment = (extra = {}, includePostgres = false) => { const result = {}; for (const key of allowedKeys) if (process.env[key] !== undefined) result[key] = process.env[key]; for (const [key, value] of Object.entries(extra)) if (value !== undefined) result[key] = String(value); delete result[postgresKey]; if (includePostgres) result[postgresKey] = postgres; return result }
const checked = (command, args, options) => { const result = spawnSync(command, args, { ...options, encoding: 'utf8', timeout: remaining(120_000), killSignal: 'SIGKILL' }); if (result.status !== 0 || result.error || result.signal) throw new Error(options.failure ?? 'compile command failed') }
const waitReady = async (path, child, profile) => { const deadline = Date.now()+remaining(60_000); while (Date.now()<deadline) { if (existsSync(path)) return readFileSync(path, 'utf8').trim(); assertChildHealthy(child, `${profile} HTTPS host`); await delay(100) } throw new Error(`${profile} HTTPS host readiness timed out`) }
const waitForDevTools = (child) => withTimeout(new Promise((resolvePromise, reject) => {
  let buffered = ''; child.stderr.setEncoding('utf8')
  child.stderr.on('data', (chunk) => { buffered = (buffered+chunk).slice(-8192); const match = buffered.match(/DevTools listening on (ws:\/\/[^\s]+)/); if (match) resolvePromise(match[1]) })
  child.once('error', () => reject(new Error('Chromium could not start'))); child.once('exit', () => reject(new Error('Chromium exited before DevTools readiness')))
}), remaining(30_000), 'Chromium DevTools readiness')
const connectWebSocket = (url) => { const socket = new WebSocket(url); return withTimeout(new Promise((resolvePromise, reject) => { socket.addEventListener('open', () => resolvePromise(socket), { once: true }); socket.addEventListener('error', () => reject(new Error('CDP connection failed')), { once: true }) }), remaining(15_000), 'CDP connection', () => socket.close()) }
const waitForTarget = async (client, baseURL) => { const deadline = Date.now()+remaining(30_000); while (Date.now()<deadline) { const { targetInfos } = await client.send('Target.getTargets'); const target = targetInfos.find((candidate) => candidate.type === 'page' && candidate.url.startsWith(baseURL)); if (target) return target.targetId; await delay(100) } throw new Error('browser page target did not load') }
const evaluate = async (client, sessionId, expression) => { const outcome = await client.send('Runtime.evaluate', { expression, returnByValue: true }, sessionId); if (outcome.exceptionDetails) throw new Error('browser result evaluation failed'); return String(outcome.result.value ?? '') }
const waitForResult = async (client, sessionId, browser, profile) => { const deadline = Date.now()+remaining(120_000); while (Date.now()<deadline) { assertChildHealthy(browser, 'Chromium'); const result = await evaluate(client, sessionId, "document.querySelector('#result')?.textContent ?? ''"); if (result.includes('IAM_ADMIN_E2E_PASS')) return; if (result.includes('IAM_ADMIN_E2E_FAIL|ASSERTION|')) throw new Error(`${profile} browser scenario failed: ${browserDiagnostic(result)}`); await delay(100) } throw new Error(`${profile} browser result timed out`) }
const shutdownHost = (baseURL) => {
  if (!baseURL) return Promise.resolve(); let request
  const operation = new Promise((resolvePromise, reject) => { request = httpsRequest(new URL('/__test/shutdown', baseURL), { method: 'POST', rejectUnauthorized: false }, (response) => { response.resume(); response.once('end', resolvePromise) }); request.once('error', () => reject(new Error('HTTPS host shutdown failed'))); request.end() })
  return withTimeout(operation, 5_000, 'HTTPS host shutdown', () => request?.destroy())
}
const runProfile = async (profile) => {
  const profileRoot = join(temporary, profile); const ready = join(profileRoot, 'ready'); const browserRoot = join(profileRoot, 'chromium')
  const host = spawnTracked(spawn, 'go', ['test', './test/iam/authorization', '-run', '^TestIAMAdministrationBrowserHarnessServer$', '-count=1', '-v'], { cwd: backend, env: environment({ GO_ADMIN_IAM_ADMIN_E2E_SERVE: '1', GO_ADMIN_IAM_ADMIN_E2E_PROFILE: profile, GO_ADMIN_IAM_ADMIN_E2E_READY_FILE: ready, GO_ADMIN_IAM_ADMIN_E2E_STATIC_DIR: staticRoot }, profile === 'postgres'), stdio: ['ignore', 'pipe', 'pipe'], drainStdout: true, drainStderr: true })
  let browser; let baseURL; let socket; let cdp; let completed = false; let operationError
  try {
    baseURL = await waitReady(ready, host, profile)
    browser = spawnTracked(spawn, chromium, ['--headless=new', '--disable-gpu', '--ignore-certificate-errors', '--no-first-run', '--no-default-browser-check', '--remote-debugging-port=0', `--user-data-dir=${browserRoot}`, baseURL], { env: environment(), stdio: ['ignore', 'ignore', 'pipe'] })
    const devToolsURL = await waitForDevTools(browser); socket = await connectWebSocket(devToolsURL); cdp = new CDPClient(socket, 10_000)
    const targetID = await waitForTarget(cdp, baseURL); const { sessionId } = await cdp.send('Target.attachToTarget', { targetId: targetID, flatten: true }); await cdp.send('Runtime.enable', {}, sessionId)
    await waitForResult(cdp, sessionId, browser, profile); completed = true
  } catch (error) {
    operationError = error
  } finally {
    const hostResult = await terminateChild(host, () => shutdownHost(baseURL)); const browserResult = await terminateChild(browser, async () => { if (cdp) await cdp.send('Browser.close'); else socket?.close() })
    try { socket?.close() } catch { browserResult.shutdownFailed = true }
    if (!hostResult.exited || !browserResult.exited) operationError ??= new Error('test child cleanup failed')
    if (completed && (hostResult.forced || browserResult.forced || browserResult.shutdownFailed || host.exitCode !== 0)) operationError ??= new Error('test child did not shut down cleanly')
  }
  if (operationError) throw operationError
}

let failure = ''
try {
  checked('pnpm', ['exec', 'vite', 'build', '--config', join(root, 'vite.config.ts')], { cwd: uiRoot, env: environment({ GO_ADMIN_IAM_ADMIN_E2E_OUT_DIR: staticRoot }), stdio: 'pipe', failure: 'browser fixture build failed' })
  checked('go', ['test', './test/iam/authorization', '-run', '^TestIAMAdministrationBrowserHarnessServer$', '-count=1'], { cwd: backend, env: environment({ GO_ADMIN_IAM_ADMIN_E2E_SERVE: '0' }), stdio: 'pipe', failure: 'HTTPS host compile self-check failed' })
  await runProfile('sqlite'); await runProfile('postgres')
} catch (error) { failure = safeRunnerDiagnostic(error) }
for (const child of activeChildren) await terminateChild(child)
if (activeChildren.size === 0) { try { rmSync(temporary, { recursive: true, force: true }) } catch { if (!failure) failure = 'temporary cleanup failed' } } else if (!failure) failure = 'test child cleanup failed'
if (failure) { console.error(runnerFailureLine(failure)); process.exitCode = 1 } else console.log('IAM_ADMIN_E2E_PASS profiles=sqlite,postgres')
