import { describe, expect, it } from 'vitest'
import { AdministrationRequestError } from '@go-admin-plus/domain-iam/administration'
import { createWebAdministrationClient } from './web-administration-client'

describe('web administration client', () => {
  it('serializes session refreshes and sends the latest CSRF with credentials', async () => {
    const requests: Request[] = []
    const responses = [
      new Response('[]', { status: 200, headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': 'a'.repeat(43) } }),
      new Response(JSON.stringify({ id: 'role-000000000001', key: 'reader', name: 'Reader', dataScope: 'self', enabled: true, protected: false, permissionCodes: [], menuIds: [] }), { status: 201, headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': 'b'.repeat(43) } }),
    ]
    const client = createWebAdministrationClient(async (request) => { requests.push(request instanceof Request ? request : new Request(request)); return responses.shift()! }, 'https://app.example.test/api')
    const first = client.listRoles(); const second = client.createRole({ key: 'reader', name: 'Reader', dataScope: 'self' }); await Promise.all([first, second])
    expect(requests[0]?.credentials).toBe('include')
    expect(requests[1]?.headers.get('X-CSRF-Token')).toBe('a'.repeat(43))
  })

  it('keeps replacement CSRF on permission denial but returns a stable error', async () => {
    const requests: Request[] = []
    const responses = [
      new Response(JSON.stringify({ category: 'authorization' }), { status: 403, headers: { 'Content-Type': 'application/problem+json', 'X-CSRF-Token': 'c'.repeat(43) } }),
      new Response(JSON.stringify({ id: 'role-000000000001', key: 'reader', name: 'Reader', dataScope: 'self', enabled: true, protected: false, permissionCodes: [], menuIds: [] }), { status: 201, headers: { 'Content-Type': 'application/json' } }),
    ]
    const client = createWebAdministrationClient(async (request) => { requests.push(request instanceof Request ? request : new Request(request)); return responses.shift()! }, 'https://app.example.test/api')
    await expect(client.manifest()).rejects.toEqual(expect.objectContaining<Partial<AdministrationRequestError>>({ category: 'forbidden' }))
    await client.createRole({ key: 'reader', name: 'Reader', dataScope: 'self' })
    expect(requests[1]?.headers.get('X-CSRF-Token')).toBe('c'.repeat(43))
  })

  it('clears CSRF only for CSRF_REJECTED, not ordinary authorization', async () => {
    const requests: Request[] = []
    const responses = [
      new Response('[]', { status: 200, headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': 'd'.repeat(43) } }),
      new Response(JSON.stringify({ category: 'authorization', code: 'PERMISSION_DENIED' }), { status: 403, headers: { 'Content-Type': 'application/problem+json' } }),
      new Response(JSON.stringify({ id: 'role-000000000001', key: 'reader', name: 'Reader', dataScope: 'self', enabled: true, protected: false, permissionCodes: [], menuIds: [] }), { status: 201, headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': 'e'.repeat(43) } }),
      new Response(JSON.stringify({ category: 'authorization', code: 'CSRF_REJECTED' }), { status: 403, headers: { 'Content-Type': 'application/problem+json' } }),
      new Response(JSON.stringify({ id: 'role-000000000002', key: 'writer', name: 'Writer', dataScope: 'self', enabled: true, protected: false, permissionCodes: [], menuIds: [] }), { status: 201, headers: { 'Content-Type': 'application/json' } }),
    ]
    const client = createWebAdministrationClient(async (request) => { requests.push(request instanceof Request ? request : new Request(request)); return responses.shift()! }, 'https://app.example.test/api')
    await client.listRoles()
    await expect(client.manifest()).rejects.toEqual(expect.objectContaining({ category: 'forbidden' }))
    await client.createRole({ key: 'reader', name: 'Reader', dataScope: 'self' })
    expect(requests[2]?.headers.get('X-CSRF-Token')).toBe('d'.repeat(43))
    await expect(client.manifest()).rejects.toEqual(expect.objectContaining({ category: 'relogin' }))
    await client.createRole({ key: 'writer', name: 'Writer', dataScope: 'self' })
    expect(requests[4]?.headers.get('X-CSRF-Token')).toBeNull()
  })

  it('maps unauthenticated and service failures without leaking transport details', async () => {
    const responses = [
      new Response(JSON.stringify({ category: 'authorization' }), { status: 401, headers: { 'Content-Type': 'application/problem+json' } }),
      new Response(JSON.stringify({ category: 'internal', detail: 'database connection detail' }), { status: 500, headers: { 'Content-Type': 'application/problem+json' } }),
    ]
    const client = createWebAdministrationClient(async () => responses.shift()!, 'https://app.example.test/api')
    await expect(client.manifest()).rejects.toEqual(expect.objectContaining({ category: 'relogin', message: 'IAM administration request failed' }))
    await expect(client.manifest()).rejects.toEqual(expect.objectContaining({ category: 'unavailable', message: 'IAM administration request failed' }))
  })

  it('maps a missing deletion projection to a stable not-found category', async () => {
    const response = new Response(JSON.stringify({ category: 'not_found', detail: 'internal row detail' }), { status: 404, headers: { 'Content-Type': 'application/problem+json' } })
    const client = createWebAdministrationClient(async () => response, 'https://app.example.test/api')
    await expect(client.getUserDeletion('account-00000001')).rejects.toEqual(expect.objectContaining({
      category: 'not-found',
      message: 'IAM administration request failed',
    }))
  })

  it('uses lifecycle and classic scope administration contracts', async () => {
    const requests: Request[] = []
    const deletion = { id: '11111111-1111-1111-1111-111111111111', accountId: 'account-00000001', strategy: 'purge', status: 'queued', auditReference: 'audit-reference', createdAt: '2026-09-01T00:00:00Z', updatedAt: '2026-09-01T00:00:00Z' }
    const responses = [
      new Response(JSON.stringify(deletion), { status: 202, headers: { 'Content-Type': 'application/json' } }),
      new Response(JSON.stringify(deletion), { status: 200, headers: { 'Content-Type': 'application/json' } }),
      new Response(null, { status: 204 }),
    ]
    const client = createWebAdministrationClient(async (request) => {
      requests.push(request instanceof Request ? request : new Request(request))
      return responses.shift()!
    }, 'https://app.example.test/api')

    await client.startUserDeletion('account-00000001', { strategy: 'purge', purgeConfirmed: true })
    await client.getUserDeletion('account-00000001')
    await client.cancelUserDeletion('account-00000001')

    expect(requests.map((request) => [request.method, new URL(request.url).pathname])).toEqual([
      ['POST', '/api/iam/administration/users/account-00000001/deletion'],
      ['GET', '/api/iam/administration/users/account-00000001/deletion'],
      ['POST', '/api/iam/administration/users/account-00000001/deletion/cancel'],
    ])
    await expect(requests[0]!.json()).resolves.toEqual({ strategy: 'purge', purgeConfirmed: true })
  })
})
