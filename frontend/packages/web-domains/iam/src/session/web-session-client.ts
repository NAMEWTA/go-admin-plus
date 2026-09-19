import {
  createContractClient,
  SessionRequestError,
  type AccountProfile,
  type LoginCredentials,
  type SessionClient,
  type UpdateProfile
} from '@go-admin-plus/domain-iam/session'
import { createSessionAwareFetch } from '@go-admin-plus/ui'

interface Problem { category?: string }
interface SessionFact { csrfToken: string, profile: AccountProfile }

const csrfPattern = /^[A-Za-z0-9_-]{43}$/

export const createWebSessionClient = (fetcher: typeof fetch = fetch, baseUrl = '/api'): SessionClient => {
  const transport = createSessionAwareFetch(fetcher)
  const contract = createContractClient({
    baseUrl,
    fetch: transport
  })
  const unwrap = <T>(data: T | undefined, error: unknown): T => {
    if (error !== undefined) {
      if (problemCategory(error) === 'authorization' || problemCategory(error) === 'authentication') transport.resetSession?.()
      throw new SessionRequestError(problemCategory(error))
    }
    if (data === undefined) throw new SessionRequestError('unavailable')
    return data
  }
  const complete = (error: unknown) => {
    if (error !== undefined) {
      if (problemCategory(error) === 'authorization' || problemCategory(error) === 'authentication') transport.resetSession?.()
      throw new SessionRequestError(problemCategory(error))
    }
  }
  const establish = (session: SessionFact): AccountProfile => {
    if (!csrfPattern.test(session.csrfToken)) {
      throw new SessionRequestError('unavailable')
    }
    return session.profile
  }
  const maintain = (session: SessionFact): AccountProfile => {
    if (!csrfPattern.test(session.csrfToken)) {
      throw new SessionRequestError('authorization')
    }
    return session.profile
  }
  return {
    login: async (credentials: LoginCredentials) => {
      const result = await contract.POST('/iam/session/login', { body: credentials })
      return establish(unwrap(result.data, result.error))
    },
    current: async () => {
      const result = await contract.GET('/iam/session/current')
      return establish(unwrap(result.data, result.error))
    },
    heartbeat: async () => {
      const result = await contract.POST('/iam/session/heartbeat')
      return maintain(unwrap(result.data, result.error))
    },
    renew: async () => {
      const result = await contract.POST('/iam/session/renew')
      return maintain(unwrap(result.data, result.error))
    },
    logout: async () => {
      const result = await contract.POST('/iam/session/logout')
      complete(result.error)
    },
    profile: async () => {
      const result = await contract.GET('/iam/account/profile')
      return unwrap(result.data, result.error) as AccountProfile
    },
    updateProfile: async (update: UpdateProfile) => {
      const result = await contract.PATCH('/iam/account/profile', { body: update })
      return unwrap(result.data, result.error) as AccountProfile
    },
    changePassword: async (currentPassword: string, newPassword: string) => {
      const result = await contract.PUT('/iam/account/password', { body: { currentPassword, newPassword } })
      complete(result.error)
    }
  }
}

const problemCategory = (value: unknown) => typeof value === 'object' && value !== null && 'category' in value
  ? String((value as Problem).category ?? 'unavailable')
  : 'unavailable'
