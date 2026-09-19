import { spawn, spawnSync } from 'node:child_process'
import { existsSync, mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { request as httpsRequest } from 'node:https'
import { tmpdir } from 'node:os'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const testRoot = dirname(fileURLToPath(import.meta.url))
const uiRoot = resolve(testRoot, '../../../..')
const repositoryRoot = resolve(uiRoot, '..')
const backendRoot = join(repositoryRoot, 'backend')
const requireE2E = process.env.GO_ADMIN_REQUIRE_IAM_E2E === '1'
const chromium = process.env.GO_ADMIN_TEST_CHROMIUM_EXECUTABLE
const postgresEnvironmentKey = 'GO_ADMIN_TEST_POSTGRES_DISPOSABLE_DSN'
const postgresDSN = process.env[postgresEnvironmentKey]
const temporaryRoot = mkdtempSync(join(tmpdir(), 'go-admin-iam-e2e-'))
const staticRoot = join(temporaryRoot, 'static')
const overallDeadline = Date.now() + 5 * 60_000

const inheritedEnvironmentAllowlist = [
  'APPDATA', 'CC', 'CGO_ENABLED', 'COMSPEC', 'COREPACK_HOME', 'CXX', 'GOCACHE', 'GOENV',
  'GOMODCACHE', 'GONOPROXY', 'GONOSUMDB', 'GOPATH', 'GOPRIVATE', 'GOPROXY', 'GOROOT',
  'GOSUMDB', 'GOTOOLCHAIN', 'HOME', 'LANG', 'LC_ALL', 'LOCALAPPDATA', 'NO_COLOR', 'PATH',
  'PATHEXT', 'PNPM_HOME', 'SSL_CERT_DIR', 'SSL_CERT_FILE', 'SystemRoot', 'TEMP', 'TMP',
  'TMPDIR', 'USERPROFILE', 'WINDIR', 'XDG_CACHE_HOME', 'XDG_CONFIG_HOME', 'XDG_RUNTIME_DIR',
]

const fail = (message) => { throw new Error(`IAM session E2E: ${message}`) }
const assert = (condition, message) => { if (!condition) fail(message) }
const delay = (milliseconds) => new Promise((resolvePromise) => setTimeout(resolvePromise, milliseconds))
const remaining = (maximum) => {
  const value = Math.min(maximum, overallDeadline - Date.now())
  if (value <= 0) fail('overall deadline exceeded')
  return value
}

const childEnvironment = (extra = {}, includePostgres = false) => {
  const environment = {}
  for (const key of inheritedEnvironmentAllowlist) {
    if (process.env[key] !== undefined) environment[key] = process.env[key]
  }
  for (const [key, value] of Object.entries(extra)) {
    if (value !== undefined) environment[key] = String(value)
  }
  delete environment[postgresEnvironmentKey]
  if (includePostgres) {
    assert(Boolean(postgresDSN), 'PostgreSQL child environment requires connection material')
    environment[postgresEnvironmentKey] = postgresDSN
  }
  const permitted = new Set([...inheritedEnvironmentAllowlist, ...Object.keys(extra)])
  if (includePostgres) permitted.add(postgresEnvironmentKey)
  assert(Object.keys(environment).every((key) => permitted.has(key)), 'child environment escaped its allowlist')
  assert(Object.hasOwn(environment, postgresEnvironmentKey) === includePostgres, 'PostgreSQL material environment boundary failed')
  return environment
}

const withTimeout = (promise, timeout, label, onTimeout = () => {}) => new Promise((resolvePromise, reject) => {
  let settled = false
  const timer = setTimeout(() => {
    if (settled) return
    settled = true
    onTimeout()
    reject(new Error(`${label} timed out`))
  }, timeout)
  promise.then((value) => {
    if (settled) return
    settled = true
    clearTimeout(timer)
    resolvePromise(value)
  }, (error) => {
    if (settled) return
    settled = true
    clearTimeout(timer)
    reject(error)
  })
})
const withDeadline = (promise, maximum, label, onTimeout) => withTimeout(promise, remaining(maximum), label, onTimeout)

const runChecked = (command, args, options) => {
  const result = spawnSync(command, args, {
    cwd: options.cwd,
    env: options.env,
    encoding: 'utf8',
    stdio: options.quiet ? 'pipe' : 'inherit',
    timeout: remaining(options.timeout ?? 120_000),
    killSignal: 'SIGKILL',
  })
  if (result.error || result.signal || result.status !== 0) fail(options.failure ?? `${command} failed`)
}

const childStates = new WeakMap()
const activeChildren = new Set()
const spawnTracked = (command, args, options) => {
  const { drainStdout, drainStderr, ...spawnOptions } = options
  const child = spawn(command, args, spawnOptions)
  const state = { closed: false, spawned: false, spawnFailed: false, output: '' }
  childStates.set(child, state)
  activeChildren.add(child)
  child.once('spawn', () => { state.spawned = true })
  child.on('error', () => {
    if (state.spawned) return
    state.spawnFailed = true
    state.closed = true
    activeChildren.delete(child)
  })
  child.once('close', () => { state.closed = true; activeChildren.delete(child) })
  const capture = (chunk) => { state.output = (state.output + String(chunk)).slice(-8192) }
  if (drainStdout && child.stdout) child.stdout.on('data', capture)
  if (drainStderr && child.stderr) child.stderr.on('data', capture)
  return child
}

const assertChildHealthy = (child, label) => {
  const state = childStates.get(child)
  if (state?.spawnFailed) fail(`${label} could not start`)
  if (state?.closed || child.exitCode !== null) {
    const category = /static directory is unavailable/.test(state?.output ?? '') ? ' static fixture unavailable'
      : /migration failed/.test(state?.output ?? '') ? ' migration failed'
        : /parse PostgreSQL browser harness connection failed/.test(state?.output ?? '') ? ' PostgreSQL DSN invalid'
        : /readiness (?:directory|file) is unavailable/.test(state?.output ?? '') ? ' readiness unavailable'
          : /profile is invalid/.test(state?.output ?? '') ? ' profile invalid' : ''
    fail(`${label} exited unexpectedly${category}`)
  }
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
      if (message.error) pending.reject(new Error('CDP command failed'))
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
      const timer = setTimeout(() => {
        this.pending.delete(id)
        reject(new Error('CDP command timed out'))
      }, remaining(10_000))
      this.pending.set(id, { resolve: resolvePromise, reject, timer })
      try {
        this.socket.send(JSON.stringify({ id, method, params, ...(sessionId ? { sessionId } : {}) }))
      } catch {
        clearTimeout(timer)
        this.pending.delete(id)
        reject(new Error('CDP command could not be sent'))
      }
    })
  }
}

const connectWebSocket = (url) => {
  const socket = new WebSocket(url)
  return withDeadline(new Promise((resolvePromise, reject) => {
    socket.addEventListener('open', () => resolvePromise(socket), { once: true })
    socket.addEventListener('error', () => reject(new Error('CDP connection failed')), { once: true })
  }), 15_000, 'CDP connection', () => socket.close())
}

const waitForFile = async (path, child) => {
  const deadline = Date.now() + remaining(60_000)
  while (Date.now() < deadline) {
    if (existsSync(path)) return readFileSync(path, 'utf8').trim()
    assertChildHealthy(child, 'HTTPS test host')
    await delay(100)
  }
  fail('HTTPS test host readiness timed out')
}

const waitForDevTools = (child) => withDeadline(new Promise((resolvePromise, reject) => {
  let buffered = ''
  let settled = false
  child.stderr.setEncoding('utf8')
  child.stderr.on('data', (chunk) => {
    if (settled) return
    buffered = (buffered + chunk).slice(-8192)
    const match = buffered.match(/DevTools listening on (ws:\/\/[^\s]+)/)
    if (!match) return
    settled = true
    resolvePromise(match[1])
  })
  child.once('error', () => reject(new Error('Chromium could not start')))
  child.once('exit', () => reject(new Error('Chromium exited before DevTools readiness')))
}), 30_000, 'Chromium DevTools readiness')

const waitForTarget = async (client, baseURL) => {
  const deadline = Date.now() + remaining(30_000)
  while (Date.now() < deadline) {
    const { targetInfos } = await client.send('Target.getTargets')
    const target = targetInfos.find((candidate) => candidate.type === 'page' && candidate.url.startsWith(baseURL))
    if (target) return target.targetId
    await delay(100)
  }
  fail('browser page target did not load')
}

const evaluate = async (client, sessionId, expression) => {
  const outcome = await client.send('Runtime.evaluate', { expression, awaitPromise: true, returnByValue: true }, sessionId)
  if (outcome.exceptionDetails) {
    const description = String(outcome.exceptionDetails.exception?.description ?? outcome.exceptionDetails.text ?? '')
    const category = description.match(/Error: ([a-zA-Z0-9 ]{1,80})/)?.[1]
    fail(`browser scenario raised an exception${category ? `: ${category}` : ''}`)
  }
  return outcome.result.value
}

const currentCookie = async (client, sessionId) => {
  const { cookies } = await client.send('Network.getAllCookies', {}, sessionId)
  const matches = cookies.filter((cookie) => cookie.name === '__Host-go-admin-session')
  assert(matches.length === 1, 'expected exactly one host session cookie')
  return matches[0]
}

const shutdownHost = (baseURL) => {
  if (!baseURL) return Promise.resolve()
  let request
  const operation = new Promise((resolvePromise, reject) => {
    request = httpsRequest(new URL('/__test/shutdown', baseURL), { method: 'POST', rejectUnauthorized: false }, (response) => {
      response.resume()
      response.once('end', resolvePromise)
    })
    request.once('error', () => reject(new Error('HTTPS host shutdown failed')))
    request.end()
  })
  return withTimeout(operation, 5_000, 'HTTPS host shutdown', () => request?.destroy())
}

const waitForExit = (child, maximum) => new Promise((resolvePromise) => {
  const state = childStates.get(child)
  if (state?.closed) return resolvePromise(true)
  const timer = setTimeout(() => { cleanup(); resolvePromise(false) }, maximum)
  const exited = () => { cleanup(); resolvePromise(true) }
  const cleanup = () => {
    clearTimeout(timer)
    child.off('close', exited)
    child.off('error', exited)
  }
  child.once('close', exited)
})

const terminateChild = async (child, label, shutdown) => {
  if (!child) return { exited: true, forced: false, preExited: false, shutdownFailed: false }
  const state = childStates.get(child)
  if (state?.closed) return { exited: true, forced: false, preExited: true, shutdownFailed: state.spawnFailed }
  const preExited = child.exitCode !== null
  let shutdownFailed = false
  if (!preExited) {
    try { await shutdown() } catch { shutdownFailed = true }
  }
  if (await waitForExit(child, 5_000)) return { exited: true, forced: false, preExited, shutdownFailed }
  try { child.kill('SIGTERM') } catch { shutdownFailed = true }
  if (await waitForExit(child, 5_000)) return { exited: true, forced: true, preExited: false, shutdownFailed }
  try { child.kill('SIGKILL') } catch { shutdownFailed = true }
  const exited = await waitForExit(child, 5_000)
  return { exited, forced: true, preExited: false, shutdownFailed: shutdownFailed || !exited, label }
}

const runProfile = async (profile) => {
  const profileRoot = join(temporaryRoot, profile)
  const readyFile = join(profileRoot, 'ready')
  const browserRoot = join(profileRoot, 'chromium')
  const hostEnvironment = childEnvironment({
    GO_ADMIN_IAM_E2E_SERVE: '1',
    GO_ADMIN_IAM_E2E_PROFILE: profile,
    GO_ADMIN_IAM_E2E_READY_FILE: readyFile,
    GO_ADMIN_IAM_E2E_STATIC_DIR: staticRoot,
  }, profile === 'postgres')
  const host = spawnTracked('go', ['test', './test/iam/session', '-run', '^TestIAMBrowserHarnessServer$', '-count=1', '-v'], {
    cwd: backendRoot, env: hostEnvironment, stdio: ['ignore', 'pipe', 'pipe'], drainStdout: true, drainStderr: true,
  })
  let browser
  let baseURL
  let socket
  let cdp
  let completed = false
  try {
    baseURL = await waitForFile(readyFile, host)
    browser = spawnTracked(chromium, [
      '--headless=new', '--disable-gpu', '--ignore-certificate-errors', '--no-first-run',
      '--no-default-browser-check', '--remote-debugging-port=0', `--user-data-dir=${browserRoot}`, baseURL,
    ], { env: childEnvironment(), stdio: ['ignore', 'ignore', 'pipe'] })
    const devToolsURL = await waitForDevTools(browser)
    socket = await connectWebSocket(devToolsURL)
    cdp = new CDPClient(socket)
    const targetID = await waitForTarget(cdp, baseURL)
    const { sessionId } = await cdp.send('Target.attachToTarget', { targetId: targetID, flatten: true })
    await cdp.send('Runtime.enable', {}, sessionId)
    await cdp.send('Network.enable', {}, sessionId)
    for (let attempt = 0; attempt < 300; attempt += 1) {
      if (await evaluate(cdp, sessionId, 'Boolean(globalThis.__iamE2E)')) break
      if (attempt === 299) fail('browser driver did not initialize')
      await delay(100)
    }

    assert(await evaluate(cdp, sessionId, 'globalThis.__iamE2E.loginAndCheckState()') === true, 'login scenario failed')
    const initialCookie = await currentCookie(cdp, sessionId)
    assert(initialCookie.secure === true, 'session cookie is not Secure')
    assert(initialCookie.httpOnly === true, 'session cookie is not HttpOnly')
    assert(initialCookie.sameSite === 'Strict', 'session cookie is not SameSite=Strict')
    assert(initialCookie.path === '/', 'session cookie path is not host-wide')
    assert(!initialCookie.domain.startsWith('.'), 'session cookie set a Domain attribute')
    const readableCookies = await evaluate(cdp, sessionId, 'document.cookie')
    assert(!String(readableCookies).includes('__Host-go-admin-session'), 'session cookie is readable by document.cookie')

    assert(await evaluate(cdp, sessionId, 'globalThis.__iamE2E.verifyCSRF()') === true, 'CSRF scenarios failed')

    const { targetId: secondTargetID } = await cdp.send('Target.createTarget', { url: baseURL })
    const { sessionId: secondSessionID } = await cdp.send('Target.attachToTarget', { targetId: secondTargetID, flatten: true })
    await cdp.send('Runtime.enable', {}, secondSessionID)
    await cdp.send('Network.enable', {}, secondSessionID)
    for (let attempt = 0; attempt < 300; attempt += 1) {
      if (await evaluate(cdp, secondSessionID, 'Boolean(globalThis.__iamE2E)')) break
      if (attempt === 299) fail('second browser tab did not initialize')
      await delay(100)
    }
    assert(await evaluate(cdp, secondSessionID, 'globalThis.__iamE2E.restoreSharedSession()') === 'authenticated', 'second tab did not restore the shared session')
    const beforeCrossTabRenewal = await currentCookie(cdp, sessionId)
    assert(await evaluate(cdp, sessionId, 'globalThis.__iamE2E.renew()') === true, 'cross-tab renewal failed')
    const afterCrossTabRenewal = await currentCookie(cdp, secondSessionID)
    assert(beforeCrossTabRenewal.value === afterCrossTabRenewal.value, 'cross-tab renewal replaced the stable browser cookie')
    assert(await evaluate(cdp, secondSessionID, 'globalThis.__iamE2E.updateFromSecondTab()') === true, 'second tab lost stable CSRF after renewal')
    assert(await evaluate(cdp, sessionId, 'globalThis.__iamE2E.logoutSharedSession()') === true, 'shared logout failed')
    assert(await evaluate(cdp, secondSessionID, 'globalThis.__iamE2E.restoreSharedSession()') === 'unauthenticated', 'second tab survived shared revocation')
    await cdp.send('Target.closeTarget', { targetId: secondTargetID })
    assert(await evaluate(cdp, sessionId, 'globalThis.__iamE2E.loginAndCheckState()') === true, 'cross-tab fixture could not restore the primary login')

    const beforeRenewal = await currentCookie(cdp, sessionId)
    assert(await evaluate(cdp, sessionId, 'globalThis.__iamE2E.renew()') === true, 'renewal scenario failed')
    const afterRenewal = await currentCookie(cdp, sessionId)
    assert(beforeRenewal.value === afterRenewal.value, 'renewal replaced the stable opaque cookie')
    assert(await evaluate(cdp, sessionId, 'globalThis.__iamE2E.updateProfile()') === true, 'profile scenario failed')
    assert(await evaluate(cdp, sessionId, 'globalThis.__iamE2E.verifyLogoutRetry()') === true, 'logout retry scenario failed')
    assert(await evaluate(cdp, sessionId, 'globalThis.__iamE2E.verifyIdleTimeout()') === true, 'idle timeout scenario failed')
    assert(await evaluate(cdp, sessionId, 'globalThis.__iamE2E.verifyAbsoluteTimeout()') === true, 'absolute timeout scenario failed')
    assert(await evaluate(cdp, sessionId, 'globalThis.__iamE2E.verifyPasswordChange()') === true, 'password scenario failed')
    completed = true
  } finally {
    const hostResult = await terminateChild(host, 'HTTPS test host', () => shutdownHost(baseURL))
    const browserResult = await terminateChild(browser, 'Chromium', async () => {
      if (cdp) await cdp.send('Browser.close')
      else socket?.close()
    })
    try { socket?.close() } catch { browserResult.shutdownFailed = true }
    assert(hostResult.exited && browserResult.exited, 'one or more test children could not be reclaimed')
    if (completed) {
      assert(!hostResult.preExited && !hostResult.forced && !hostResult.shutdownFailed && host.exitCode === 0, 'HTTPS test host did not shut down cleanly')
      assert(!browserResult.preExited && !browserResult.forced && !browserResult.shutdownFailed, 'Chromium did not shut down cleanly')
    }
  }
}

try {
  runChecked('pnpm', ['exec', 'vite', 'build', '--config', join(testRoot, 'vite.config.ts')], {
    cwd: uiRoot,
    env: childEnvironment({ GO_ADMIN_IAM_E2E_OUT_DIR: staticRoot }),
    failure: 'browser fixture build failed',
  })
  runChecked('go', ['test', './test/iam/session', '-run', '^TestIAMBrowserHarnessServer$', '-count=1'], {
    cwd: backendRoot,
    env: childEnvironment({ GO_ADMIN_IAM_E2E_SERVE: '0' }),
    quiet: true,
    failure: 'HTTPS test host compile self-check failed',
  })

  const missing = []
  if (!chromium) missing.push('GO_ADMIN_TEST_CHROMIUM_EXECUTABLE')
  if (!postgresDSN) missing.push(postgresEnvironmentKey)
  if (missing.length) {
    if (requireE2E) fail(`required environment is missing: ${missing.join(', ')}`)
    console.log(`IAM_SESSION_E2E_SKIP missing environment: ${missing.join(', ')}`)
  } else {
    await runProfile('sqlite')
    await runProfile('postgres')
    console.log('IAM_SESSION_E2E_PASS profiles=sqlite,postgres')
  }
} finally {
  if (activeChildren.size === 0) rmSync(temporaryRoot, { recursive: true, force: true })
}
