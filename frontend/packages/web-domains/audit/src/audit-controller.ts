import { AuditRequestError, type AuditClient, type AuditFact, type AuditFailure, type AuditFilters, type CleanupResult } from '@go-admin-plus/domain-audit'
import {
  createListController,
  createRemovalController,
  type ListController,
  type RemovalRunResult,
} from '@go-admin-plus/ui'

export interface AuditController {
  readonly list: ListController<AuditFilters, AuditFact, string>
  detail(id: string): Promise<AuditFact>
  cleanup(before: string): Promise<AuditCleanupRunResult>
  repairCleanup(): Promise<AuditCleanupRepairResult>
  lastCleanup(): CleanupResult | null
  lastFailure(): AuditFailure | null
  lastTraceId(): string | null
}

export type AuditCleanupRunResult = RemovalRunResult | 'repair-required'
export type AuditCleanupRepairResult = 'completed' | 'empty' | 'busy' | 'refresh-failed'

export const consumeCleanupFailure = (
  controller: Pick<AuditController, 'lastFailure'>,
  sessionRequired: () => void,
): AuditFailure => {
  const failure = controller.lastFailure() ?? 'unavailable'
  if (failure === 'relogin') sessionRequired()
  return failure
}

export const createAuditController = (
  client: AuditClient,
  confirmCleanup: (count: number) => Promise<boolean>,
): AuditController => {
  const list = createListController<AuditFilters, AuditFact, string>({
    initialFilters: () => ({}),
    load: async (request) => {
      const page = await client.list({ filters: request.filters, page: request.page, pageSize: request.pageSize })
      return { rows: page.records, total: page.total }
    },
    rowKey: (fact) => fact.id,
    pageSize: 20,
  })
  let cleanupResult: CleanupResult | null = null
  let cleanupFailure: AuditFailure | null = null
  let cleanupTraceId: string | null = null
  const captureCleanupFailure = (error: unknown) => {
    cleanupFailure = error instanceof AuditRequestError ? error.category : 'unavailable'
    cleanupTraceId = error instanceof AuditRequestError ? error.traceId ?? null : null
  }
  const removal = createRemovalController<string>({
    confirm: confirmCleanup,
    execute: async ([before]) => {
      if (!before) throw new Error('Cleanup boundary is required')
      try {
        cleanupResult = await client.cleanup(before)
      } catch (error) {
        captureCleanupFailure(error)
        throw error
      }
    },
    clearSelection: list.clearSelection,
    refreshed: async () => {
      try {
        await list.refresh()
      } catch (error) {
        captureCleanupFailure(error)
        throw error
      }
    },
  })
  let pendingRepairBoundary: string | null = null
  let repairInFlight = false
  return {
    list,
    detail: (id) => client.detail(id),
    async cleanup(before) {
      if (repairInFlight || removal.busy) return 'busy'
      if (pendingRepairBoundary !== null) return 'repair-required'
      cleanupResult = null
      cleanupFailure = null
      cleanupTraceId = null
      const result = await removal.run([before])
      if (result === 'refresh-failed') pendingRepairBoundary = before
      return result
    },
    async repairCleanup() {
      if (pendingRepairBoundary === null) return 'empty'
      if (repairInFlight || removal.busy) return 'busy'
      repairInFlight = true
      cleanupFailure = null
      cleanupTraceId = null
      try {
        await list.refresh()
        pendingRepairBoundary = null
        return 'completed'
      } catch (error) {
        captureCleanupFailure(error)
        return 'refresh-failed'
      } finally {
        repairInFlight = false
      }
    },
    lastCleanup: () => cleanupResult,
    lastFailure: () => cleanupFailure,
    lastTraceId: () => cleanupTraceId,
  }
}
