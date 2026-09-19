// 一次性 Windows runner 中诊断 sidecar 的最小操作系统环境，不读取真实用户数据。
import { spawn, spawnSync } from 'node:child_process'
import { randomBytes } from 'node:crypto'
import { mkdir, mkdtemp, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

if (process.platform !== 'win32' || process.env.CI !== 'true' || process.env.GITHUB_ACTIONS !== 'true' || process.argv.length !== 3) {
  throw new Error('sidecar environment probe requires an ephemeral Windows runner')
}
// Node/libuv 会自动补充 SystemRoot；用 Rust 启动器复现生产环境的 env_clear。
const launcherRoot = await mkdtemp(join(tmpdir(), 'go-admin-environment-launcher-'))
const source = join(launcherRoot, 'launcher.rs')
const executable = join(launcherRoot, 'launcher.exe')
await writeFile(source, `use std::{env, process::{Command, Stdio}};
use std::os::windows::process::CommandExt;
fn main() {
    let args: Vec<String> = env::args().collect();
    let mut child = Command::new(&args[1]);
    child.env_clear().stdin(Stdio::inherit()).stdout(Stdio::inherit()).stderr(Stdio::inherit()).creation_flags(0x08000000);
    if args[2] == "system-root" { child.env("SystemRoot", env::var_os("SystemRoot").expect("SystemRoot missing")); }
    let status = child.status().expect("sidecar spawn failed");
    std::process::exit(status.code().unwrap_or(1));
}`)
const build = spawnSync('rustc', ['--edition=2024', source, '-o', executable], { stdio: 'inherit' })
if (build.status !== 0) throw new Error('environment probe launcher could not be built')
for (const name of ['empty', 'system-root']) {
  const root = await mkdtemp(join(tmpdir(), 'go-admin-sidecar-probe-'))
  const data = join(root, 'data')
  const logs = join(root, 'logs')
  await mkdir(data)
  await mkdir(logs)
  const child = spawn(executable, [process.argv[2], name], { stdio: ['pipe', 'pipe', 'pipe'], windowsHide: true })
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
await rm(launcherRoot, { recursive: true, force: true })
