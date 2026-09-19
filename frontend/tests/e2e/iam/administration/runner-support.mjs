export const delay = (milliseconds) => new Promise((resolve) => setTimeout(resolve, milliseconds))

export const withTimeout = (promise, timeout, label, onTimeout = () => {}) => new Promise((resolve, reject) => {
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
    resolve(value)
  }, (error) => {
    if (settled) return
    settled = true
    clearTimeout(timer)
    reject(error)
  })
})

export class CDPClient {
  constructor(socket, commandTimeout = 10_000) {
    this.socket = socket
    this.commandTimeout = commandTimeout
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
    return new Promise((resolve, reject) => {
      const id = this.nextID++
      const timer = setTimeout(() => {
        this.pending.delete(id)
        reject(new Error('CDP command timed out'))
      }, this.commandTimeout)
      this.pending.set(id, { resolve, reject, timer })
      try { this.socket.send(JSON.stringify({ id, method, params, ...(sessionId ? { sessionId } : {}) })) } catch {
        clearTimeout(timer)
        this.pending.delete(id)
        reject(new Error('CDP command could not be sent'))
      }
    })
  }
}

export const activeChildren = new Set()
const childStates = new WeakMap()

export const spawnTracked = (spawn, command, args, options) => {
  const { drainStdout, drainStderr, ...spawnOptions } = options
  const child = spawn(command, args, spawnOptions)
  const state = { closed: false, spawned: false, spawnFailed: false }
  childStates.set(child, state); activeChildren.add(child)
  child.once('spawn', () => { state.spawned = true })
  child.on('error', () => { if (!state.spawned) state.spawnFailed = true; state.closed = true; activeChildren.delete(child) })
  child.once('exit', () => { activeChildren.delete(child) })
  child.once('close', () => { state.closed = true; activeChildren.delete(child) })
  if (drainStdout && child.stdout) child.stdout.resume()
  if (drainStderr && child.stderr) child.stderr.resume()
  return child
}

export const assertChildHealthy = (child, label) => {
  const state = childStates.get(child)
  if (state?.spawnFailed) throw new Error(`${label} could not start`)
  if (state?.closed || child.exitCode !== null || child.signalCode !== null) throw new Error(`${label} exited unexpectedly`)
}

export const waitForExit = (child, maximum) => new Promise((resolve) => {
  const state = childStates.get(child)
  if (state?.closed || child.exitCode !== null || child.signalCode !== null) { resolve(true); return }
  const timer = setTimeout(() => { cleanup(); resolve(false) }, maximum)
  const exited = () => { cleanup(); resolve(true) }
  const cleanup = () => { clearTimeout(timer); child.off('exit', exited); child.off('close', exited); child.off('error', exited) }
  child.once('exit', exited)
  child.once('close', exited)
  child.once('error', exited)
})

export const terminateChild = async (child, shutdown, timeout = 5_000) => {
  if (!child) return { exited: true, forced: false, preExited: false, shutdownFailed: false }
  const state = childStates.get(child)
  if (state?.closed || child.exitCode !== null || child.signalCode !== null) return { exited: true, forced: false, preExited: true, shutdownFailed: Boolean(state?.spawnFailed) }
  let shutdownFailed = false
  if (shutdown) {
    try { await withTimeout(Promise.resolve(shutdown()), timeout, 'child shutdown') } catch { shutdownFailed = true }
    if (await waitForExit(child, timeout)) return { exited: true, forced: false, preExited: false, shutdownFailed }
  }
  try { child.kill('SIGTERM') } catch { shutdownFailed = true }
  if (await waitForExit(child, timeout)) return { exited: true, forced: true, preExited: false, shutdownFailed }
  try { child.kill('SIGKILL') } catch { shutdownFailed = true }
  const exited = await waitForExit(child, timeout)
  return { exited, forced: true, preExited: false, shutdownFailed: shutdownFailed || !exited }
}
