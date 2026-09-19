import type { components } from './generated/schema'

export type AccountProfile = components['schemas']['Profile']
export type LoginCredentials = components['schemas']['LoginRequest']
export type UpdateProfile = components['schemas']['UpdateProfileRequest']

export interface SessionClient {
  login(credentials: LoginCredentials): Promise<AccountProfile>
  current(): Promise<AccountProfile>
  heartbeat(): Promise<AccountProfile>
  renew(): Promise<AccountProfile>
  logout(): Promise<void>
  profile(): Promise<AccountProfile>
  updateProfile(update: UpdateProfile): Promise<AccountProfile>
  changePassword(currentPassword: string, newPassword: string): Promise<void>
}

export type SessionState =
  | { status: 'unauthenticated'; profile: null; error: null }
  | { status: 'loading'; profile: AccountProfile | null; error: null }
  | { status: 'authenticated'; profile: AccountProfile; error: null }
  | { status: 'error'; profile: AccountProfile | null; error: 'validation' | 'conflict' | 'unavailable' }

export interface SessionController {
  state(): SessionState
  subscribe(listener: (state: SessionState) => void): () => void
  restore(): Promise<void>
  heartbeat(): Promise<void>
  renew(): Promise<void>
  login(credentials: LoginCredentials): Promise<void>
  logout(): Promise<void>
  updateProfile(update: UpdateProfile): Promise<void>
  changePassword(currentPassword: string, newPassword: string): Promise<void>
}

export const createSessionController = (client: SessionClient): SessionController => {
  let state: SessionState = { status: 'unauthenticated', profile: null, error: null }
  const listeners = new Set<(value: SessionState) => void>()
  let operation = 0
  const publish = (next: SessionState) => {
    state = next
    listeners.forEach((listener) => listener(next))
  }
  const run = async (action: () => Promise<AccountProfile>, retain: boolean) => {
    const sequence = ++operation
    publish({ status: 'loading', profile: retain ? state.profile : null, error: null })
    try {
      const profile = await action()
      if (sequence === operation) publish({ status: 'authenticated', profile, error: null })
    } catch (error) {
      if (sequence !== operation) return
      if (requiresRelogin(error)) publish({ status: 'unauthenticated', profile: null, error: null })
      else publish({ status: 'error', profile: state.profile, error: publicError(error) })
    }
  }
  const maintain = async (action: () => Promise<AccountProfile>) => {
    const sequence = operation
    try {
      const profile = await action()
      if (sequence === operation && state.status === 'authenticated') {
        publish({ status: 'authenticated', profile, error: null })
      }
    } catch (error) {
      if (sequence !== operation || state.status !== 'authenticated') return
      if (requiresRelogin(error)) {
        operation += 1
        publish({ status: 'unauthenticated', profile: null, error: null })
      }
    }
  }
  return {
    state: () => state,
    subscribe(listener) { listeners.add(listener); listener(state); return () => listeners.delete(listener) },
    restore: () => run(() => client.current(), false),
    heartbeat: () => maintain(() => client.heartbeat()),
    renew: () => maintain(() => client.renew()),
    login: (credentials) => run(() => client.login(credentials), false),
    async logout() {
      const sequence = ++operation
      publish({ status: 'loading', profile: state.profile, error: null })
      try {
        await client.logout()
        if (sequence === operation) publish({ status: 'unauthenticated', profile: null, error: null })
      } catch (error) {
        if (sequence !== operation) return
        if (requiresRelogin(error)) publish({ status: 'unauthenticated', profile: null, error: null })
        else publish({ status: 'error', profile: state.profile, error: publicError(error) })
      }
    },
    updateProfile: (update) => run(() => client.updateProfile(update), true),
    async changePassword(currentPassword, newPassword) {
      const sequence = ++operation
      publish({ status: 'loading', profile: state.profile, error: null })
      try {
        await client.changePassword(currentPassword, newPassword)
        if (sequence === operation) publish({ status: 'unauthenticated', profile: null, error: null })
      } catch (error) {
        if (sequence !== operation) return
        if (requiresRelogin(error)) publish({ status: 'unauthenticated', profile: null, error: null })
        else publish({ status: 'error', profile: state.profile, error: publicError(error) })
      }
    },
  }
}

const requiresRelogin = (error: unknown) => error instanceof SessionRequestError
  && (error.category === 'authentication' || error.category === 'authorization')
const publicError = (error: unknown): 'validation' | 'conflict' | 'unavailable' => {
  if (error instanceof SessionRequestError && (error.category === 'validation' || error.category === 'conflict')) return error.category
  return 'unavailable'
}

export class SessionRequestError extends Error {
  readonly category: string

  constructor(category: string) {
    super('Session request failed')
    this.name = 'SessionRequestError'
    this.category = category
  }
}
