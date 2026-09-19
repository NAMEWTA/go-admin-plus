import { installRequestDiagnostic } from '../../request-diagnostic'
import { createApp, h, type App, type Component } from 'vue'
import { createProductMemoryHistory, createProductRouter } from '@go-admin-plus/app-shell/product'
import type { AdministrationClient } from '@go-admin-plus/domain-iam/administration'
import { createSessionController } from '@go-admin-plus/domain-iam/session'
import type { PermissionCode } from '@go-admin-plus/platform'
import { createAdministrationController, createWebAdministrationClient, AdministrationPage, type AdministrationController } from '@go-admin-plus/web-domain-iam/administration'
import { createWebSessionClient } from '@go-admin-plus/web-domain-iam/session'
import { administrationMountDiagnostic, safeBrowserDiagnostic } from './diagnostics.mjs'

const requestDiagnostic = installRequestDiagnostic()
const assert: (condition: unknown, message: string) => asserts condition = (condition, message) => { if (!condition) throw new Error(message) }
const renderFailure = (error: unknown) => {
  const diagnostic = `${safeBrowserDiagnostic(error)} [${requestDiagnostic()}]`
  const marker = `IAM_ADMIN_E2E_FAIL|ASSERTION|${diagnostic}`
  document.body.replaceChildren()
  const result = document.createElement('pre')
  result.id = 'result'
  result.textContent = marker
  document.body.append(result)
  console.error(`IAM_ADMIN_E2E_DIAGNOSTIC|ASSERTION|${diagnostic}`)
}
const session = createSessionController(createWebSessionClient(fetch, '/api'))
const control = async (path: string, method = 'GET') => { const response = await fetch(`/__test/${path}`, { method }); assert(response.ok, 'test control failed'); return response }
const waitUntil = async (condition: () => boolean, message: string | (() => string), timeout = 10_000) => {
  const deadline = Date.now() + timeout
  while (Date.now() < deadline) {
    if (condition() && document.querySelector('.administration-page[aria-busy="true"]') === null) return
    await new Promise((resolve) => setTimeout(resolve, 25))
  }
  throw new Error(typeof message === 'function' ? message() : message)
}
const element = <T extends Element>(selector: string) => {
  const result = document.querySelector<T>(selector)
  assert(result, `missing element: ${selector}`)
  return result
}
const row = (key: string) => document.querySelector<HTMLTableRowElement>(`[data-row-key="${key}"]`)
const input = async (selector: string, value: string | boolean) => {
  const target = element<HTMLInputElement | HTMLSelectElement>(selector)
  if (target instanceof HTMLInputElement && target.type === 'checkbox') target.checked = Boolean(value)
  else target.value = String(value)
  target.dispatchEvent(new Event(target instanceof HTMLSelectElement || target.type === 'checkbox' ? 'change' : 'input', { bubbles: true }))
  await Promise.resolve()
}
const click = (selector: string) => element<HTMLElement>(selector).click()
const clickRow = (key: string, action: string) => {
  const target = row(key)?.querySelector<HTMLButtonElement>(`[data-action="${action}"]`)
  assert(target, `missing ${action} for ${key}`)
  target.click()
}
const clickRowAndWait = async (key: string, action: string, selector: string) => {
  clickRow(key, action)
  await waitUntil(() => document.querySelector(selector) !== null, `row ${action} view did not render`)
}
const submit = (testID: string) => {
  const form = element<HTMLFormElement>(`[data-testid="${testID}"]`)
  assert(form.checkValidity(), `invalid controls: ${[...form.querySelectorAll<HTMLInputElement>(':invalid')].map(value => value.name).join(',')}`)
  form.requestSubmit()
}
type AdministrationRouter = ReturnType<typeof createProductRouter>
const openView = async (mounted: { router: AdministrationRouter }, name: 'users' | 'roles' | 'menus') => {
  await mounted.router.push(`/iam/${name}`)
  await waitUntil(
    () => document.querySelector(`section[aria-labelledby="${name}-heading"]`) !== null,
    `administration view ${name} did not render`,
  )
}

interface MountedAdministration { app: App; router: AdministrationRouter; controller: AdministrationController; api: ReturnType<typeof createWebAdministrationClient>; confirmations(): number }
type RequestPhase = 'not-started' | 'pending' | 'success' | 'error'
interface MountPhases { manifest: RequestPhase; users: RequestPhase }
interface ManifestProbe {
  permissionCount: number
  hasUsersRead: boolean
  hasManifestRead: boolean
  scope: 'all' | 'self' | 'unknown'
}
const observeRequest = async <T>(phases: MountPhases, key: keyof MountPhases, operation: () => Promise<T>): Promise<T> => {
  phases[key] = 'pending'
  try {
    const result = await operation()
    phases[key] = 'success'
    return result
  } catch (error) {
    phases[key] = 'error'
    throw error
  }
}
const observedClient = (client: AdministrationClient, phases: MountPhases, projection: ManifestProbe): AdministrationClient => ({
  ...client,
  manifest: () => observeRequest(phases, 'manifest', async () => {
    const manifest = await client.manifest()
    projection.permissionCount = manifest.permissionCodes.length
    projection.hasUsersRead = manifest.permissionCodes.includes('iam.users.read')
    projection.hasManifestRead = manifest.permissionCodes.includes('iam.manifest.read')
    projection.scope = manifest.dataScope === 'all' || manifest.dataScope === 'self' ? manifest.dataScope : 'unknown'
    return manifest
  }),
  listUsers: (...arguments_) => observeRequest(phases, 'users', () => client.listUsers(...arguments_)),
})
const mountAdministration = async (expectedUser = 'admin'): Promise<MountedAdministration> => {
  document.body.innerHTML = '<div id="app"></div>'
  const sessionState = session.state()
  assert(sessionState.status === 'authenticated', 'administration mount requires an authenticated session')
  const api = createWebAdministrationClient(fetch, '/api')
  const phases: MountPhases = { manifest: 'not-started', users: 'not-started' }
  const projection: ManifestProbe = { permissionCount: -1, hasUsersRead: false, hasManifestRead: false, scope: 'unknown' }
  let confirmations = 0
  let pageMounted = false
  const controller = createAdministrationController(observedClient(api, phases, projection), async () => { confirmations += 1; return true })
  const router = createProductRouter('web', {
    loadIdentity: async () => {
      const manifest = await api.manifest()
      return {
        kind: 'authenticated',
        subjectId: sessionState.profile.id,
        permissions: manifest.permissionCodes as ReadonlyArray<PermissionCode>,
        dataScope: manifest.dataScope === 'all' ? 'all' : 'self',
      }
    },
    loadNavigation: async () => {
      const manifest = await api.manifest()
      return manifest.menus.map(menu => ({ path: menu.path as `/${string}`, permission: menu.permissionCode as PermissionCode }))
    },
  }, { history: createProductMemoryHistory() })
  const app = createApp({ render: () => h(AdministrationPage as Component, { controller }) })
  app.use(router)
  await router.push('/iam/users')
  await router.isReady()
  app.mount('#app')
  pageMounted = true
  await waitUntil(() => row(expectedUser) !== null, () => {
    const snapshot = controller.users.snapshot()
    return administrationMountDiagnostic({
      failure: controller.failure(),
      canUsersRead: controller.can('iam.users.read'),
      rows: snapshot.rows.length,
      total: snapshot.total,
      loading: snapshot.loading,
      alertText: document.querySelector('[role="alert"]')?.textContent?.trim() ?? null,
      manifest: phases.manifest,
      users: phases.users,
      readyState: document.readyState,
      pageMounted,
      permissionCount: projection.permissionCount,
      hasUsersRead: projection.hasUsersRead,
      hasManifestRead: projection.hasManifestRead,
      scope: projection.scope,
    })
  })
  return { app, router, controller, api, confirmations: () => confirmations }
}

const fillCreateUser = async (username: string, displayName: string) => {
  click('[data-testid="open-create-user"]')
  await waitUntil(() => document.querySelector('[data-testid="create-user"]') !== null, 'create user dialog did not render')
  await input('[data-testid="create-user"] [name="username"]', username)
  await input('[data-testid="create-user"] [name="displayName"]', displayName)
  await input('[data-testid="create-user"] [name="email"]', `${username}@example.test`)
  await input('[data-testid="create-user"] [name="password"]', `${username} browser password`)
  submit('create-user')
  await waitUntil(() => row(username) !== null, `user ${username} was not rendered`)
  await waitUntil(() => document.querySelector('[data-testid="create-user"]') === null, 'create user dialog did not close')
  click('[data-testid="open-create-user"]')
  await waitUntil(() => document.querySelector('[data-testid="create-user"]') !== null, 'create user dialog did not reopen')
  assert(element<HTMLInputElement>('[data-testid="create-user"] [name="password"]').value === '', 'create password was retained')
  click('[data-testid="create-user"] [aria-label="关闭"]')
}

const scenario = async () => {
  await session.login({ username: 'admin', password: 'administrator password' })
  const administratorState = session.state()
  assert(administratorState.status === 'authenticated', 'administrator login failed')
  assert(administratorState.profile.username === 'admin', 'administrator identity mismatch')
  assert(!document.cookie.includes('__Host-go-admin-session'), 'HttpOnly session became script-readable')
  const attributes = await (await control('cookie-attributes')).json() as Record<string, boolean>
  assert(attributes.secure && attributes.httpOnly && attributes.strict, 'host cookie attributes failed')
  let mounted = await mountAdministration()

  await input('[data-testid="user-search"] input', 'admin')
  submit('user-search')
  await waitUntil(() => row('admin') !== null, 'search result missing')
  click('[data-testid="user-search-reset"]')
  await waitUntil(() => element<HTMLInputElement>('[data-testid="user-search"] input').value === '', 'search reset failed')

  await openView(mounted, 'roles')
  click('[data-testid="open-create-role"]')
  await waitUntil(() => document.querySelector('[data-testid="create-role"]') !== null, 'create role dialog did not render')
  await input('[data-testid="create-role"] [name="key"]', 'browser-reader')
  await input('[data-testid="create-role"] [name="name"]', 'Browser Reader')
  await input('[data-testid="create-role"] [name="dataScope"]', 'self')
  submit('create-role')
  await waitUntil(() => row('browser-reader') !== null, 'role create was not rendered')
  await clickRowAndWait('browser-reader', 'edit', '[data-testid="edit-role"]')
  await input('[data-testid="edit-role"] [name="name"]', 'Browser Reader Updated')
  submit('edit-role')
  await waitUntil(() => row('browser-reader')?.textContent?.includes('Browser Reader Updated') === true, () => `role edit was not rendered (failure=${mounted.controller.failure()}, busy=${mounted.controller.busy}, controllerUpdated=${mounted.controller.roles().some(v => v.name === 'Browser Reader Updated')})`)
  await clickRowAndWait('browser-reader', 'edit', '[data-testid="edit-role"]')
  await input('[data-testid="edit-role"] [name="enabled"]', false)
  submit('edit-role')
  await waitUntil(() => row('browser-reader')?.textContent?.includes('停用') === true, 'role disable was not rendered')
  await clickRowAndWait('browser-reader', 'edit', '[data-testid="edit-role"]')
  await input('[data-testid="edit-role"] [name="enabled"]', true)
  submit('edit-role')
  await waitUntil(() => row('browser-reader')?.textContent?.includes('启用') === true, 'role enable was not rendered')

  await openView(mounted, 'menus')
  click('[data-testid="open-create-menu"]')
  await waitUntil(() => document.querySelector('[data-testid="create-menu"]') !== null, 'create menu dialog did not render')
  await input('[data-testid="create-menu"] [name="key"]', 'browser-menu')
  await input('[data-testid="create-menu"] [name="label"]', 'Browser Menu')
  await input('[data-testid="create-menu"] [name="kind"]', 'directory')
  await input('[data-testid="create-menu"] [name="sortOrder"]', '90')
  submit('create-menu')
  await waitUntil(() => row('browser-menu') !== null, 'menu create was not rendered')
  await clickRowAndWait('browser-menu', 'edit', '[data-testid="edit-menu"]')
  await input('[data-testid="edit-menu"] [name="label"]', 'Browser Menu Updated')
  submit('edit-menu')
  await waitUntil(() => row('browser-menu')?.textContent?.includes('Browser Menu Updated') === true, () => `menu edit was not rendered (failure=${mounted.controller.failure()}, busy=${mounted.controller.busy})`)

  const directory = mounted.controller.menus().find(value => value.key === 'browser-menu')!
  await clickRowAndWait('iam-users', 'edit', '[data-testid="edit-menu"]')
  await input('[data-testid="edit-menu"] [name="parentId"]', directory.id)
  submit('edit-menu')
  await waitUntil(() => mounted.controller.menus().find(value => value.key === 'iam-users')?.parentId === directory.id, 'menu tree reparenting did not refresh')

  await openView(mounted, 'roles')
  await clickRowAndWait('browser-reader', 'edit', '[data-testid="assign-role-grants"]')
  await input('[data-testid="assign-role-grants"] [data-permission-code="iam.users.read"]', true)
  await input('[data-testid="assign-role-grants"] [data-permission-code="iam.manifest.read"]', true)
  await input('[data-testid="assign-role-grants"] [data-menu-key="iam-users"]', true)
  await input('[data-testid="assign-role-grants"] [data-menu-key="browser-menu"]', true)
  submit('assign-role-grants')
  await waitUntil(() => mounted.controller.roles().find((value) => value.key === 'browser-reader')?.permissionCodes.includes('iam.manifest.read') === true, 'grants did not refresh')

  await openView(mounted, 'users')
  await fillCreateUser('browser-user', 'Browser User')
  click('[data-testid="open-create-user"]')
  await waitUntil(() => document.querySelector('[data-testid="create-user"]') !== null, 'duplicate user dialog did not render')
  await input('[data-testid="create-user"] [name="username"]', 'browser-user')
  await input('[data-testid="create-user"] [name="displayName"]', 'Duplicate Browser User')
  await input('[data-testid="create-user"] [name="email"]', 'browser-user@example.test')
  await input('[data-testid="create-user"] [name="password"]', 'duplicate browser password')
  submit('create-user')
  await waitUntil(() => document.querySelector('[role="alert"]')?.textContent?.includes('受系统保护') === true, 'conflict page state was not visible')
  click('[data-testid="create-user"] [aria-label="关闭"]')
  await clickRowAndWait('browser-user', 'edit', '[data-testid="edit-user"]')
  await input('[data-testid="edit-user"] [name="displayName"]', 'Browser Updated')
  submit('edit-user')
  await waitUntil(() => row('browser-user')?.textContent?.includes('Browser Updated') === true, 'user edit was not rendered')
  await clickRowAndWait('browser-user', 'edit', '[data-testid="assign-user-roles"]')
  await input('[data-testid="assign-user-roles"] [data-role-key="browser-reader"]', true)
  submit('assign-user-roles')
  await waitUntil(() => mounted.controller.users.snapshot().rows.find((value) => value.username === 'browser-user')?.roleIds.length === 1, 'role assignment did not refresh')
  clickRow('browser-user', 'toggle')
  await waitUntil(() => mounted.controller.users.snapshot().rows.find((value) => value.username === 'browser-user')?.disabled === true && row('browser-user')?.querySelector('[data-action="toggle"]')?.textContent === '启用', 'user disable was not rendered')
  clickRow('browser-user', 'toggle')
  await waitUntil(() => mounted.controller.users.snapshot().rows.find((value) => value.username === 'browser-user')?.disabled === false && row('browser-user')?.querySelector('[data-action="toggle"]')?.textContent === '停用', 'user enable was not rendered')
  await clickRowAndWait('browser-user', 'edit', '[data-testid="reset-user-password"]')
  await input('[data-testid="reset-user-password"] [name="password"]', 'browser replacement password')
  submit('reset-user-password')
  await waitUntil(() => document.querySelector('[data-testid="reset-user-password"]') === null, 'reset password dialog did not close')
  await clickRowAndWait('browser-user', 'edit', '[data-testid="reset-user-password"]')
  assert(element<HTMLInputElement>('[data-testid="reset-user-password"] [name="password"]').value === '', 'reset password was retained')
  click('[aria-labelledby="edit-user-title"] [aria-label="关闭"]')
  await waitUntil(() => document.querySelector('[data-testid="reset-user-password"]') === null, 'reset password dialog did not close after retention check')

  await fillCreateUser('browser-single', 'Browser Single')
  clickRow('browser-single', 'delete')
  await waitUntil(() => document.querySelector('[data-testid="delete-user"]') !== null, 'account deletion dialog did not render')
  click('[data-testid="delete-user"] input[value="purge"]')
  await waitUntil(() => document.querySelector('[data-testid="delete-user"] [name="purgeConfirmed"]') !== null, 'purge confirmation did not render')
  await input('[data-testid="delete-user"] [name="purgeConfirmed"]', true)
  submit('delete-user')
  await waitUntil(
    () => document.querySelector('.deletion-status')?.textContent?.includes('queued') === true,
    () => `account deletion was not queued (failure=${mounted.controller.failure() ?? 'none'}, deletion=${JSON.stringify(mounted.controller.deletion())}, alert=${document.querySelector('[role="alert"]')?.textContent?.trim() ?? 'none'})`,
  )
  click('[data-testid="delete-user"] .management-dialog__footer button:nth-child(2)')
  await waitUntil(() => document.querySelector('[data-testid="delete-user"]') === null, 'queued account deletion was not canceled')
  await waitUntil(() => mounted.controller.users.snapshot().rows.find((value) => value.username === 'browser-single')?.disabled === true, 'canceled deletion did not keep the account disabled')
  assert(mounted.confirmations() >= 4, 'destructive page commands bypassed confirmation before remount')

  mounted.app.unmount()
  await session.logout()
  await session.login({ username: 'browser-user', password: 'browser replacement password' })
  assert(session.state().status === 'authenticated', 'reset password login failed')
  mounted = await mountAdministration('browser-user')
  const manifest = await mounted.api.manifest()
  assert(manifest.menus.some((value) => value.kind === 'directory' && value.label === 'Browser Menu Updated'), 'assigned menu not projected')
  assert(document.querySelector('[data-testid="create-user"]') === null, 'unauthorized create action was visible')
  assert(row('browser-user')?.querySelector('[data-action]') === null, 'unauthorized row actions were visible')
  await mounted.router.push('/iam/roles')
  await waitUntil(() => document.querySelector('section[aria-labelledby="roles-heading"]') === null && document.body.textContent?.includes('当前账号没有可用的管理视图') === true, 'unauthorized role route rendered')
  await openView(mounted, 'users')
  const before = await (await control('snapshot')).json() as { users: number }
  let denied = false
  try { await mounted.api.createUser({ username: 'direct-forbidden', displayName: 'Forbidden', email: 'direct-forbidden@example.test', password: 'forbidden password' }) } catch { denied = true }
  assert(denied, 'direct unauthorized API succeeded')
  const after = await (await control('snapshot')).json() as { users: number }
  assert(after.users === before.users, 'direct denial changed state')

  mounted.app.unmount()
  await session.logout()
  await session.login({ username: 'admin', password: 'administrator password' })
  mounted = await mountAdministration()
  await clickRowAndWait('browser-user', 'edit', '[data-testid="assign-user-roles"]')
  await input('[data-testid="assign-user-roles"] [data-role-key="browser-reader"]', false)
  submit('assign-user-roles')
  await waitUntil(() => mounted.controller.users.snapshot().rows.find((value) => value.username === 'browser-user')?.roleIds.length === 0, 'assigned user role cleanup failed')
  await openView(mounted, 'roles')
  clickRow('browser-reader', 'delete')
  await waitUntil(() => row('browser-reader') === null, 'role cleanup failed')
  await openView(mounted, 'menus')
  await clickRowAndWait('iam-users', 'edit', '[data-testid="edit-menu"]')
  await input('[data-testid="edit-menu"] [name="parentId"]', '')
  submit('edit-menu')
  await waitUntil(() => !mounted.controller.menus().find(value => value.key === 'iam-users')?.parentId, 'menu parent cleanup failed')
  clickRow('browser-menu', 'delete')
  await waitUntil(() => row('browser-menu') === null, 'menu cleanup failed')
  assert(mounted.confirmations() >= 2, 'cleanup page commands bypassed confirmation')
  await control('revoke-role-read', 'POST')
  let revoked = false
  try { await mounted.api.listRoles() } catch { revoked = true }
  assert(revoked, 'permission revoke was not immediate')
  mounted.app.unmount()
  document.body.innerHTML = '<pre id="result">IAM_ADMIN_E2E_PASS</pre>'
  await control('shutdown', 'POST')
}

await scenario().catch(async (error: unknown) => {
  renderFailure(error)
  await control('shutdown', 'POST').catch(() => undefined)
})
