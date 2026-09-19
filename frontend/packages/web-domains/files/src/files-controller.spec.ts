import { describe, expect, it, vi } from 'vitest'
import { FilesRequestError, filesPermissions, type FileMetadata, type FilesClient, type FilesPermissionCode, type UploadCandidate } from '@go-admin-plus/domain-files'
import { createFilesController } from './files-controller'
import pageSource from './FilesPage.vue?raw'

const file = (id = '00000000-0000-4000-8000-000000000013'): FileMetadata => ({ id, originalName: 'notes.txt', mediaType: 'text/plain', sizeBytes: 5, sha256: 'a'.repeat(64), revision: 1, createdAt: '2026-08-27T00:00:00Z', updatedAt: '2026-08-27T00:00:00Z' })
const candidate: UploadCandidate = { name: 'notes.txt', type: 'text/plain', size: 5, body: new Blob(['hello'], { type: 'text/plain' }) }
const deferred = <T>() => { let resolve!: (value: T) => void; let reject!: (error: unknown) => void; const promise = new Promise<T>((success, failure) => { resolve = success; reject = failure }); return { promise, resolve, reject } }
const fixture = () => {
  let rows = [file()]
  const client: FilesClient = {
    list: vi.fn(async () => ({ rows, total: rows.length })),
    upload: vi.fn(async () => { rows = [file()]; return file() }),
    download: vi.fn(async () => new Blob(['hello'])),
    delete: vi.fn(async () => { rows = [] }),
  }
  const capabilities = { can: vi.fn<(code: FilesPermissionCode) => boolean>(() => true) }
  return { client, capabilities, controller: createFilesController(client, vi.fn(async () => true), capabilities) }
}

describe('files controller', () => {
  it('binds rows and actions to the fail-closed projection', () => {
    expect(pageSource).toContain('v-if="projectionVisible && canRead"')
    expect(pageSource).toContain('data-testid="files-upload"')
    expect(pageSource).toContain('data-testid="files-delete-selected"')
  })

  it.each(['quota', 'capacity'] as const)('keeps release actions available after an upload %s failure', async category => {
    const { client, controller } = fixture()
    await controller.list.refresh()
    vi.mocked(client.upload).mockRejectedValueOnce(new FilesRequestError(category, 'trace_files_123'))
    expect(await controller.upload(candidate)).toBe('failed')
    expect(controller.failure()).toBe(category)
    expect(controller.failureTraceId()).toBe('trace_files_123')
    expect(controller.projectionVisible).toBe(true)
    expect(controller.can(filesPermissions.read)).toBe(true)
    expect(controller.can(filesPermissions.delete)).toBe(true)
    expect(pageSource).toContain("failure.value === 'quota' || failure.value === 'capacity'")
  })

  it('searches, resets, pages, sorts and selects through the shared list controller', async () => {
    const { client, controller } = fixture()
    await controller.list.search({ search: ' notes ' })
    await controller.list.setPageSize(10)
    await controller.list.setSort({ key: 'name', direction: 'ascending' })
    controller.list.select(controller.list.snapshot().rows)
    expect(controller.list.snapshot().selectedKeys).toEqual([file().id])
    expect(client.list).toHaveBeenLastCalledWith(expect.objectContaining({ search: 'notes', pageSize: 10, sort: 'name', direction: 'ascending' }))
    await controller.list.reset()
    expect(controller.list.snapshot().filters.search).toBe('')
  })

  it('repairs repeated refresh failures without repeating upload', async () => {
    const { client, controller } = fixture()
    await controller.list.refresh()
    vi.mocked(client.list).mockRejectedValueOnce(new FilesRequestError('unavailable')).mockRejectedValueOnce(new FilesRequestError('unavailable')).mockResolvedValue({ rows: [file()], total: 1 })
    expect(await controller.upload(candidate)).toBe('refresh-failed')
    expect(await controller.upload({ ...candidate, name: 'other.txt' })).toBe('refresh-failed')
    expect(await controller.repairProjection()).toBe('refresh-failed')
    expect(await controller.repairProjection()).toBe('completed')
    expect(client.upload).toHaveBeenCalledTimes(1)
    expect(controller.takeCompletion()).toBe('upload')
  })

  it('confirms once, rechecks capability after confirmation and never repeats delete during repair', async () => {
    const { client, capabilities } = fixture()
    const gate = deferred<boolean>()
    const confirm = vi.fn(() => gate.promise)
    const controller = createFilesController(client, confirm, capabilities)
    await controller.list.refresh()
    const removal = controller.remove([file()])
    capabilities.can.mockImplementation(code => code !== filesPermissions.delete)
    gate.resolve(true)
    expect(await removal).toBe('failed')
    expect(client.delete).not.toHaveBeenCalled()

    capabilities.can.mockReturnValue(true)
    await controller.list.refresh()
    vi.mocked(client.list).mockRejectedValueOnce(new FilesRequestError('unavailable')).mockResolvedValue({ rows: [], total: 0 })
    expect(await controller.remove([file()])).toBe('refresh-failed')
    expect(await controller.remove([file('00000000-0000-4000-8000-000000000014')])).toBe('refresh-failed')
    expect(await controller.repairProjection()).toBe('completed')
    expect(client.delete).toHaveBeenCalledTimes(1)
    expect(confirm).toHaveBeenCalledTimes(2)
  })

  for (const category of ['forbidden', 'relogin', 'unavailable'] as const) {
    it(`hides stale rows and actions after current ${category} list failure`, async () => {
      const { client, controller } = fixture()
      await controller.list.refresh()
      controller.list.select(controller.list.snapshot().rows)
      vi.mocked(client.list).mockRejectedValueOnce(new FilesRequestError(category))
      await expect(controller.list.refresh()).rejects.toMatchObject({ category })
      expect(controller.failure()).toBe(category)
      expect(controller.projectionVisible).toBe(false)
      expect(controller.can(filesPermissions.write)).toBe(false)
      expect(controller.list.snapshot()).toMatchObject({ rows: [], total: 0, selectedKeys: [] })
    })
  }

  it('ignores stale list success and failure', async () => {
    const { client, controller } = fixture()
    const stale = deferred<{ rows: FileMetadata[]; total: number }>()
    const current = deferred<{ rows: FileMetadata[]; total: number }>()
    vi.mocked(client.list).mockImplementationOnce(() => stale.promise).mockImplementationOnce(() => current.promise)
    const first = controller.list.refresh()
    const second = controller.list.refresh()
    current.reject(new FilesRequestError('unavailable'))
    await expect(second).rejects.toMatchObject({ category: 'unavailable' })
    stale.resolve({ rows: [file()], total: 1 })
    await first
    expect(controller.failure()).toBe('unavailable')
    expect(controller.list.snapshot().rows).toEqual([])
  })

  it('fails closed when capabilities are withdrawn after a successful projection', async () => {
    const { capabilities, controller } = fixture()
    await controller.list.refresh()
    capabilities.can.mockReturnValue(false)
    expect(controller.projectionVisible).toBe(false)
    expect(controller.list.snapshot()).toMatchObject({ rows: [], total: 0, selectedKeys: [] })
    expect(await controller.upload(candidate)).toBe('failed')
  })

  it('does not destroy a stable projection for locally invalid search', async () => {
    const { client, controller } = fixture()
    await controller.list.refresh()
    await expect(controller.list.search({ search: '😀'.repeat(101) })).rejects.toMatchObject({ category: 'validation' })
    expect(client.list).toHaveBeenCalledTimes(1)
    expect(controller.projectionVisible).toBe(true)
    expect(controller.list.snapshot().rows).toHaveLength(1)
  })
})
