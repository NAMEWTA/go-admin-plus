import { describe, expect, it, vi } from 'vitest'
import { SchedulerRequestError, type Definition, type SchedulerClient } from '@go-admin-plus/domain-scheduler'
import { createSchedulerController, settleSchedulerPageOperation } from './scheduler-controller'

const taskTypes = [{ key: 'reports.daily', label: 'Daily', fields: [{ name: 'name', label: 'Name', kind: 'string' as const, required: true }] }]
const definition: Definition = { id: '00000000-0000-4000-8000-000000000001', name: 'Daily', taskType: 'reports.daily', schedule: { cron: '0 1 * * *', timezone: 'UTC' }, parameters: { name: 'sales' }, enabled: false, revision: 1, createdAt: '2026-08-27T00:00:00Z', updatedAt: '2026-08-27T00:00:00Z' }
const capability = { can: () => true, scope: () => 'all' as const }
const client = (): SchedulerClient => ({
  taskTypes: vi.fn(async () => taskTypes), listDefinitions: vi.fn(async () => ({ rows: [definition], total: 1 })), createDefinition: vi.fn(async () => definition), updateDefinition: vi.fn(async () => definition), runDefinition: vi.fn(async () => ({executionId:'00000000-0000-4000-8000-000000000099'})), enableDefinition: vi.fn(async () => ({ ...definition, enabled: true, revision: 2 })), stopDefinition: vi.fn(async () => definition), deleteDefinition: vi.fn(async () => undefined), listExecutions: vi.fn(async () => ({ rows: [], total: 0 })),
})

describe('scheduler controller', () => {
  it('retains a safe trace id for actionable failure feedback', async () => {
    const api = client(); const controller = createSchedulerController(api, capability, async () => true)
    vi.mocked(api.listDefinitions).mockRejectedValueOnce(new SchedulerRequestError('conflict', 'trace-scheduler-01234567'))
    await expect(controller.definitions.refresh()).rejects.toBeInstanceOf(SchedulerRequestError)
    expect(controller.failureTraceId()).toBe('trace-scheduler-01234567')
  })
  it('returns a completed page operation result while always settling', async () => {
    const settled = vi.fn()

    await expect(settleSchedulerPageOperation(async () => 'completed' as const, settled)).resolves.toBe('completed')
    await expect(settleSchedulerPageOperation(async () => { throw new Error('handled by controller') }, settled)).resolves.toBeUndefined()
    expect(settled).toHaveBeenCalledTimes(2)
  })

  it('blocks repeated mutations after write success until projection repair', async () => {
    const api = client(); let calls = 0
    vi.mocked(api.listDefinitions).mockImplementation(async () => { calls += 1; if (calls === 2) throw new SchedulerRequestError('unavailable'); return { rows: [definition], total: 1 } })
    const controller = createSchedulerController(api, capability, async () => true)
    await controller.refreshTaskTypes(); await controller.definitions.refresh()
    expect(await controller.createDefinition({ name: 'Daily', taskType: 'reports.daily', schedule: definition.schedule, parameters: { name: 'sales' } })).toBe('refresh-failed')
    expect(await controller.enableDefinition(definition)).toBe('refresh-failed')
    expect(api.createDefinition).toHaveBeenCalledTimes(1)
    expect(await controller.repairProjection()).toBe('completed')
  })

  it('clears projected rows immediately when capability is revoked', async () => {
    let allowed = true
    const controller = createSchedulerController(client(), { can: () => allowed, scope: () => 'all' }, async () => true)
    await controller.definitions.refresh(); expect(controller.definitions.snapshot().rows).toHaveLength(1)
    allowed = false
    expect(controller.definitions.snapshot()).toMatchObject({ rows: [], total: 0, selectedKeys: [] })
  })

  it('rechecks delete capability after asynchronous confirmation', async () => {
    let allowed = true; let release!: (value: boolean) => void
    const api = client()
    const controller = createSchedulerController(api, { can: () => allowed, scope: () => 'all' }, () => new Promise(resolve => { release = resolve }))
    const pending = controller.deleteDefinition(definition)
    allowed = false; release(true)
    expect(await pending).toBe('failed')
    expect(api.deleteDefinition).not.toHaveBeenCalled()
  })

  it('ignores a stale failure after the latest successful request', async () => {
    let reject!: (value: unknown) => void; let resolve!: (value: { rows: Definition[]; total: number }) => void
    const api = client()
    vi.mocked(api.listDefinitions).mockImplementationOnce(() => new Promise((_, failure) => { reject = failure })).mockImplementationOnce(() => new Promise(success => { resolve = success }))
    const controller = createSchedulerController(api, capability, async () => true)
    const stale = controller.definitions.search({ search: 'old' }); const current = controller.definitions.search({ search: 'new' })
    resolve({ rows: [definition], total: 1 }); await current; reject(new SchedulerRequestError('forbidden')); await stale
    expect(controller.definitions.snapshot().rows).toHaveLength(1); expect(controller.failure()).toBeNull()
  })
})
