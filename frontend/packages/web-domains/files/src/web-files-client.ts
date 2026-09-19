import {
  createContractClient,
  FilesRequestError,
  fileMediaTypes,
  validFileName,
  type DeleteFileTarget,
  type FileMetadata,
  type FilePage,
  type FileQuery,
  type FilesClient,
  type FilesFailure,
  type UploadCandidate,
} from '@go-admin-plus/domain-files'
import { createSessionAwareFetch } from '@go-admin-plus/ui'

interface Problem { category?: string; code?: string; traceId?: string }
const uuidPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i
const tracePattern = /^[A-Za-z0-9_-]{8,128}$/

export const createWebFilesClient = (fetcher: typeof fetch = fetch, baseUrl = '/api'): FilesClient => {
  let tail: Promise<void> = Promise.resolve()
  const serialized = <T>(operation: () => Promise<T>): Promise<T> => {
    const result = tail.then(operation, operation)
    tail = result.then(() => undefined, () => undefined)
    return result
  }
  const transport = createSessionAwareFetch(fetcher)
  const contract = createContractClient({ baseUrl, fetch: transport })
  const failure = (error: unknown): never => {
    const category = problemCategory(error)
    const traceId = error instanceof FilesRequestError ? error.traceId : safeTraceId((error as Problem)?.traceId)
    throw new FilesRequestError(category, traceId)
  }
  const responseData = <T>(data: T | undefined, error: unknown): T => error === undefined && data !== undefined ? data : failure(error)
  const direct = async (path: string, init: RequestInit): Promise<Response> => {
    const response = await transport(new Request(`${baseUrl}${path}`, init))
    if (!response.ok) {
      const problem = await response.clone().json().catch(() => null)
      failure(problem ?? new Error('files request failed'))
    }
    return response
  }
  return {
    uploadPolicy: async () => {
      const response=await fetcher(`${baseUrl}/runtime/info`)
      if(!response.ok)throw new FilesRequestError('unavailable')
      const value=await response.json() as {maxUploadBytes:number;allowedTypes:string[]}
      if(!Number.isSafeInteger(value.maxUploadBytes)||value.maxUploadBytes<1||value.maxUploadBytes>1073741824||!Array.isArray(value.allowedTypes)||!value.allowedTypes.every(v=>typeof v==='string'))throw new FilesRequestError('unavailable')
      return {maxUploadBytes:value.maxUploadBytes,allowedTypes:value.allowedTypes}
    },
    list: query => serialized(async () => {
      const result = await contract.GET('/files/objects', { params: { query } })
      return parsePage(responseData(result.data, result.error))
    }),
    upload: candidate => serialized(async () => {
      const form = new FormData()
      form.append('file', candidate.body, candidate.name)
      const response = await direct('/files/objects', { method: 'POST', body: form })
      return parseMetadata(await response.json())
    }),
    download: id => serialized(async () => {
      if (!uuidPattern.test(id)) throw new FilesRequestError('validation')
      const response = await direct(`/files/objects/${encodeURIComponent(id)}/content`, { method: 'GET', headers: { accept: 'application/octet-stream' } })
      return response.blob()
    }),
    delete: targets => serialized(async () => {
      const result = await contract.POST('/files/objects/batch-delete', { body: { files: [...targets] } })
      if (result.error !== undefined) failure(result.error)
    }),
  }
}

const exactKeys = (record: Record<string, unknown>, expected: ReadonlyArray<string>) => {
  const actual = Object.keys(record).sort()
  const wanted = [...expected].sort()
  return actual.length === wanted.length && actual.every((key, index) => key === wanted[index])
}

const parseMetadata = (value: unknown): FileMetadata => {
  if (!value || typeof value !== 'object') throw new FilesRequestError('unavailable')
  const record = value as Record<string, unknown>
  if (!exactKeys(record, ['id', 'originalName', 'mediaType', 'sizeBytes', 'sha256', 'revision', 'createdAt', 'updatedAt'])
    || typeof record.id !== 'string' || !uuidPattern.test(record.id)
    || typeof record.originalName !== 'string' || !validFileName(record.originalName)
    || typeof record.mediaType !== 'string' || !fileMediaTypes.includes(record.mediaType as typeof fileMediaTypes[number])
    || !Number.isSafeInteger(record.sizeBytes) || Number(record.sizeBytes) < 0 || Number(record.sizeBytes) > 1024 * 1024 * 1024
    || typeof record.sha256 !== 'string' || !/^[a-f0-9]{64}$/.test(record.sha256)
    || !Number.isSafeInteger(record.revision) || Number(record.revision) < 1
    || typeof record.createdAt !== 'string' || !validDate(record.createdAt)
    || typeof record.updatedAt !== 'string' || !validDate(record.updatedAt)) {
    throw new FilesRequestError('unavailable')
  }
  return record as unknown as FileMetadata
}

const parsePage = (value: unknown): FilePage => {
  if (!value || typeof value !== 'object') throw new FilesRequestError('unavailable')
  const record = value as Record<string, unknown>
  if (!exactKeys(record, ['rows', 'total']) || !Array.isArray(record.rows) || record.rows.length > 100 || !Number.isSafeInteger(record.total) || Number(record.total) < 0) {
    throw new FilesRequestError('unavailable')
  }
  return { rows: record.rows.map(parseMetadata), total: Number(record.total) }
}

const validDate = (value: string) => Number.isFinite(Date.parse(value)) && value.includes('T')
const problemCategory = (value: unknown): FilesFailure => {
  if (value instanceof FilesRequestError) return value.category
  if (!value || typeof value !== 'object') return 'unavailable'
  const problem = value as Problem
  if (problem.code === 'CSRF_REJECTED' || problem.category === 'authentication') return 'relogin'
  if (problem.category === 'authorization') return 'forbidden'
  if (problem.code === 'FILES_QUOTA_EXCEEDED') return 'quota'
  if (problem.code === 'FILES_CAPACITY_UNAVAILABLE') return 'capacity'
  if (problem.code === 'CONTENT_TOO_LARGE' || problem.code === 'MEDIA_TYPE_REJECTED' || problem.code === 'FILE_SIZE_MISMATCH') return 'content'
  if (problem.category === 'validation') return 'validation'
  if (problem.category === 'not_found') return 'not-found'
  if (problem.category === 'conflict') return 'conflict'
  return 'unavailable'
}
const safeTraceId = (value: unknown): string | undefined => typeof value === 'string' && tracePattern.test(value) ? value : undefined

export type { DeleteFileTarget, FileQuery, UploadCandidate }
