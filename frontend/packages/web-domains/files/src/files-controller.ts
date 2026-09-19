import { FilesRequestError, filesPermissions, validFileSearch, validUploadCandidate, type FileMetadata, type FilesClient, type FilesFailure, type FilesPermissionCode, type UploadPolicy, type UploadCandidate } from '@go-admin-plus/domain-files'
import { createListController, type ListController } from '@go-admin-plus/ui'

export type FilesMutationResult = 'completed' | 'invalid' | 'cancelled' | 'empty' | 'busy' | 'failed' | 'refresh-failed'
export interface FilesFilters { readonly search: string }
export interface FilesCapabilityPort { can(permissionCode: FilesPermissionCode): boolean }
export type FilesCompletedIntent = 'upload' | 'remove'

export interface FilesController {
  uploadPolicy(): Promise<UploadPolicy>
  readonly list: ListController<FilesFilters, FileMetadata, string>
  readonly busy: boolean
  readonly pendingRepair: boolean
  readonly projectionVisible: boolean
  failure(): FilesFailure | null
  failureTraceId(): string | null
  clearFailure(): void
  can(permissionCode: FilesPermissionCode): boolean
  takeCompletion(): FilesCompletedIntent | null
  upload(candidate: UploadCandidate): Promise<FilesMutationResult>
  remove(files: ReadonlyArray<FileMetadata>): Promise<FilesMutationResult>
  download(file: FileMetadata): Promise<Blob | null>
  repairProjection(): Promise<FilesMutationResult>
}

export const createFilesController = (client: FilesClient, confirm: (count: number) => Promise<boolean>, capabilities: FilesCapabilityPort): FilesController => {
  let mutationBusy = false
  let repairBusy = false
  let pending: FilesCompletedIntent | null = null
  let completion: FilesCompletedIntent | null = null
  let failure: FilesFailure | null = null
  let failureTraceId: string | null = null
  let projectionVisible = false
  let projectionGeneration = 0
  let requestSequence = 0
  const record = (error: unknown) => {
    failure = error instanceof FilesRequestError ? error.category : 'unavailable'
    failureTraceId = error instanceof FilesRequestError ? error.traceId ?? null : null
  }
  let rawList!: ListController<FilesFilters, FileMetadata, string>
  const hideProjection = () => { projectionVisible = false; rawList.clearSelection() }
  const failOperation = (error: unknown) => {
    record(error)
    if (failure === 'relogin' || failure === 'forbidden' || failure === 'unavailable') {
      requestSequence += 1
      hideProjection()
    }
  }
  rawList = createListController<FilesFilters, FileMetadata, string>({
    initialFilters: () => ({ search: '' }),
    normalizeFilters: filters => ({ search: filters.search.trim() }),
    validate: request => {
      if (!validFileSearch(request.filters.search)) {
        requestSequence += 1
        failure = 'validation'
        throw new FilesRequestError('validation')
      }
    },
    rowKey: row => row.id,
    load: async request => {
      const sequence = ++requestSequence
      failure = null; failureTraceId = null
      try {
        if (!capabilities.can(filesPermissions.read)) throw new FilesRequestError('forbidden')
        const result = await client.list({ search: request.filters.search, page: request.page, pageSize: request.pageSize,
          sort: (request.sort?.key ?? 'createdAt') as 'name'|'sizeBytes'|'createdAt', direction: request.sort?.direction ?? 'descending' })
        if (sequence === requestSequence) { failure = null; failureTraceId = null; projectionVisible = true; projectionGeneration += 1 }
        return result
      } catch (error) {
        if (sequence === requestSequence) { record(error); hideProjection() }
        throw error
      }
    },
  })
  const list: ListController<FilesFilters, FileMetadata, string> = {
    snapshot() {
      const snapshot = rawList.snapshot()
      if (projectionVisible && capabilities.can(filesPermissions.read)) return snapshot
      return { ...snapshot, rows: [], total: 0, selectedKeys: [] }
    },
    refresh: () => rawList.refresh(),
    search: filters => rawList.search(filters),
    reset: () => rawList.reset(),
    setPage: page => rawList.setPage(page),
    setPageSize: pageSize => rawList.setPageSize(pageSize),
    setSort: sort => rawList.setSort(sort),
    select(rows) { if (projectionVisible && capabilities.can(filesPermissions.read)) rawList.select(rows) },
    clearSelection: () => rawList.clearSelection(),
  }
  const refresh = async (): Promise<FilesMutationResult> => {
    failure = null; failureTraceId = null
    const previousGeneration = projectionGeneration
    try {
      await list.refresh()
      if (!projectionVisible || projectionGeneration === previousGeneration) return 'refresh-failed'
      completion = pending
      pending = null
      return 'completed'
    } catch (error) {
      record(error)
      return 'refresh-failed'
    }
  }
  return {
    uploadPolicy: () => client.uploadPolicy?.() ?? Promise.resolve({maxUploadBytes:10*1024*1024,allowedTypes:['application/pdf','image/jpeg','image/png','text/plain']}),
    list,
    get busy() { return mutationBusy || repairBusy },
    get pendingRepair() { return pending !== null },
    get projectionVisible() { return projectionVisible && capabilities.can(filesPermissions.read) },
    failure: () => failure,
    failureTraceId: () => failureTraceId,
    clearFailure: () => { failure = null; failureTraceId = null },
    can: permissionCode => projectionVisible && capabilities.can(filesPermissions.read) && capabilities.can(permissionCode),
    takeCompletion() { const value = completion; completion = null; return value },
    async upload(candidate) {
      if (mutationBusy || repairBusy) return 'busy'
      if (pending) return 'refresh-failed'
      if (!projectionVisible || !capabilities.can(filesPermissions.write)) { failOperation(new FilesRequestError('forbidden')); return 'failed' }
      mutationBusy = true
      failure = null; failureTraceId = null
      try {
        try {
          const policy = await (client.uploadPolicy?.() ?? Promise.resolve({ maxUploadBytes: 10 * 1024 * 1024, allowedTypes: ['application/pdf', 'image/jpeg', 'image/png', 'text/plain'] }))
          if (!validUploadCandidate(candidate, policy)) return 'invalid'
        } catch (error) { failOperation(error); return 'failed' }
        try { await client.upload(candidate) } catch (error) { failOperation(error); return 'failed' }
        pending = 'upload'
        return await refresh()
      } finally { mutationBusy = false }
    },
    async remove(files) {
      if (mutationBusy || repairBusy) return 'busy'
      if (pending) return 'refresh-failed'
      if (!projectionVisible || !capabilities.can(filesPermissions.delete)) { failOperation(new FilesRequestError('forbidden')); return 'failed' }
      if (files.length === 0) return 'empty'
      mutationBusy = true
      failure = null; failureTraceId = null
      try {
        if (!await confirm(files.length)) return 'cancelled'
        if (!projectionVisible || !capabilities.can(filesPermissions.delete)) { failOperation(new FilesRequestError('forbidden')); return 'failed' }
        try { await client.delete(files.map(({ id, revision }) => ({ id, revision }))) } catch (error) { failOperation(error); return 'failed' }
        list.clearSelection()
        pending = 'remove'
        return await refresh()
      } finally { mutationBusy = false }
    },
    async download(file) {
      if (mutationBusy || repairBusy) return null
      if (!projectionVisible || !capabilities.can(filesPermissions.read)) { failOperation(new FilesRequestError('forbidden')); return null }
      mutationBusy = true
      failure = null; failureTraceId = null
      try {
        try { return await client.download(file.id) } catch (error) { failOperation(error); return null }
      } finally { mutationBusy = false }
    },
    async repairProjection() {
      if (mutationBusy || repairBusy) return 'busy'
      if (!pending) return 'completed'
      repairBusy = true
      try { return await refresh() } finally { repairBusy = false }
    },
  }
}
