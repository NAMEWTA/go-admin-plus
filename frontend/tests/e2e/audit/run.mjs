import { spawnSync } from 'node:child_process'
import { existsSync, mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { request as httpsRequest } from 'node:https'
import { tmpdir } from 'node:os'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { browserExceptionDiagnostic, createDiagnosticBuffer, redactDiagnostic } from './diagnostics.mjs'
import { cleanupTrackedChildren, didSpawnFail, spawnTracked, terminateChild, waitForExit } from './process-lifecycle.mjs'

if (process.env.GO_ADMIN_REQUIRE_AUDIT_E2E !== '1') {
  console.log('AUDIT_E2E_SKIP: set GO_ADMIN_REQUIRE_AUDIT_E2E=1 for the required SQLite/PostgreSQL browser harness')
  process.exit(0)
}

const testRoot = dirname(fileURLToPath(import.meta.url))
const uiRoot = resolve(testRoot, '../../..')
const backendRoot = resolve(uiRoot, '../backend')
const postgresKey = 'GO_ADMIN_TEST_POSTGRES_DISPOSABLE_DSN'
const postgresDSN = process.env[postgresKey]
const chromium = process.env.GO_ADMIN_TEST_CHROMIUM_EXECUTABLE
const pnpm = 'pnpm'
const temporaryRoot = mkdtempSync(join(tmpdir(), 'go-admin-audit-e2e-'))
const staticRoot = join(temporaryRoot, 'static')
const deadline = Date.now() + 5 * 60_000
const activeChildren = new Set()
const diagnosticSecrets = [postgresDSN, 'correct horse battery', 'incorrect password']

const fail = (message) => { throw new Error(`Audit E2E: ${message}`) }
const assert = (condition, message) => { if (!condition) fail(message) }
const delay = (milliseconds) => new Promise((resolvePromise) => setTimeout(resolvePromise, milliseconds))
const safeEnvironmentKeys = [
  'APPDATA', 'CC', 'CGO_ENABLED', 'COMSPEC', 'COREPACK_HOME', 'CXX', 'GOCACHE', 'GOENV',
  'GOMODCACHE', 'GONOPROXY', 'GONOSUMDB', 'GOPATH', 'GOPRIVATE', 'GOPROXY', 'GOROOT',
  'GOSUMDB', 'GOTOOLCHAIN', 'HOME', 'LANG', 'LC_ALL', 'LOCALAPPDATA', 'NO_COLOR', 'PATH',
  'PATHEXT', 'PNPM_HOME', 'SSL_CERT_DIR', 'SSL_CERT_FILE', 'SystemRoot', 'TEMP', 'TMP',
  'TMPDIR', 'USERPROFILE', 'WINDIR', 'XDG_CACHE_HOME', 'XDG_CONFIG_HOME', 'XDG_RUNTIME_DIR',
]

const childEnvironment = (extra = {}, includePostgres = false) => {
  const environment = {}
  for (const key of safeEnvironmentKeys) if (process.env[key] !== undefined) environment[key] = process.env[key]
  for (const [key, value] of Object.entries(extra)) if (value !== undefined) environment[key] = String(value)
  if (includePostgres) environment[postgresKey] = postgresDSN
  return environment
}

const remaining = (maximum = 60_000) => {
  const milliseconds = Math.min(maximum, deadline - Date.now())
  if (milliseconds <= 0) fail('overall timeout exceeded')
  return milliseconds
}

class CDPClient {
  constructor(socket) {
    this.socket = socket
    this.nextID = 1
    this.pending = new Map()
    socket.addEventListener('message', (event) => {
      let message
      try { message = JSON.parse(String(event.data)) } catch { this.rejectAll(); return }
      if (!message.id) return
      const pending = this.pending.get(message.id)
      if (!pending) return
      this.pending.delete(message.id)
      clearTimeout(pending.timer)
      if (message.error) pending.reject(new Error(`CDP command failed: ${redactDiagnostic(message.error.message, diagnosticSecrets)}`))
      else pending.resolve(message.result)
    })
    socket.addEventListener('close', () => this.rejectAll())
  }

  rejectAll() {
    for (const pending of this.pending.values()) {
      clearTimeout(pending.timer)
      pending.reject(new Error('CDP connection closed'))
    }
    this.pending.clear()
  }

  send(method, params = {}, sessionId) {
    return new Promise((resolvePromise, reject) => {
      const id = this.nextID++
      const timer = setTimeout(() => { this.pending.delete(id); reject(new Error('CDP command timed out')) }, remaining(10_000))
      this.pending.set(id, { resolve: resolvePromise, reject, timer })
      this.socket.send(JSON.stringify({ id, method, params, ...(sessionId ? { sessionId } : {}) }))
    })
  }
}

const waitForReady = async (path, host) => {
  const timeout = Date.now() + remaining()
  while (Date.now() < timeout) {
    if (existsSync(path)) return readFileSync(path, 'utf8').trim()
    if (didSpawnFail(host)) fail('Audit HTTPS host could not start')
    if (host.exitCode !== null || host.signalCode !== null) fail('Audit HTTPS host exited before readiness')
    await delay(100)
  }
  fail('Audit HTTPS host readiness timed out')
}

const waitForDevTools = (browser) => new Promise((resolvePromise, reject) => {
  let buffered = ''
  const timer = setTimeout(() => reject(new Error('Chromium DevTools readiness timed out')), remaining(30_000))
  browser.stderr.setEncoding('utf8')
  browser.stderr.on('data', (chunk) => {
    buffered = (buffered + chunk).slice(-8192)
    const match = buffered.match(/DevTools listening on (ws:\/\/[^\s]+)/)
    if (!match) return
    clearTimeout(timer)
    resolvePromise(match[1])
  })
  browser.once('exit', () => { clearTimeout(timer); reject(new Error('Chromium exited before readiness')) })
  browser.once('error', () => { clearTimeout(timer); reject(new Error('Chromium could not start')) })
})

const connect = (url) => new Promise((resolvePromise, reject) => {
  const socket = new WebSocket(url)
  const timer = setTimeout(() => { socket.close(); reject(new Error('CDP connection timed out')) }, remaining(15_000))
  socket.addEventListener('open', () => { clearTimeout(timer); resolvePromise(socket) }, { once: true })
  socket.addEventListener('error', () => { clearTimeout(timer); reject(new Error('CDP connection failed')) }, { once: true })
})

const waitForPage = async (client, baseURL) => {
  for (let attempt = 0; attempt < 300; attempt += 1) {
    const { targetInfos } = await client.send('Target.getTargets')
    const target = targetInfos.find((candidate) => candidate.type === 'page' && candidate.url.startsWith(baseURL))
    if (target) return target.targetId
    await delay(100)
  }
  fail('Audit browser page did not load')
}

const evaluate = async (client, sessionId, expression) => {
  const outcome = await client.send('Runtime.evaluate', { expression, awaitPromise: true, returnByValue: true }, sessionId)
  if (outcome.exceptionDetails) fail(`Audit browser scenario raised an exception: ${browserExceptionDiagnostic(outcome.exceptionDetails, diagnosticSecrets)}`)
  return outcome.result.value
}

const capture = (stream, buffer) => {
  stream?.setEncoding('utf8')
  stream?.on('data', (chunk) => buffer.append(chunk))
}

const profileDiagnostic = (profile, error, hostOutput, browserOutput) => {
  const sections = [`${profile}: ${redactDiagnostic(error instanceof Error ? error.message : error, diagnosticSecrets)}`]
  if (hostOutput.text()) sections.push(`host output:\n${hostOutput.text()}`)
  if (browserOutput.text()) sections.push(`browser output:\n${browserOutput.text()}`)
  return sections.join('\n')
}

const requestHostShutdown = (baseURL) => new Promise((resolvePromise) => {
  const request = httpsRequest(new URL('/__test/shutdown', baseURL), {
    method: 'POST', rejectUnauthorized: false, timeout: 5_000,
  }, (response) => {
    response.resume()
    response.once('end', () => resolvePromise(response.statusCode === 204))
  })
  request.once('error', () => resolvePromise(false))
  request.once('timeout', () => { request.destroy(); resolvePromise(false) })
  request.end()
})

const runProfile = async (profile) => {
  const profileRoot = join(temporaryRoot, profile)
  const readyFile = join(profileRoot, 'ready')
  const browserRoot = join(profileRoot, 'chromium')
  const host = spawnTracked('go', ['test', './test/audit', '-run', '^TestAuditUIHarnessServer$', '-count=1', '-v'], {
    cwd: backendRoot,
    env: childEnvironment({
      GO_ADMIN_AUDIT_E2E_SERVE: '1', GO_ADMIN_AUDIT_E2E_PROFILE: profile,
      GO_ADMIN_AUDIT_E2E_READY_FILE: readyFile, GO_ADMIN_AUDIT_E2E_STATIC_DIR: staticRoot,
    }, profile === 'postgres'),
    stdio: ['ignore', 'pipe', 'pipe'],
  }, activeChildren)
  const hostOutput = createDiagnosticBuffer(8_192, diagnosticSecrets)
  const browserOutput = createDiagnosticBuffer(8_192, diagnosticSecrets)
  capture(host.stdout, hostOutput)
  capture(host.stderr, hostOutput)
  let browser
  let socket
  let client
  let sessionId
  let baseURL
  let profileError
  try {
    baseURL = await waitForReady(readyFile, host)
    browser = spawnTracked(chromium, [
      '--headless=new', '--disable-gpu', '--ignore-certificate-errors', '--no-first-run',
      '--no-default-browser-check', '--remote-debugging-port=0', `--user-data-dir=${browserRoot}`, baseURL,
    ], { env: childEnvironment(), stdio: ['ignore', 'ignore', 'pipe'] }, activeChildren)
    capture(browser.stderr, browserOutput)
    const devToolsURL = await waitForDevTools(browser)
    socket = await connect(devToolsURL)
    client = new CDPClient(socket)
    const targetId = await waitForPage(client, baseURL)
    const attached = await client.send('Target.attachToTarget', { targetId, flatten: true })
    sessionId = attached.sessionId
    await client.send('Runtime.enable', {}, sessionId)
    for (let attempt = 0; attempt < 300; attempt += 1) {
      if (await evaluate(client, sessionId, 'Boolean(globalThis.__auditE2E)')) break
      if (attempt === 299) fail('Audit browser driver did not initialize')
      await delay(100)
    }
    assert(await evaluate(client, sessionId, 'globalThis.__auditE2E.run()') === true, `${profile} Audit browser scenario failed`)
    await evaluate(client, sessionId, 'globalThis.__auditE2E.shutdown()')
    const hostExit = await waitForExit(host, remaining(10_000))
    assert(hostExit.exited && hostExit.code === 0, `${profile} Audit HTTPS host failed`)
  } catch (error) {
    profileError = error
  } finally {
    if (host.exitCode === null && host.signalCode === null && baseURL) {
      await requestHostShutdown(baseURL)
      await waitForExit(host, 10_000)
    }
    if (client && browser?.exitCode === null && browser?.signalCode === null) {
      try { await client.send('Browser.close') } catch { /* bounded termination below */ }
      await waitForExit(browser, 5_000)
    }
    socket?.close()
    if (browser && browser.exitCode === null && browser.signalCode === null) await terminateChild(browser, 5_000)
    if (host.exitCode === null && host.signalCode === null) await terminateChild(host, 5_000)
  }
  if (profileError) fail(profileDiagnostic(profile, profileError, hostOutput, browserOutput))
}

let scenarioError
let cleanupError
try {
  assert(Boolean(chromium), 'set GO_ADMIN_TEST_CHROMIUM_EXECUTABLE to a Chromium executable')
  assert(Boolean(postgresDSN), `set ${postgresKey} to a disposable PostgreSQL database`)
  const build = spawnSync(pnpm, ['--dir', uiRoot, 'exec', 'vite', 'build', '--config', join(testRoot, 'vite.config.ts')], {
    cwd: uiRoot,
    env: childEnvironment({ GO_ADMIN_AUDIT_E2E_OUT_DIR: staticRoot }),
    encoding: 'utf8',
    stdio: 'pipe',
    timeout: remaining(120_000),
  })
  if (build.error || build.signal || build.status !== 0) {
    const output = redactDiagnostic(`${build.stdout ?? ''}\n${build.stderr ?? ''}`, diagnosticSecrets).slice(-8_192).trim()
    fail(`Audit browser bundle failed${output ? `:\n${output}` : ''}`)
  }
  await runProfile('sqlite')
  await runProfile('postgres')
  console.log('AUDIT_E2E_PASS')
} catch (error) {
  scenarioError = error
} finally {
  try { await cleanupTrackedChildren(activeChildren, 5_000) } catch (error) { cleanupError = error }
  rmSync(temporaryRoot, { recursive: true, force: true })
}
if (scenarioError) throw scenarioError
if (cleanupError) throw cleanupError
