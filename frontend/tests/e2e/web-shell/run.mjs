import { spawn, spawnSync } from 'node:child_process'
import { existsSync, mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { request as httpsRequest } from 'node:https'
import { tmpdir } from 'node:os'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { activeChildren, assertChildHealthy, CDPClient, delay, spawnTracked, terminateChild, withTimeout } from '../iam/administration/runner-support.mjs'

if (process.env.GO_ADMIN_REQUIRE_WEB_SHELL_E2E !== '1') {
  console.log('WEB_SHELL_E2E_SKIP required opt-in is disabled')
  process.exit(0)
}
const chromium = process.env.GO_ADMIN_TEST_CHROMIUM_EXECUTABLE
const postgresKey = 'GO_ADMIN_TEST_POSTGRES_DISPOSABLE_DSN'
const postgres = process.env[postgresKey]
if (!chromium || !postgres) {
  console.error('WEB_SHELL_E2E_RUN_FAIL|required environment is missing')
  process.exit(1)
}

const root = dirname(fileURLToPath(import.meta.url))
const uiRoot = resolve(root, '../../..')
const repositoryRoot = resolve(uiRoot, '..')
const backend = join(repositoryRoot, 'backend')
const temporary = mkdtempSync(join(tmpdir(), 'go-admin-web-shell-e2e-'))
const staticRoot = join(temporary, 'static')
const deadline = Date.now() + 12 * 60_000
const environmentKeys = [
  'APPDATA', 'CC', 'CGO_ENABLED', 'COMSPEC', 'COREPACK_HOME', 'CXX', 'GOCACHE', 'GOENV',
  'GOMODCACHE', 'GONOPROXY', 'GONOSUMDB', 'GOPATH', 'GOPRIVATE', 'GOPROXY', 'GOROOT',
  'GOSUMDB', 'GOTOOLCHAIN', 'HOME', 'LANG', 'LC_ALL', 'LOCALAPPDATA', 'NO_COLOR', 'PATH',
  'PATHEXT', 'PNPM_HOME', 'SSL_CERT_DIR', 'SSL_CERT_FILE', 'SystemRoot', 'TEMP', 'TMP',
  'TMPDIR', 'USERPROFILE', 'WINDIR', 'XDG_CACHE_HOME', 'XDG_CONFIG_HOME', 'XDG_RUNTIME_DIR',
]
const remaining = maximum => {
  const value = Math.min(maximum, deadline - Date.now())
  if (value <= 0) throw new Error('overall deadline exceeded')
  return value
}
const environment = (extra = {}, withPostgres = false) => {
  const value = {}
  for (const key of environmentKeys) if (process.env[key] !== undefined) value[key] = process.env[key]
  Object.assign(value, extra)
  delete value[postgresKey]
  if (withPostgres) value[postgresKey] = postgres
  return value
}
const checked = (command, args, options) => {
  const result = spawnSync(command, args, { ...options, encoding: 'utf8', timeout: remaining(options.timeout ?? 180_000), killSignal: 'SIGKILL' })
  if (result.status !== 0 || result.error || result.signal) throw new Error(options.failure)
}
const waitReady = async (path, host, profile) => {
  const until = Date.now() + remaining(90_000)
  while (Date.now() < until) {
    if (existsSync(path)) return readFileSync(path, 'utf8').trim()
    assertChildHealthy(host, `${profile} product host`)
    await delay(100)
  }
  throw new Error(`${profile} product host readiness timed out`)
}
const devTools = child => withTimeout(new Promise((resolvePromise, reject) => {
  let output = ''
  child.stderr.setEncoding('utf8')
  child.stderr.on('data', chunk => {
    output = (output + chunk).slice(-8192)
    const match = output.match(/DevTools listening on (ws:\/\/[^\s]+)/)
    if (match) resolvePromise(match[1])
  })
  child.once('error', () => reject(new Error('Chromium could not start')))
  child.once('exit', () => reject(new Error('Chromium exited before readiness')))
}), remaining(30_000), 'Chromium readiness')
const connect = url => {
  const socket = new WebSocket(url)
  return withTimeout(new Promise((resolvePromise, reject) => {
    socket.addEventListener('open', () => resolvePromise(socket), { once: true })
    socket.addEventListener('error', () => reject(new Error('CDP connection failed')), { once: true })
  }), remaining(15_000), 'CDP connection', () => socket.close())
}
const shutdown = baseURL => new Promise(resolvePromise => {
  if (!baseURL) { resolvePromise(); return }
  const request = httpsRequest(new URL('/__test/shutdown', baseURL), { method: 'POST', rejectUnauthorized: false }, response => {
    response.resume()
    response.once('end', resolvePromise)
  })
  request.once('error', resolvePromise)
  request.end()
})

const runProfile = async profile => {
  const profileRoot = join(temporary, profile)
  const ready = join(profileRoot, 'ready')
  const host = spawnTracked(spawn, 'go', ['test', './test/e2e/web', '-run', '^TestProductWebShellHarness$', '-count=1', '-v'], {
    cwd: backend,
    env: environment({
      GO_ADMIN_WEB_SHELL_E2E_SERVE: '1',
      GO_ADMIN_WEB_SHELL_E2E_PROFILE: profile,
      GO_ADMIN_WEB_SHELL_E2E_READY_FILE: ready,
      GO_ADMIN_WEB_SHELL_E2E_STATIC_DIR: staticRoot,
      GO_ADMIN_WEB_SHELL_E2E_REPOSITORY_ROOT: repositoryRoot,
    }, profile === 'postgres'),
    stdio: ['ignore', 'pipe', 'pipe'], drainStdout: true, drainStderr: true,
  })
  let browser
  let baseURL
  let socket
  let cdp
  let failure
  let completed = false
  try {
    baseURL = await waitReady(ready, host, profile)
    const size = profile === 'sqlite' ? '1440,900' : '390,844'
    browser = spawnTracked(spawn, chromium, [
      '--headless=new', '--disable-gpu', '--ignore-certificate-errors', '--no-first-run',
      '--no-default-browser-check', '--force-prefers-reduced-motion', `--window-size=${size}`,
      '--remote-debugging-port=0', `--user-data-dir=${join(profileRoot, 'chromium')}`,
      `${baseURL}/iam/roles`,
    ], { env: environment(), stdio: ['ignore', 'ignore', 'pipe'] })
    socket = await connect(await devTools(browser))
    cdp = new CDPClient(socket, 15_000)
    let page
    const targetDeadline = Date.now() + remaining(30_000)
    while (Date.now() < targetDeadline && !page) {
      const values = await cdp.send('Target.getTargets')
      page = values.targetInfos.find(value => value.type === 'page' && value.url.startsWith(baseURL))
      if (!page) await delay(100)
    }
    if (!page) throw new Error('browser target did not load')
    const attached = await cdp.send('Target.attachToTarget', { targetId: page.targetId, flatten: true })
    await cdp.send('Runtime.enable', {}, attached.sessionId)
    await cdp.send('Page.enable', {}, attached.sessionId)
    const resultDeadline = Date.now() + remaining(240_000)
    while (Date.now() < resultDeadline) {
      assertChildHealthy(browser, 'Chromium')
      try {
        const value = await cdp.send('Runtime.evaluate', { expression: "document.querySelector('#result')?.textContent ?? ''", returnByValue: true }, attached.sessionId)
        const marker = String(value.result.value ?? '')
        if (marker.includes('WEB_SHELL_E2E_PASS')) {
          const screenshot = await cdp.send('Page.captureScreenshot', { format: 'png', captureBeyondViewport: false }, attached.sessionId)
          if (typeof screenshot.data !== 'string' || screenshot.data.length < 1000) throw new Error('browser screenshot is empty')
          completed = true
          break
        }
        if (marker.includes('WEB_SHELL_E2E_FAIL|')) {
          const category = marker.split('|').slice(1).join('|')
          throw new Error(`${profile} product shell assertion failed: ${/^[a-zA-Z0-9 /:-]{1,420}$/.test(category) ? category : 'browser assertion failed'}`)
        }
      } catch (error) {
        if (error instanceof Error && !/CDP command failed|execution context/i.test(error.message)) throw error
      }
      await delay(100)
    }
    if (!completed) throw new Error(`${profile} product shell timed out`)
  } catch (error) { failure = error }
  finally {
    const hostResult = await terminateChild(host, () => shutdown(baseURL), 15_000)
    const browserResult = await terminateChild(browser, async () => { if (cdp) await cdp.send('Browser.close'); else socket?.close() })
    try { socket?.close() } catch { browserResult.shutdownFailed = true }
    if (!hostResult.exited || !browserResult.exited) failure ??= new Error('test child cleanup failed')
    if (completed && (hostResult.forced || browserResult.forced || browserResult.shutdownFailed || host.exitCode !== 0)) failure ??= new Error('test child did not shut down cleanly')
  }
  if (failure) throw failure
}

let failure = ''
try {
  checked('pnpm', ['exec', 'vite', 'build', '--config', join(root, 'vite.config.ts')], {
    cwd: uiRoot, env: environment({ GO_ADMIN_WEB_SHELL_E2E_OUT_DIR: staticRoot }), stdio: 'pipe', failure: 'product shell fixture build failed',
  })
  checked('go', ['test', './test/e2e/web', '-run', '^TestProductWebShellHarness$', '-count=1'], {
    cwd: backend, env: environment({ GO_ADMIN_WEB_SHELL_E2E_SERVE: '0' }), stdio: 'pipe', failure: 'product shell host compile check failed',
  })
  await runProfile('sqlite')
  await runProfile('postgres')
} catch (error) {
  failure = error instanceof Error && /^[a-zA-Z0-9 .,:/|-]{1,500}$/.test(error.message) ? error.message : 'product shell E2E execution failed'
}
for (const child of activeChildren) await terminateChild(child)
if (activeChildren.size === 0) {
  try { rmSync(temporary, { recursive: true, force: true }) } catch { failure ||= 'temporary cleanup failed' }
} else failure ||= 'test child cleanup failed'
if (failure) {
  console.error(`WEB_SHELL_E2E_RUN_FAIL|${failure}`)
  process.exitCode = 1
} else console.log('WEB_SHELL_E2E_PASS profiles=sqlite,postgres viewports=desktop,mobile')
