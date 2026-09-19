import { spawn } from 'node:child_process'
import { basename } from 'node:path'

const maxOutput = 16 * 1024
const sidecarExecutable = /^go-admin-sidecar(?:\.exe|-(?:aarch64-apple-darwin|x86_64-apple-darwin|x86_64-pc-windows-msvc\.exe))?$/

export const execute = (command, args, {
  allowedExitCodes = [0], cwd, env = process.env, input, timeout = 120_000
} = {}) => new Promise((resolveRun, rejectRun) => {
  const child = spawn(command, args, { cwd, env, stdio: ['pipe', 'pipe', 'pipe'] })
  const chunks = []
  let size = 0
  let settled = false
  let pendingError
  let reapTimer
  const timer = setTimeout(() => {
    killAndReap(new Error('desktop native command timed out'))
  }, timeout)
  const finish = error => {
    if (settled) return
    settled = true
    clearTimeout(timer)
    clearTimeout(reapTimer)
    if (error) rejectRun(error)
    else resolveRun(Buffer.concat(chunks).toString('utf8'))
  }
  const killAndReap = error => {
    if (pendingError || settled) return
    pendingError = error
    child.kill('SIGKILL')
    reapTimer = setTimeout(() => finish(new Error('desktop native command cleanup failed')), 5_000)
  }
  const collect = chunk => {
    if (pendingError) return
    size += chunk.length
    if (size > maxOutput) {
      killAndReap(new Error('desktop native command output exceeded the limit'))
      return
    }
    chunks.push(chunk)
  }
  child.stdout.on('data', collect)
  child.stderr.on('data', collect)
  child.once('error', () => finish(new Error('desktop native command unavailable')))
  child.once('close', (code, signal) => finish(
    pendingError ?? (signal === null && code !== null && allowedExitCodes.includes(code)
      ? undefined
      : new Error('desktop native command failed'))
  ))
  if (input !== undefined) child.stdin.end(input)
  else child.stdin.end()
})

export const parseSidecarProcesses = output => new Set(output.split('\n').flatMap(line => {
  const match = line.match(/^\s*(\d+)\s+(\S+)(?:\s|$)/)
  if (!match || !sidecarExecutable.test(basename(match[2]))) return []
  const pid = Number.parseInt(match[1], 10)
  return Number.isSafeInteger(pid) && pid > 0 ? [pid] : []
}))

export const sidecarProcesses = async (run = execute) => {
  const output = await run('/usr/bin/pgrep', ['-lf', 'go-admin-sidecar'], { allowedExitCodes: [0, 1] })
  return parseSidecarProcesses(output)
}

const waitForSidecarsToExit = async (pids, query, pause, timeout = 5_000) => {
  const deadline = Date.now() + timeout
  let remaining = new Set(pids)
  while (remaining.size !== 0) {
    const current = await query()
    remaining = new Set([...remaining].filter(pid => current.has(pid)))
    if (remaining.size === 0 || Date.now() >= deadline) break
    await pause(50)
  }
  return remaining
}

/** Reaps only exact approved sidecar processes absent from the pre-run baseline. */
export const reapNewSidecars = async (baseline, {
  query = sidecarProcesses,
  signal = process.kill,
  pause = milliseconds => new Promise(resolve => setTimeout(resolve, milliseconds)),
  timeout = 5_000
} = {}) => {
  const current = await query()
  const leaked = new Set([...current].filter(pid => !baseline.has(pid)))
  const send = async (pid, name) => {
    if (!(await query()).has(pid)) return
    try {
      signal(pid, name)
    } catch (error) {
      if (error?.code !== 'ESRCH') throw error
    }
  }
  for (const pid of leaked) await send(pid, 'SIGTERM')
  let remaining = await waitForSidecarsToExit(leaked, query, pause, timeout)
  for (const pid of remaining) await send(pid, 'SIGKILL')
  remaining = await waitForSidecarsToExit(remaining, query, pause, timeout)
  if (remaining.size !== 0) throw new Error('desktop sidecar cleanup failed')
  return leaked
}
