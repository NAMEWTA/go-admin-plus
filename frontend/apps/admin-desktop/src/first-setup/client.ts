import { invoke as tauriInvoke } from '@tauri-apps/api/core'

export interface FirstSetupInput {
  username: string
  displayName: string
  email: string
  password: string
}

export type FirstSetupState = 'required' | 'login-required' | 'unavailable'
export type FirstSetupOutcome =
  | { state: 'complete'; profile: PublicProfile }
  | { state: 'login-required' }

interface PublicProfile {
  id: string
  username: string
  displayName: string
  email: string
  avatarRef: string | null
}

type Invoke = <T>(command: string, args?: Record<string, unknown>) => Promise<T>

const record = (value: unknown): Record<string, unknown> => {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) throw new Error('首次设置响应无效')
  return value as Record<string, unknown>
}

const exactKeys = (value: Record<string, unknown>, expected: readonly string[]) => {
  const actual = Object.keys(value).sort()
  const required = [...expected].sort()
  if (actual.length !== required.length || actual.some((key, index) => key !== required[index])) {
    throw new Error('首次设置响应无效')
  }
}

const parseState = (value: unknown): FirstSetupState => {
  const result = record(value)
  exactKeys(result, ['state'])
  if (result.state !== 'required' && result.state !== 'login-required' && result.state !== 'unavailable') {
    throw new Error('首次设置响应无效')
  }
  return result.state
}

const parseProfile = (value: unknown): PublicProfile => {
  const profile = record(value)
  exactKeys(profile, ['id', 'username', 'displayName', 'email', 'avatarRef'])
  if (typeof profile.id !== 'string' || profile.id.length === 0 ||
    typeof profile.username !== 'string' || profile.username.length === 0 ||
    typeof profile.displayName !== 'string' || profile.displayName.length === 0 ||
    typeof profile.email !== 'string' || profile.email.length === 0 ||
    (profile.avatarRef !== null && typeof profile.avatarRef !== 'string')) {
    throw new Error('首次设置响应无效')
  }
  return profile as unknown as PublicProfile
}

const parseOutcome = (value: unknown): FirstSetupOutcome => {
  const result = record(value)
  if (result.state === 'login-required') {
    exactKeys(result, ['state'])
    return { state: 'login-required' }
  }
  if (result.state === 'complete') {
    exactKeys(result, ['state', 'profile'])
    return { state: 'complete', profile: parseProfile(result.profile) }
  }
  throw new Error('首次设置响应无效')
}

export const createFirstSetupClient = (invoke: Invoke = tauriInvoke) => ({
  state: async () => parseState(await invoke<unknown>('desktop_first_setup_state')),
  submit: async (input: FirstSetupInput) => parseOutcome(await invoke<unknown>('desktop_first_setup_submit', { input }))
})
