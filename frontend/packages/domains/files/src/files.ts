import type { components } from './generated/client'

export type FileMetadata = components['schemas']['FileMetadata']
export type FilePage = components['schemas']['FilePage']
export type DeleteFileTarget = components['schemas']['DeleteFileTarget']
export type FilesFailure = 'relogin' | 'forbidden' | 'validation' | 'content' | 'not-found' | 'conflict' | 'quota' | 'capacity' | 'unavailable'
export type FilesPermissionCode = 'files.objects.read' | 'files.objects.write' | 'files.objects.delete'

export const filesPermissions = {
  read: 'files.objects.read',
  write: 'files.objects.write',
  delete: 'files.objects.delete',
} as const satisfies Readonly<Record<string, FilesPermissionCode>>

export interface FileQuery {
  readonly search: string
  readonly page: number
  readonly pageSize: number
  readonly sort: 'name' | 'sizeBytes' | 'createdAt'
  readonly direction: 'ascending' | 'descending'
}

export interface UploadCandidate {
  readonly name: string
  readonly type: string
  readonly size: number
  readonly body: Blob
}

export interface UploadPolicy { readonly maxUploadBytes:number; readonly allowedTypes:ReadonlyArray<string> }
export interface FilesClient {
  uploadPolicy?(): Promise<UploadPolicy>
  list(query: FileQuery): Promise<FilePage>
  upload(candidate: UploadCandidate): Promise<FileMetadata>
  download(id: string): Promise<Blob>
  delete(targets: ReadonlyArray<DeleteFileTarget>): Promise<void>
}

export class FilesRequestError extends Error {
  readonly category: FilesFailure
  readonly traceId?: string
  constructor(category: FilesFailure, traceId?: string) {
    super(category)
    this.category = category
    if (traceId !== undefined) this.traceId = traceId
  }
}

export const fileMediaTypes = ['application/pdf', 'image/jpeg', 'image/png', 'text/plain'] as const
export const maximumFileBytes = 10 * 1024 * 1024
export const codePointLength = (value: string): number => Array.from(value).length
const containsUnicodeControl = (value: string): boolean => /\p{Cc}/u.test(value)
export const validFileSearch = (value: string): boolean => codePointLength(value.trim()) <= 100
export const validFileName = (value: string): boolean => {
  const name = value.trim()
  return codePointLength(name) >= 1 && codePointLength(name) <= 255 && name !== '.' && name !== '..'
    && !name.includes('/') && !name.includes('\\') && !containsUnicodeControl(name)
}
export const validUploadCandidate = (candidate: UploadCandidate, policy: UploadPolicy = {maxUploadBytes:maximumFileBytes,allowedTypes:fileMediaTypes}): boolean => {
  return validFileName(candidate.name) && policy.allowedTypes.includes(candidate.type)
    && Number.isSafeInteger(candidate.size) && candidate.size >= 0 && candidate.size <= policy.maxUploadBytes
    && candidate.body.size === candidate.size && candidate.body.type === candidate.type
}
