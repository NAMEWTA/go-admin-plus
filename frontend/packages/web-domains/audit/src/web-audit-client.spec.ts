import { describe, expect, it } from 'vitest'
import { AuditRequestError } from '@go-admin-plus/domain-audit'
import { createWebAuditClient } from './web-audit-client'

const json = (value: unknown, init: ResponseInit = {}) => new Response(JSON.stringify(value), { status: 200, headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': 'c'.repeat(43) }, ...init })

describe('web audit client', () => {
  it('uses browser cookies, applies replacement CSRF, and exposes only redacted facts', async () => {
    const requests: Request[] = []
    const fetcher: typeof fetch = async (input, init) => {
      requests.push(new Request(input, init))
      if (requests.length === 1) return json({ records: [], total: 0, page: 1, pageSize: 20 })
      return json({ deleted: 0, moreEligible: false })
    }
    const client = createWebAuditClient(fetcher, 'https://app.example.test/api')
    await client.list({ filters: {}, page: 1, pageSize: 20 })
    await client.cleanup('2026-06-01T00:00:00Z')
    expect(requests[0]?.credentials).toBe('include')
    expect(requests[0]?.headers.has('Authorization')).toBe(false)
    expect(requests[1]?.headers.get('X-CSRF-Token')).toBe('c'.repeat(43))
    expect(JSON.stringify(client)).not.toContain('c'.repeat(43))
  })

  it('maps bad session and CSRF failures to relogin without leaking server detail', async () => {
    for (const response of [
      json({ category: 'authentication', detail: 'private token' }, { status: 401 }),
      json({ category: 'authorization', code: 'CSRF_REJECTED', detail: 'private csrf' }, { status: 403 }),
    ]) {
		const client = createWebAuditClient(async () => response.clone(), 'https://app.example.test/api')
      await expect(client.list({ filters: {}, page: 1, pageSize: 20 })).rejects.toEqual(new AuditRequestError('relogin'))
    }
  })

  it('preserves only a safe problem trace reference', async () => {
    const client = createWebAuditClient(async () => json({ category: 'conflict', traceId: 'trace-audit-01234567', detail: 'private SQL' }, { status: 409 }), 'https://app.example.test/api')
    await expect(client.cleanup('2026-06-01T00:00:00Z')).rejects.toMatchObject({ category: 'conflict', traceId: 'trace-audit-01234567', message: 'Audit request failed' })
  })
})
