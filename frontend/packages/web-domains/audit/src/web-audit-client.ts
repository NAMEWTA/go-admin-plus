import {
  AuditRequestError,
  createContractClient,
  type AuditClient,
  type AuditFailure,
  type AuditListRequest,
} from '@go-admin-plus/domain-audit'
import { createSessionAwareFetch } from '@go-admin-plus/ui'

interface Problem { category?: string; code?: string; traceId?: string }

export const createWebAuditClient = (fetcher: typeof fetch = fetch, baseUrl = '/api'): AuditClient => {
  let requestTail: Promise<void> = Promise.resolve()
  const serialized = <T>(operation: () => Promise<T>): Promise<T> => {
    const result = requestTail.then(operation, operation)
    requestTail = result.then(() => undefined, () => undefined)
    return result
  }
  const contract = createContractClient({
    baseUrl,
    fetch: createSessionAwareFetch(fetcher),
  })

  const unwrap = <T>(data: T | undefined, error: unknown): T => {
    if (error !== undefined) throw failure(error)
    if (data === undefined) throw new AuditRequestError('unavailable')
    return data
  }
  const failure = (value: unknown): AuditRequestError => {
    const problem = isProblem(value) ? value : {}
    const category = publicFailure(problem)
    return new AuditRequestError(category, safeTraceId(problem.traceId))
  }

  return {
    list: (request: AuditListRequest) => serialized(async () => {
      const query = { page: request.page, pageSize: request.pageSize, ...request.filters }
      const result = await contract.GET('/audit/records', { params: { query } })
      return unwrap(result.data, result.error)
    }),
    detail: (id: string) => serialized(async () => {
      const result = await contract.GET('/audit/records/{id}', { params: { path: { id } } })
      return unwrap(result.data, result.error)
    }),
    cleanup: (before: string) => serialized(async () => {
      const result = await contract.POST('/audit/records/cleanup', { body: { before, confirmation: 'delete-expired-audit-records' } })
      return unwrap(result.data, result.error)
    }),
  }
}

const isProblem = (value: unknown): value is Problem => typeof value === 'object' && value !== null
const safeTraceId = (value: unknown): string | null => typeof value === 'string' && /^[A-Za-z0-9_-]{8,128}$/.test(value) ? value : null
const publicFailure = (problem: Problem): AuditFailure => {
  if (problem.category === 'authentication' || problem.code === 'CSRF_REJECTED') return 'relogin'
  if (problem.category === 'authorization') return 'forbidden'
  if (problem.category === 'validation') return 'validation'
  if (problem.category === 'conflict') return 'conflict'
  if (problem.category === 'not_found') return 'not-found'
  return 'unavailable'
}
