import { createContractClient, SchedulerRequestError, type SchedulerClient } from '@go-admin-plus/domain-scheduler'
import { createSessionAwareFetch } from '@go-admin-plus/ui'

interface Problem { category?: string; code?: string; traceId?: string }

export const createWebSchedulerClient = (fetcher: typeof fetch = fetch, baseUrl = '/api'): SchedulerClient => {
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
    if (error !== undefined) throw new SchedulerRequestError(problemCategory(error), safeTraceId((error as Problem)?.traceId))
    if (data === undefined) throw new SchedulerRequestError('unavailable')
    return data
  }
  const complete = (error: unknown) => { if (error !== undefined) throw new SchedulerRequestError(problemCategory(error), safeTraceId((error as Problem)?.traceId)) }
  return {
    taskTypes: () => serialized(async () => { const result = await contract.GET('/scheduler/task-types'); return unwrap(result.data, result.error) }),
    listDefinitions: (search, page, pageSize) => serialized(async () => { const result = await contract.GET('/scheduler/definitions', { params: { query: { search, page, pageSize } } }); return unwrap(result.data, result.error) }),
    createDefinition: (body) => serialized(async () => { const result = await contract.POST('/scheduler/definitions', { body }); return unwrap(result.data, result.error) }),
    updateDefinition: (definitionId, revision, body) => serialized(async () => { const result = await contract.PATCH('/scheduler/definitions/{definitionId}', { params: { path: { definitionId } }, body: { ...body, revision } }); return unwrap(result.data, result.error) }),
    runDefinition: (definitionId, revision) => serialized(async () => { const result = await contract.POST('/scheduler/definitions/{definitionId}/run', { params: { path: { definitionId } }, body: { revision } }); return unwrap(result.data, result.error) }),
    enableDefinition: (definitionId, revision) => serialized(async () => { const result = await contract.POST('/scheduler/definitions/{definitionId}/enable', { params: { path: { definitionId } }, body: { revision } }); return unwrap(result.data, result.error) }),
    stopDefinition: (definitionId, revision) => serialized(async () => { const result = await contract.POST('/scheduler/definitions/{definitionId}/stop', { params: { path: { definitionId } }, body: { revision } }); return unwrap(result.data, result.error) }),
    deleteDefinition: (definitionId, revision) => serialized(async () => { const result = await contract.DELETE('/scheduler/definitions/{definitionId}', { params: { path: { definitionId }, query: { revision } } }); complete(result.error) }),
    listExecutions: (definitionId, status, page, pageSize) => serialized(async () => { const result = await contract.GET('/scheduler/executions', { params: { query: { ...(definitionId ? { definitionId } : {}), ...(status ? { status } : {}), page, pageSize } } }); return unwrap(result.data, result.error) }),
  }
}

const problemCategory = (value: unknown) => {
  if (typeof value !== 'object' || value === null) return 'unavailable'
  const problem = value as Problem
  if (problem.code === 'CSRF_REJECTED' || problem.category === 'authentication') return 'relogin'
  if (problem.category === 'authorization') return 'forbidden'
  if (problem.category === 'validation' || problem.category === 'conflict' || problem.category === 'not-found') return problem.category
  return 'unavailable'
}
const safeTraceId = (value: unknown): string | null => typeof value === 'string' && /^[A-Za-z0-9_-]{8,128}$/.test(value) ? value : null
