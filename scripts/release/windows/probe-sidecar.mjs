// 一次性 Windows runner 中诊断 sidecar 的最小操作系统环境，不读取真实用户数据。
import { spawn } from 'node:child_process'
import { randomBytes } from 'node:crypto'
import { mkdir, mkdtemp, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

if (process.platform !== 'win32' || process.env.CI !== 'true' || process.env.GITHUB_ACTIONS !== 'true' || process.argv.length !== 3) {
  throw new Error('sidecar environment probe requires an ephemeral Windows runner')
}
for (const [name, environment] of [['empty', {}], ['system-root', { SystemRoot: process.env.SystemRoot }]]) {
  const root = await mkdtemp(join(tmpdir(), 'go-admin-sidecar-probe-'))
  const data = join(root, 'data')
  const logs = join(root, 'logs')
  await mkdir(data)
  await mkdir(logs)
  const child = spawn(process.argv[2], [], { env: environment, stdio: ['pipe', 'pipe', 'pipe'], windowsHide: true })
  let output = ''
  let diagnostic = ''
  let port
  child.stdout.on('data', value => {
    output = `${output}${value}`.slice(-4096)
    try { const state = JSON.parse(output.trim()); if (state.state === 'listening') port = state.port } catch { /* partial output */ }
  })
  child.stderr.on('data', value => { diagnostic = `${diagnostic}${value}`.slice(-2048) })
  child.stdin.on('error', () => {})
  const exited = new Promise(resolve => child.once('close', resolve))
  const nonce = randomBytes(32).toString('base64url')
  child.stdin.write(`${JSON.stringify({
    dataDirectory: `\\\\?\\${data}`, logDirectory: `\\\\?\\${logs}`, loopbackPort: 0,
    readinessNonce: nonce, controlToken: randomBytes(32).toString('base64url')
  })}\n`)
  let ready = false
  const end = Date.now() + 15_000
  while (child.exitCode === null && Date.now() < end) {
    if (port) {
      const response = await fetch(`http://127.0.0.1:${port}/__desktop/ready`, { headers: { 'X-Go-Admin-Desktop-Nonce': nonce }, signal: AbortSignal.timeout(3000) })
      ready = response.status === 200
      break
    }
    await new Promise(resolve => setTimeout(resolve, 100))
  }
  child.stdin.end()
  const killer = setTimeout(() => child.kill(), 6000)
  await exited
  clearTimeout(killer)
  console.log(JSON.stringify({ probe: name, ready, exitCode: child.exitCode, diagnostic }))
  await rm(root, { recursive: true, force: true })
}
