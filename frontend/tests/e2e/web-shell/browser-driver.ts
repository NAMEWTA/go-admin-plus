import { createApp } from 'vue'

import { createBrowserSessionFetch, createWebRuntime } from '@go-admin-plus/adapter-browser'
import { createProductRouter } from '@go-admin-plus/app-shell/product'

import App from '../../../apps/admin-web/src/App.vue'
import '../../../apps/admin-web/src/styles.css'
import '@go-admin-plus/ui/admin-theme.css'

const result = document.querySelector<HTMLElement>('#result')
if (!result) throw new Error('result marker is missing')

const assert: (condition: unknown, message: string) => asserts condition = (condition, message) => {
  if (!condition) throw new Error(message)
}
const wait = async (condition: () => boolean, message: string, timeout = 15_000) => {
  const deadline = Date.now() + timeout
  while (Date.now() < deadline) {
    if (condition()) return
    await new Promise(resolve => setTimeout(resolve, 25))
  }
  throw new Error(message)
}
const input = (label: string, value: string) => {
  const target = document.querySelector<HTMLInputElement>(`input[aria-label="${label}"]`)
  assert(target, `missing ${label} input`)
  target.value = value
  target.dispatchEvent(new Event('input', { bubbles: true }))
}
const post = async (path: string) => {
  const response = await fetch(path, { method: 'POST' })
  assert(response.ok, 'test control failed')
}
const shellState = () => document.querySelector<HTMLElement>('[data-shell-state]')?.dataset.shellState
const noHorizontalOverflow = () => document.documentElement.scrollWidth <= window.innerWidth
const darkThemeSelected = () => document.documentElement.dataset.theme === 'dark' &&
  document.querySelector('button[aria-label="当前使用深色主题"]') !== null

let consoleErrors = 0
const originalConsoleError = console.error
console.error = (...values: unknown[]) => {
  consoleErrors += 1
  originalConsoleError(...values)
}
window.addEventListener('error', () => { consoleErrors += 1 })
window.addEventListener('unhandledrejection', () => { consoleErrors += 1 })

const runtimeTrace: string[] = []
const observedFetch: typeof fetch = async (input, init) => {
  const response = await fetch(input, init)
  const path = typeof input === 'string' ? input : input instanceof URL ? input.pathname : new URL(input.url).pathname
  if (path === '/api/runtime/identity' || path === '/api/runtime/navigation') {
    let contract = 'invalid'
    try {
      const body = await response.clone().json() as Record<string, unknown> | unknown[]
      if (path.endsWith('identity')) contract = !Array.isArray(body) && Array.isArray(body.permissions) && body.permissions.includes('iam.roles.read') ? 'roles' : 'without-roles'
      else contract = Array.isArray(body) && body.some(entry => typeof entry === 'object' && entry !== null && (entry as Record<string, unknown>).path === '/iam/roles') ? 'roles' : 'without-roles'
    } catch {
      contract = 'non-json'
    }
    runtimeTrace.push(`${path.endsWith('identity') ? 'identity' : 'navigation'}-${response.status}-${contract}`)
  }
  return response
}
const fetcher = createBrowserSessionFetch(observedFetch)
const runtime = createWebRuntime(fetcher)
const router = createProductRouter('web', runtime)
const routeTrace: string[] = []
router.afterEach((to, from) => { routeTrace.push(`${String(from.name ?? 'start')}-${String(to.name ?? 'missing')}`) })
createApp(App, { fetcher, runtime }).use(router).mount('#app')

const firstLoad = async () => {
  await wait(() => shellState() === 'login', 'login view did not render')
  input('账号', 'admin')
  input('密码', 'web browser administrator password')
  const form = document.querySelector<HTMLFormElement>('form[aria-label="登录"]')
  assert(form, 'login form is missing')
  form.requestSubmit()
  await wait(() => shellState() === 'workspace' && location.pathname === '/iam/users', 'authenticated workspace did not render')
  const darkTheme = document.querySelector<HTMLButtonElement>('button[aria-label="使用深色主题"]')
  assert(darkTheme, 'dark theme control did not render')
  darkTheme.click()
  await wait(darkThemeSelected, 'dark theme did not apply')
  await router.push('/iam/roles')
  await wait(() => location.pathname === '/iam/roles' && shellState() === 'workspace' && document.querySelector('section[aria-labelledby="roles-heading"]') !== null, 'role route did not render')
  await new Promise(resolve => setTimeout(resolve, 100))
  sessionStorage.setItem('web-shell-e2e-stage', 'deep-link')
  location.reload()
}

const deepLinkAndHistory = async () => {
  try {
    await wait(() => shellState() === 'workspace' && location.pathname === '/iam/roles', 'deep link was not restored')
  } catch {
    const identity = await runtime.loadIdentity().catch(() => ({ kind: 'unavailable' as const }))
    const navigation = await runtime.loadNavigation().catch(() => [])
    const permission = identity.kind === 'authenticated' && identity.permissions.includes('iam.roles.read')
    const menu = navigation.some(entry => entry.path === '/iam/roles')
    throw new Error(`deep link state ${shellState() ?? 'missing'} path ${location.pathname} permission ${permission} menu ${menu} route ${routeTrace.join('-')} trace ${runtimeTrace.join('-')}`)
  }
  await wait(darkThemeSelected, 'dark theme was not restored after reload')
  assert(document.title === '角色管理 - Go Admin Plus', 'deep link title is incorrect')
  assert(document.querySelector('.product-shell__breadcrumb')?.textContent?.includes('角色管理'), 'deep link breadcrumb is incorrect')
  assert(matchMedia('(prefers-reduced-motion: reduce)').matches, 'reduced-motion media was not applied')
  assert(noHorizontalOverflow(), 'shell overflows the active viewport')

  await router.push('/iam/users')
  await wait(() => location.pathname === '/iam/users', 'forward route did not render')
  history.back()
  await wait(() => location.pathname === '/iam/roles', 'history back did not restore route truth')
  history.forward()
  await wait(() => location.pathname === '/iam/users', 'history forward did not restore route truth')

  await router.push('/missing-browser-route')
  await wait(() => shellState() === 'not-found' && document.title.startsWith('页面不存在'), 'not-found route did not render')

  await post('/__test/revoke-permission?code=iam.roles.read')
  await router.push('/iam/roles')
  await wait(() => shellState() === 'forbidden' && location.pathname === '/forbidden', 'revoked route did not fail closed')

  await router.push('/iam/users')
  await wait(() => shellState() === 'workspace' && location.pathname === '/iam/users', 'allowed route did not recover from forbidden state')
  await wait(() => document.querySelector('[data-testid="user-search"]') !== null, 'user search was not available for revocation check')
  await post('/__test/revoke-sessions')
  const search = document.querySelector<HTMLFormElement>('[data-testid="user-search"]')
  assert(search, 'user search was not available for revocation check')
  search.requestSubmit()
  try {
    await wait(() => shellState() === 'login' && location.pathname === '/login', 'revoked session did not return to login')
  } catch {
    const relogin = document.body.textContent?.includes('重新登录') === true || document.body.textContent?.includes('登录状态已失效') === true
    throw new Error(`revoked session state ${shellState() ?? 'missing'} path ${location.pathname} relogin ${relogin}`)
  }
  assert(noHorizontalOverflow(), 'login overflows the active viewport')
  assert(consoleErrors === 0, 'browser console reported an error')
  sessionStorage.removeItem('web-shell-e2e-stage')
  result.textContent = 'WEB_SHELL_E2E_PASS'
}

const scenario = sessionStorage.getItem('web-shell-e2e-stage') === 'deep-link' ? deepLinkAndHistory : firstLoad
scenario().catch(async error => {
  const category = error instanceof Error && /^[a-zA-Z0-9 /:-]{1,420}$/.test(error.message) ? error.message : 'browser assertion failed'
  result.textContent = `WEB_SHELL_E2E_FAIL|${category}`
  await post('/__test/shutdown').catch(() => undefined)
})
