import { describe, expect, it, vi } from 'vitest'
import { createWebFilesClient } from './web-files-client'

const csrf = 'c'.repeat(43)
const metadata = { id: '00000000-0000-4000-8000-000000000013', originalName: 'notes.txt', mediaType: 'text/plain', sizeBytes: 5, sha256: 'a'.repeat(64), revision: 1, createdAt: '2026-08-27T00:00:00Z', updatedAt: '2026-08-27T00:00:00Z' }
const json = (status: number, body: unknown, headers: Record<string, string> = {}) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': status >= 400 ? 'application/problem+json' : 'application/json', ...headers } })

describe('web files client', () => {
  it('uses the typed list contract and carries validated CSRF only to mutations', async () => {
    const fetcher = vi.fn<typeof fetch>()
      .mockResolvedValueOnce(json(200, { rows: [metadata], total: 1 }, { 'X-CSRF-Token': csrf }))
      .mockResolvedValueOnce(json(201, metadata, { 'X-CSRF-Token': csrf }))
    const client = createWebFilesClient(fetcher, 'https://files.test/api')
    expect((await client.list({ search: '', page: 1, pageSize: 20, sort: 'createdAt', direction: 'descending' })).rows).toHaveLength(1)
    await client.upload({ name: 'notes.txt', type: 'text/plain', size: 5, body: new Blob(['hello'], { type: 'text/plain' }) })
    expect(new Request(fetcher.mock.calls[0]![0]).headers.has('X-CSRF-Token')).toBe(false)
    const uploadRequest = new Request(fetcher.mock.calls[1]![0])
    expect(uploadRequest.headers.get('X-CSRF-Token')).toBe(csrf)
    expect(uploadRequest.body).not.toBeNull()
  })

  it('keeps ordinary forbidden distinct and maps CSRF rejection to relogin', async () => {
    const fetcher = vi.fn<typeof fetch>()
      .mockResolvedValueOnce(json(200, { rows: [], total: 0 }, { 'X-CSRF-Token': csrf }))
      .mockResolvedValueOnce(json(403, { category: 'authorization', code: 'PERMISSION_DENIED' }))
      .mockResolvedValueOnce(json(403, { category: 'authorization', code: 'CSRF_REJECTED' }))
    const client = createWebFilesClient(fetcher, 'https://files.test/api')
    await client.list({ search: '', page: 1, pageSize: 20, sort: 'createdAt', direction: 'descending' })
    await expect(client.delete([{ id: metadata.id, revision: 1 }])).rejects.toMatchObject({ category: 'forbidden' })
    await expect(client.delete([{ id: metadata.id, revision: 1 }])).rejects.toMatchObject({ category: 'relogin' })
    expect(new Request(fetcher.mock.calls[2]![0]).headers.get('X-CSRF-Token')).toBe(csrf)
  })

  it('fails closed on malformed CSRF or an over-posted response', async () => {
    const client = createWebFilesClient(vi.fn<typeof fetch>().mockResolvedValue(json(200, { rows: [], total: 0 }, { 'X-CSRF-Token': 'bad+token' })), 'https://files.test/api')
    await expect(client.list({ search: '', page: 1, pageSize: 20, sort: 'createdAt', direction: 'descending' })).rejects.toMatchObject({ category: 'relogin' })

    const overPosted = createWebFilesClient(vi.fn<typeof fetch>().mockResolvedValue(json(200, { rows: [{ ...metadata, storageKey: 'object-secret' }], total: 1 })), 'https://files.test/api')
    await expect(overPosted.list({ search: '', page: 1, pageSize: 20, sort: 'createdAt', direction: 'descending' })).rejects.toMatchObject({ category: 'unavailable' })
  })

  it('returns downloaded bytes without exposing credentials through the port', async () => {
    const fetcher = vi.fn<typeof fetch>().mockResolvedValue(new Response('hello', { status: 200, headers: { 'X-CSRF-Token': csrf, 'Content-Type': 'application/octet-stream' } }))
    const blob = await createWebFilesClient(fetcher, 'https://files.test/api').download(metadata.id)
    expect(await blob.text()).toBe('hello')
    expect(Object.keys(createWebFilesClient(fetcher))).toEqual(['uploadPolicy', 'list', 'upload', 'download', 'delete'])
  })

  it.each([
    [409, 'FILES_QUOTA_EXCEEDED', 'quota'],
    [507, 'FILES_CAPACITY_UNAVAILABLE', 'capacity'],
    [415, 'MEDIA_TYPE_REJECTED', 'content'],
  ] as const)('maps %s/%s to %s and preserves a safe reference', async (status, code, category) => {
    const client = createWebFilesClient(vi.fn<typeof fetch>().mockResolvedValue(json(status, { category: status === 415 ? 'validation' : 'conflict', code, traceId: 'trace_files_123', detail: 'discard me' })), 'https://files.test/api')
    await expect(client.upload({ name: 'notes.txt', type: 'text/plain', size: 5, body: new Blob(['hello'], { type: 'text/plain' }) })).rejects.toMatchObject({ category, traceId: 'trace_files_123' })
  })

  it('discards a malformed problem reference', async () => {
    const client = createWebFilesClient(vi.fn<typeof fetch>().mockResolvedValue(json(507, { category: 'conflict', code: 'FILES_CAPACITY_UNAVAILABLE', traceId: '<raw>' })), 'https://files.test/api')
    const failure = await client.upload({ name: 'notes.txt', type: 'text/plain', size: 5, body: new Blob(['hello'], { type: 'text/plain' }) }).catch(error => error)
    expect(failure).toMatchObject({ category: 'capacity' })
    expect(failure.traceId).toBeUndefined()
  })
})
