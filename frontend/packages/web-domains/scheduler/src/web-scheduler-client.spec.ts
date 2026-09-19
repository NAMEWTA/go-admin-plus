import { describe, expect, it, vi } from 'vitest'
import { createWebSchedulerClient } from './web-scheduler-client'

describe('web scheduler client', () => {
  it('uses cookie credentials and rotates CSRF without Authorization', async () => {
    const requests: Request[] = []
    const fetcher = vi.fn(async (request: Request) => { requests.push(request); if (request.method === 'GET') return new Response(JSON.stringify([]), { status: 200, headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': 'csrf-next' } }); return new Response(null, { status: 204 }) })
    const api = createWebSchedulerClient(fetcher as typeof fetch, 'https://admin.test/api')
    await api.taskTypes(); await api.deleteDefinition('00000000-0000-4000-8000-000000000001', 1)
    expect(requests.every(request => request.credentials === 'include')).toBe(true)
    expect(requests.every(request => !request.headers.has('Authorization'))).toBe(true)
    expect(requests[1]?.headers.get('X-CSRF-Token')).toBe('csrf-next')
  })

  it('maps stable problem categories without exposing detail', async () => {
    const fetcher = vi.fn(async () => new Response(JSON.stringify({ category: 'conflict', traceId: 'trace-scheduler-01234567', detail: 'private SQL' }), { status: 409, headers: { 'Content-Type': 'application/problem+json' } }))
    const api = createWebSchedulerClient(fetcher as typeof fetch, 'https://admin.test/api')
    await expect(api.deleteDefinition('00000000-0000-4000-8000-000000000001', 1)).rejects.toMatchObject({ category: 'conflict', traceId: 'trace-scheduler-01234567', message: 'Scheduler request failed' })
  })
})
