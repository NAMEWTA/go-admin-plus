import { createSessionController, type SessionController } from '@go-admin-plus/domain-iam/session'
import { createWebSessionClient } from '@go-admin-plus/web-domain-iam/session'

const originalPassword = 'correct horse battery'
const replacementPassword = 'replacement password value'
const profileUpdate = { displayName: 'Browser Verified', email: 'browser@example.test' }

const assert: (condition: unknown, message: string) => asserts condition = (condition, message) => {
  if (!condition) throw new Error(message)
}

const controllerFor = (fetcher?: typeof fetch): SessionController => createSessionController(createWebSessionClient(fetcher, '/api'))
let controller = controllerFor()

const reset = () => { controller = controllerFor() }
const authenticated = () => {
  const state = controller.state()
  assert(state.status === 'authenticated', 'expected authenticated state')
  return state.profile
}

const login = async (password = originalPassword) => {
  reset()
  await controller.login({ username: 'admin', password })
  return controller.state().status
}

const control = async (path: string) => {
  const response = await fetch(`/__test/${path}`, { method: 'POST' })
  assert(response.ok, 'test control request failed')
}

const snapshot = async (): Promise<{ displayName: string; activeSessions: number; replacementPasswordActive: boolean }> => {
  const response = await fetch('/__test/snapshot')
  assert(response.ok, 'test snapshot request failed')
  return response.json() as Promise<{ displayName: string; activeSessions: number; replacementPasswordActive: boolean }>
}

const credentialMutatingFetch = (mode: 'missing' | 'tampered'): typeof fetch => async (input, init) => {
  const request = new Request(input, init)
  const headers = new Headers(request.headers)
  if (request.method !== 'GET') {
    if (mode === 'missing') headers.delete('X-CSRF-Token')
    else headers.set('X-CSRF-Token', 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa')
  }
  return fetch(new Request(request, { headers }))
}

const driver = {
  async loginAndCheckState() {
    assert(await login() === 'authenticated', 'login failed')
    authenticated()
    return true
  },

  async verifyCSRF() {
    await controller.updateProfile(profileUpdate)
    assert(authenticated().displayName === profileUpdate.displayName, 'valid CSRF update failed')
    const before = await snapshot()

    for (const mode of ['missing', 'tampered'] as const) {
      const rejected = controllerFor(credentialMutatingFetch(mode))
      await rejected.restore()
      assert(rejected.state().status === 'authenticated', 'CSRF fixture could not restore identity')
      await rejected.updateProfile({ displayName: 'Must Not Persist', email: 'rejected@example.test' })
      assert(rejected.state().status === 'unauthenticated', 'CSRF rejection did not require login')
      const after = await snapshot()
      assert(after.displayName === before.displayName, 'CSRF rejection changed account state')
    }
    reset()
    await controller.restore()
    authenticated()
    return true
  },

  async renew() {
    await control('advance?seconds=61')
    await controller.renew()
    authenticated()
    return true
  },

  async restoreSharedSession() {
    reset()
    await controller.restore()
    return controller.state().status
  },

  async updateFromSecondTab() {
    await controller.updateProfile({ displayName: 'Cross Tab Renewal', email: 'cross-tab@example.test' })
    assert(authenticated().displayName === 'Cross Tab Renewal', 'second tab could not use the renewed session')
    return true
  },

  async logoutSharedSession() {
    await controller.logout()
    assert(controller.state().status === 'unauthenticated', 'shared logout retained identity')
    return true
  },

  async updateProfile() {
    await controller.updateProfile({ displayName: 'Persisted Profile', email: 'persisted@example.test' })
    assert(authenticated().displayName === 'Persisted Profile', 'profile update did not reach controller state')
    assert((await snapshot()).displayName === 'Persisted Profile', 'profile update was not persisted')
    return true
  },

  async verifyLogoutRetry() {
    await control('logout-failure?enabled=true')
    await controller.logout()
    const failed = controller.state()
    assert(failed.status === 'error' && failed.profile?.displayName === 'Persisted Profile', 'failed logout discarded identity')
    await control('logout-failure?enabled=false')
    await controller.logout()
    assert(controller.state().status === 'unauthenticated', 'successful logout retained identity')
    return true
  },

  async verifyIdleTimeout() {
    assert(await login() === 'authenticated', 'idle fixture login failed')
    await control('advance?seconds=121')
    await controller.restore()
    assert(controller.state().status === 'unauthenticated', 'idle timeout did not require login')
    return true
  },

  async verifyAbsoluteTimeout() {
    assert(await login() === 'authenticated', 'absolute fixture login failed')
    for (let index = 0; index < 4; index += 1) {
      await control('advance?seconds=60')
      await controller.heartbeat()
      authenticated()
    }
    await control('advance?seconds=61')
    await controller.restore()
    assert(controller.state().status === 'unauthenticated', 'absolute timeout did not require login')
    return true
  },

  async verifyPasswordChange() {
    assert(await login() === 'authenticated', 'password fixture login failed')
    await controller.changePassword(originalPassword, replacementPassword)
    assert(controller.state().status === 'unauthenticated', 'password change did not revoke identity')
    assert((await snapshot()).replacementPasswordActive, 'password change did not persist')
    await control('advance?seconds=600')
    await control('advance?seconds=600')
    assert(await login(replacementPassword) === 'authenticated', 'replacement password was rejected')
    await controller.logout()
    assert(controller.state().status === 'unauthenticated', 'replacement login logout failed')
    assert(await login(originalPassword) === 'unauthenticated', 'old password remained valid')
    return true
  },

  async shutdown() {
    await control('shutdown')
    return true
  },
}

declare global {
  interface Window { __iamE2E: typeof driver }
}

window.__iamE2E = driver
