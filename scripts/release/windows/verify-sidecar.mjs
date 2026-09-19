// 使用已安装的真实 sidecar 验证最小系统环境、扩展路径、就绪握手和父进程管道关闭。
import { spawn, spawnSync } from 'node:child_process'
import { randomBytes } from 'node:crypto'
import { mkdir, mkdtemp, realpath, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

if (process.platform !== 'win32' || process.env.CI !== 'true' || process.env.GITHUB_ACTIONS !== 'true' || process.argv.length !== 3) {
  throw new Error('sidecar startup verification requires an ephemeral Windows runner')
}
// Node/libuv 会自动补充系统环境；Rust 启动器只传递生产宿主允许的 SystemRoot。
const launcherRoot = await mkdtemp(join(tmpdir(), 'go-admin-environment-launcher-'))
const source = join(launcherRoot, 'launcher.rs')
const executable = join(launcherRoot, 'launcher.exe')
await writeFile(source, `use std::{env, process::{Command, Stdio}};
use std::os::windows::process::CommandExt;
fn main() {
    let args: Vec<String> = env::args().collect();
    let mut child = Command::new(&args[1]);
    child.env_clear().env("SystemRoot", env::var_os("SystemRoot").expect("SystemRoot missing"));
    child.stdin(Stdio::inherit()).stdout(Stdio::inherit()).stderr(Stdio::inherit()).creation_flags(0x08000000);
    let status = child.status().expect("sidecar spawn failed");
    std::process::exit(status.code().unwrap_or(1));
}`)
const build = spawnSync('rustc', ['--edition=2024', source, '-o', executable], { stdio: 'inherit' })
if (build.status !== 0) throw new Error('sidecar verification launcher could not be built')
let child
let exited
let root
try {
  root = await realpath(await mkdtemp(join(tmpdir(), 'go-admin-sidecar-check-')))
  const data = join(root, 'data')
  const logs = join(root, 'logs')
  await mkdir(data)
  await mkdir(logs)
  child = spawn(executable, [process.argv[2]], { stdio: ['pipe', 'pipe', 'pipe'], windowsHide: true })
  let spawnError
  child.on('error', error => { spawnError = error })
  let output = ''
  let diagnostic = ''
  let port
  child.stdout.on('data', value => {
    output = `${output}${value}`.slice(-4096)
    try { const state = JSON.parse(output.trim()); if (state.state === 'listening') port = state.port } catch { /* partial output */ }
  })
  child.stderr.on('data', value => { diagnostic = `${diagnostic}${value}`.slice(-2048) })
  child.stdin.on('error', () => {})
  exited = new Promise(resolve => child.once('close', resolve))
  const nonce = randomBytes(32).toString('base64url')
  child.stdin.write(`${JSON.stringify({
    dataDirectory: `\\\\?\\${data}`, logDirectory: `\\\\?\\${logs}`, loopbackPort: 0,
    readinessNonce: nonce, controlToken: randomBytes(32).toString('base64url')
  })}\n`)
  let ready = false
  const end = Date.now() + 15_000
  while (child.exitCode === null && Date.now() < end) {
    if (spawnError) throw spawnError
    if (port) {
      const response = await fetch(`http://127.0.0.1:${port}/__desktop/ready`, { headers: { 'X-Go-Admin-Desktop-Nonce': nonce }, signal: AbortSignal.timeout(3000) })
      ready = response.status === 200
      break
    }
    await new Promise(resolve => setTimeout(resolve, 100))
  }
  child.stdin.end()
  await stopChild()
  if (!ready || child.exitCode !== 0) throw new Error(`Installed sidecar startup/shutdown failed: exit=${child.exitCode}, ready=${ready}, ${diagnostic}`)
  console.log('GO_ADMIN_WINDOWS_SIDECAR_STARTUP_PASS')
} finally {
  if (child) {
    child.stdin.end()
    await stopChild()
  }
  if (root) await rm(root, { recursive: true, force: true })
  await rm(launcherRoot, { recursive: true, force: true })
}

async function stopChild() {
  // 只终止本次启动器的进程树，避免超时后遗留持有数据库锁的 sidecar。
  const killer = setTimeout(() => {
    if (Number.isInteger(child.pid) && child.exitCode === null) {
      spawnSync('taskkill.exe', ['/PID', String(child.pid), '/T', '/F'], { stdio: 'ignore', windowsHide: true, timeout: 5000 })
    }
  }, 6000)
  try { await exited } finally { clearTimeout(killer) }
}
