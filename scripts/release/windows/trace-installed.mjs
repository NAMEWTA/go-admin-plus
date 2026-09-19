#!/usr/bin/env node

import { spawn } from 'node:child_process'
import { writeFile } from 'node:fs/promises'
import process from 'node:process'

const argumentsByName = new Map()
for (let index = 2; index < process.argv.length; index += 2) argumentsByName.set(process.argv[index], process.argv[index + 1])
const application = argumentsByName.get('--application')
const evidenceFile = argumentsByName.get('--evidence')
if (process.platform !== 'win32' || process.env.CI !== 'true' || process.env.GITHUB_ACTIONS !== 'true' ||
    !application || !evidenceFile || process.argv.length !== 6) {
  throw new Error('installed Windows tracer requires an ephemeral CI runner and exact paths')
}

const endpoint = 'http://127.0.0.1:4444'
const delay = milliseconds => new Promise(resolve => setTimeout(resolve, milliseconds))
const driver = spawn('tauri-driver.exe', [], { stdio: ['ignore', 'ignore', 'pipe'], windowsHide: true })
let driverError = ''
driver.stderr.on('data', chunk => { driverError = `${driverError}${chunk}`.slice(-4096) })

const request = async (path, method = 'GET', body) => {
  const response = await fetch(`${endpoint}${path}`, {
    method,
    headers: body === undefined ? undefined : { 'content-type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body)
  })
  const payload = await response.json().catch(() => ({}))
  if (!response.ok || payload.value?.error) {
    const detail = typeof payload.value?.message === 'string' ? payload.value.message :
      typeof payload.message === 'string' ? payload.message : `HTTP ${response.status}`
    throw new Error(`WebDriver command failed: ${detail.slice(0, 240)}`)
  }
  return payload.value
}
const poll = async (description, operation, timeout = 90_000) => {
  const end = Date.now() + timeout
  while (Date.now() < end) {
    try { if (await operation()) return } catch { /* application may still be starting */ }
    await delay(200)
  }
  throw new Error(`${description} timed out`)
}
const waitForDriver = () => poll('tauri-driver readiness', async () => {
  if (driver.exitCode !== null) throw new Error('tauri-driver exited before accepting a session')
  await request('/status')
  return true
}, 30_000)
const createSession = async () => {
  const value = await request('/session', 'POST', {
    capabilities: {
      alwaysMatch: {
        browserName: 'wry',
        'tauri:options': { application, args: ['--no-sandbox', '--disable-gpu'] }
      }
    }
  })
  if (typeof value.sessionId !== 'string' || value.sessionId.length === 0) throw new Error('WebDriver session identifier is invalid')
  return value.sessionId
}
const execute = (session, script, args = []) => request(`/session/${session}/execute/sync`, 'POST', { script, args })
const closeSession = async session => {
  await request(`/session/${session}`, 'DELETE')
  await delay(1000)
}
const loginIfRequired = async session => {
  await poll('login or restored workspace', () => execute(session, `
    return Boolean(document.querySelector('form[aria-label="登录"]') || document.querySelector('nav[aria-label="主导航"]'))
  `))
  const loginVisible = await execute(session, 'return Boolean(document.querySelector(\'form[aria-label="登录"]\'))')
  if (!loginVisible) return false
  await execute(session, `
    const form = document.querySelector('form[aria-label="登录"]')
    const username = form?.querySelector('input[autocomplete="username"]')
    const password = form?.querySelector('input[autocomplete="current-password"]')
    if (!form || !username || !password) return false
    username.value = arguments[0]
    password.value = arguments[1]
    username.dispatchEvent(new Event('input', { bubbles: true }))
    password.dispatchEvent(new Event('input', { bubbles: true }))
    form.requestSubmit()
    return true
  `, ['admin', 'administrator password'])
  await poll('authenticated workspace', () => execute(session, 'return Boolean(document.querySelector(\'nav[aria-label="主导航"]\'))'))
  return true
}
const openRoles = async session => {
  await poll('角色管理导航', () => execute(session, `
    const button = [...document.querySelectorAll('nav[aria-label="主导航"] button')].find(value => value.textContent?.trim() === '角色管理')
    if (!button) return false
    button.click()
    return true
  `))
  await poll('角色列表', () => execute(session, "return Boolean(document.querySelector('#roles-heading'))"))
}

let activeSession
try {
  await waitForDriver()
  const roleKey = `win-${Date.now()}`
  activeSession = await createSession()
  if (!await loginIfRequired(activeSession)) throw new Error('first installed launch unexpectedly restored a prior session')
  await openRoles(activeSession)
  await execute(activeSession, `document.querySelector('[data-testid="open-create-role"]')?.click()`)
  await poll('新增角色表单', () => execute(activeSession, `return Boolean(document.querySelector('[data-testid="create-role"]'))`))
  const submitted = await execute(activeSession, `
    const form = document.querySelector('[data-testid="create-role"]')
    const values = { key: arguments[0], name: 'Windows 安装验收', dataScope: 'self' }
    if (!form) return false
    for (const [name, value] of Object.entries(values)) {
      const control = form.querySelector('[name="' + name + '"]')
      if (!control) return false
      control.value = value
      control.dispatchEvent(new Event(control.tagName === 'SELECT' ? 'change' : 'input', { bubbles: true }))
    }
    form.requestSubmit()
    return true
  `, [roleKey])
  if (!submitted) throw new Error('installed role form is incomplete')
  await poll('created role', () => execute(activeSession, 'return document.querySelector(\'tbody\')?.textContent?.includes(arguments[0]) === true', [roleKey]))
  await closeSession(activeSession)
  activeSession = undefined

  activeSession = await createSession()
  await loginIfRequired(activeSession)
  await openRoles(activeSession)
  await poll('persisted role after restart', () => execute(activeSession, 'return document.querySelector(\'tbody\')?.textContent?.includes(arguments[0]) === true', [roleKey]))
  const deleted = await execute(activeSession, `
    window.confirm = () => true
    const row = [...document.querySelectorAll('tbody tr')].find(value => value.textContent?.includes(arguments[0]))
    const button = row?.querySelector('button[data-action="delete"]')
    if (!button) return false
    button.click()
    return true
  `, [roleKey])
  if (!deleted) throw new Error('persisted role could not be deleted')
  await poll('deleted role', () => execute(activeSession, 'return document.querySelector(\'tbody\')?.textContent?.includes(arguments[0]) !== true', [roleKey]))
  await closeSession(activeSession)
  activeSession = undefined

  await writeFile(evidenceFile, `${JSON.stringify({
    schemaVersion: 1, driver: 'tauri-driver', firstLaunchLogin: 'passed', create: 'passed',
    restart: 'passed', persistence: 'passed', delete: 'passed'
  }, null, 2)}\n`, { encoding: 'utf8', flag: 'wx' })
  process.stdout.write('GO_ADMIN_WINDOWS_INSTALLED_TRACER_PASS\n')
} catch (error) {
  if (activeSession) await closeSession(activeSession).catch(() => {})
  if (driverError) process.stderr.write('tauri-driver diagnostics were captured\n')
  throw error
} finally {
  driver.kill()
}
