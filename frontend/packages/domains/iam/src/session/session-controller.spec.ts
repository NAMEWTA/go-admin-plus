import { describe, expect, it } from 'vitest'
import { createSessionController, SessionRequestError, type AccountProfile, type SessionClient } from './session-controller'

const profile: AccountProfile = { id: '1', username: 'admin', displayName: 'Admin', email: 'admin@example.test' }
const client = (overrides: Partial<SessionClient> = {}): SessionClient => ({
  login: async () => profile, current: async () => profile, logout: async () => undefined,
  heartbeat: async () => profile, renew: async () => profile,
  profile: async () => profile, updateProfile: async () => profile, changePassword: async () => undefined,
  ...overrides,
})

describe('session controller', () => {
  it('does not expose cookie, token, csrf, or password state', async () => {
    const controller = createSessionController(client())
    await controller.login({ username: 'admin', password: 'sensitive-value' })
    expect(controller.state()).toEqual({ status: 'authenticated', profile, error: null })
    expect(JSON.stringify(controller.state())).not.toMatch(/token|csrf|password|sensitive-value/i)
  })

  it('maps expired sessions to the stable unauthenticated state', async () => {
    const controller = createSessionController(client({ current: async () => { throw new SessionRequestError('authentication') } }))
    await controller.restore()
    expect(controller.state().status).toBe('unauthenticated')
  })

  it('invalidates the local identity after password change', async () => {
    const controller = createSessionController(client())
    await controller.restore()
    await controller.changePassword('old-sensitive', 'new-sensitive')
    expect(controller.state().status).toBe('unauthenticated')
  })

  it('keeps the authenticated profile when password validation fails', async () => {
    const controller = createSessionController(client({ changePassword: async () => { throw new SessionRequestError('validation') } }))
    await controller.restore()
    await controller.changePassword('wrong-old-value', 'invalid-new-value')
    expect(controller.state()).toEqual({ status: 'error', profile, error: 'validation' })
  })

  it('keeps the profile and allows retry when logout is unavailable', async () => {
    const controller = createSessionController(client({ logout: async () => { throw new SessionRequestError('internal') } }))
    await controller.restore()
    await controller.logout()
    expect(controller.state()).toEqual({ status: 'error', profile, error: 'unavailable' })
  })

  it.each(['authentication', 'authorization'])('requires login after %s logout rejection', async (category) => {
    const controller = createSessionController(client({ logout: async () => { throw new SessionRequestError(category) } }))
    await controller.restore()
    await controller.logout()
    expect(controller.state()).toEqual({ status: 'unauthenticated', profile: null, error: null })
  })

  it('does not let a stale rejected logout overwrite a newer login', async () => {
    let rejectLogout: ((reason: unknown) => void) | undefined
    const pendingLogout = new Promise<void>((_, reject) => { rejectLogout = reject })
    const controller = createSessionController(client({ logout: () => pendingLogout }))
    await controller.restore()
    const stale = controller.logout()
    await controller.login({ username: 'admin', password: 'sensitive-value' })
    rejectLogout?.(new SessionRequestError('internal'))
    await stale
    expect(controller.state()).toEqual({ status: 'authenticated', profile, error: null })
  })

  it('requires login after CSRF rejection on a protected mutation', async () => {
    const controller = createSessionController(client({ updateProfile: async () => { throw new SessionRequestError('authorization') } }))
    await controller.restore()
    await controller.updateProfile({ displayName: 'Admin', email: 'admin@example.test' })
    expect(controller.state()).toEqual({ status: 'unauthenticated', profile: null, error: null })
  })

  it('refreshes heartbeat without publishing a loading state', async () => {
    const refreshed = { ...profile, displayName: 'Admin Updated' }
    const controller = createSessionController(client({ heartbeat: async () => refreshed }))
    await controller.restore()
    const states: string[] = []
    const unsubscribe = controller.subscribe(state => { states.push(state.status) })
    states.length = 0
    await controller.heartbeat()
    expect(controller.state()).toEqual({ status: 'authenticated', profile: refreshed, error: null })
    expect(states).toEqual(['authenticated'])
    unsubscribe()
  })

  it('never revives a session after renew reports authentication failure', async () => {
    const controller = createSessionController(client({
      renew: async () => { throw new SessionRequestError('authentication') }
    }))
    await controller.restore()
    await controller.renew()
    expect(controller.state()).toEqual({ status: 'unauthenticated', profile: null, error: null })
  })
})
