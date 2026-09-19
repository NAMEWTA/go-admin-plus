import { describe, expect, it } from 'vitest'
import { AuditRequestError, type AuditClient, type AuditFact } from '@go-admin-plus/domain-audit'
import { consumeCleanupFailure, createAuditController } from './audit-controller'

const fact: AuditFact = {
  id: 'audit-web-000001', kind: 'login', action: 'login', outcome: 'succeeded',
  actorType: 'account', actorRef: 'account:account-1', source: 'web',
  subject: 'login:attempt-1', occurredAt: '2026-08-01T00:00:00Z',
}

describe('audit controller', () => {
  it('uses deterministic list filters and bounded destructive cleanup', async () => {
    const calls: string[] = []
    const client: AuditClient = {
      list: async (request) => { calls.push(`list:${request.page}:${request.filters.source ?? ''}`); return { records: [fact], total: 1, page: request.page, pageSize: request.pageSize } },
      detail: async () => fact,
      cleanup: async (before) => { calls.push(`cleanup:${before}`); return { deleted: 1, moreEligible: false } },
    }
    const controller = createAuditController(client, async () => true)
    await controller.list.search({ source: 'web' })
		controller.list.select([fact])
    const first = controller.cleanup('2026-06-01T00:00:00Z')
    const duplicate = await controller.cleanup('2026-06-01T00:00:00Z')
    expect(await first).toBe('completed')
    expect(duplicate).toBe('busy')
    expect(calls.filter((call) => call.startsWith('cleanup'))).toHaveLength(1)
    expect(controller.list.snapshot().rows).toEqual([fact])
		expect(controller.list.snapshot().filters).toEqual({ source: 'web' })
		expect(controller.list.snapshot().selectedKeys).toEqual([])
  })

  it('does not execute cleanup when confirmation is rejected', async () => {
    let writes = 0
    const client: AuditClient = {
      list: async () => ({ records: [], total: 0, page: 1, pageSize: 20 }),
      detail: async () => fact,
      cleanup: async () => { writes += 1; return { deleted: 0, moreEligible: false } },
    }
    const controller = createAuditController(client, async () => false)
    expect(await controller.cleanup('2026-06-01T00:00:00Z')).toBe('cancelled')
    expect(writes).toBe(0)
  })

  it('fences new cleanup until repeated refresh repair succeeds', async () => {
    const writes: string[] = []
    let confirmations = 0
    let loads = 0
    const client: AuditClient = {
      list: async () => {
        loads += 1
        if (loads <= 2) throw new Error('temporary read failure')
        return { records: [], total: 0, page: 1, pageSize: 20 }
      },
      detail: async () => fact,
      cleanup: async (before) => { writes.push(before); return { deleted: 1, moreEligible: false } },
    }
    const controller = createAuditController(client, async () => { confirmations += 1; return true })
    const firstBoundary = '2026-06-01T00:00:00Z'
    const secondBoundary = '2026-05-01T00:00:00Z'
    expect(await controller.cleanup(firstBoundary)).toBe('refresh-failed')
    expect(await controller.cleanup(secondBoundary)).toBe('repair-required')
    expect(writes).toEqual([firstBoundary])
    expect(confirmations).toBe(1)
    expect(await controller.repairCleanup()).toBe('refresh-failed')
    expect(await controller.repairCleanup()).toBe('completed')
    expect(await controller.cleanup(secondBoundary)).toBe('completed')
    expect(writes).toEqual([firstBoundary, secondBoundary])
    expect(confirmations).toBe(2)
    expect(loads).toBe(4)
  })

  it.each(['relogin', 'forbidden'] as const)('preserves post-write refresh %s without repeating cleanup', async (category) => {
    let writes = 0
    let relogins = 0
    let loads = 0
    const client: AuditClient = {
      list: async () => {
        loads += 1
        if (loads === 1) throw new AuditRequestError(category)
        return { records: [], total: 0, page: 1, pageSize: 20 }
      },
      detail: async () => fact,
      cleanup: async () => { writes += 1; return { deleted: 1, moreEligible: false } },
    }
    const controller = createAuditController(client, async () => true)
    expect(await controller.cleanup('2026-06-01T00:00:00Z')).toBe('refresh-failed')
    expect(consumeCleanupFailure(controller, () => { relogins += 1 })).toBe(category)
    expect(relogins).toBe(category === 'relogin' ? 1 : 0)
    expect(await controller.cleanup('2026-05-01T00:00:00Z')).toBe('repair-required')
    expect(writes).toBe(1)
    expect(await controller.repairCleanup()).toBe('completed')
    expect(writes).toBe(1)
  })

  it('updates the stable category across repeated repair failures and eventually recovers', async () => {
    let writes = 0
    let relogins = 0
    let loads = 0
    const client: AuditClient = {
      list: async () => {
        loads += 1
        if (loads === 1) throw new AuditRequestError('relogin')
        if (loads === 2) throw new AuditRequestError('forbidden')
        if (loads === 3) throw new AuditRequestError('relogin')
        return { records: [], total: 0, page: 1, pageSize: 20 }
      },
      detail: async () => fact,
      cleanup: async () => { writes += 1; return { deleted: 1, moreEligible: false } },
    }
    const controller = createAuditController(client, async () => true)
    expect(await controller.cleanup('2026-06-01T00:00:00Z')).toBe('refresh-failed')
    expect(consumeCleanupFailure(controller, () => { relogins += 1 })).toBe('relogin')
    expect(await controller.repairCleanup()).toBe('refresh-failed')
    expect(consumeCleanupFailure(controller, () => { relogins += 1 })).toBe('forbidden')
    expect(await controller.repairCleanup()).toBe('refresh-failed')
    expect(consumeCleanupFailure(controller, () => { relogins += 1 })).toBe('relogin')
    expect(await controller.repairCleanup()).toBe('completed')
    expect(controller.lastFailure()).toBeNull()
    expect(relogins).toBe(2)
    expect(writes).toBe(1)
  })

	it.each(['relogin', 'forbidden'] as const)('preserves the %s cleanup failure category', async (category) => {
		const client: AuditClient = {
			list: async () => ({ records: [], total: 0, page: 1, pageSize: 20 }),
			detail: async () => fact,
			cleanup: async () => { throw new AuditRequestError(category) },
		}
		const controller = createAuditController(client, async () => true)
		expect(await controller.cleanup('2026-06-01T00:00:00Z')).toBe('failed')
		expect(controller.lastFailure()).toBe(category)
		expect(controller.lastCleanup()).toBeNull()
	})

  it('retains a safe trace id for actionable cleanup feedback', async () => {
    const api: AuditClient = { list: async () => ({ records: [], total: 0, page: 1, pageSize: 20 }), detail: async () => fact, cleanup: async () => { throw new AuditRequestError('conflict', 'trace-audit-01234567') } }
    const controller = createAuditController(api, async () => true)
    expect(await controller.cleanup('2026-06-01T00:00:00Z')).toBe('failed')
    expect(controller.lastTraceId()).toBe('trace-audit-01234567')
  })
})
