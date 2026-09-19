import { SchedulerRequestError, validDefinitionInput, validSchedulerSearch, type Definition, type DefinitionInput, type Execution, type ExecutionStatus, type SchedulerClient, type TaskType } from '@go-admin-plus/domain-scheduler'
import { createListController, type ListController } from '@go-admin-plus/ui'

export interface DefinitionFilters { search: string }
export interface ExecutionFilters { definitionId: string; status: ExecutionStatus | '' }
export type SchedulerFailure = 'relogin' | 'forbidden' | 'validation' | 'not-found' | 'conflict' | 'unavailable'
export type SchedulerCommandResult = 'completed' | 'cancelled' | 'busy' | 'invalid' | 'failed' | 'refresh-failed'
export interface SchedulerCapabilityPort { can(permission: string): boolean; scope(): 'self' | 'all' | null }

export interface SchedulerController {
  definitions: ListController<DefinitionFilters, Definition, string>
  executions: ListController<ExecutionFilters, Execution, string>
  taskTypes(): ReadonlyArray<TaskType>
  refreshTaskTypes(): Promise<void>
  can(permission: string): boolean
  failure(): SchedulerFailure | null
  failureTraceId(): string | null
  clearFailure(): void
  hasPendingRepair(): boolean
  repairProjection(): Promise<SchedulerCommandResult>
  readonly busy: boolean
  createDefinition(input: DefinitionInput): Promise<SchedulerCommandResult>
  updateDefinition(definition: Definition, input: DefinitionInput): Promise<SchedulerCommandResult>
  runDefinition(definition: Definition): Promise<SchedulerCommandResult>
  enableDefinition(definition: Definition): Promise<SchedulerCommandResult>
  stopDefinition(definition: Definition): Promise<SchedulerCommandResult>
  deleteDefinition(definition: Definition): Promise<SchedulerCommandResult>
}

export const createSchedulerController = (client: SchedulerClient, capabilities: SchedulerCapabilityPort, confirm: (count: number) => Promise<boolean>): SchedulerController => {
  let taskTypes: ReadonlyArray<TaskType> = []
  let taskTypesVisible = false
  let definitionsVisible = false
  let executionsVisible = false
  let failure: SchedulerFailure | null = null
  let failureTraceId: string | null = null
  let mutationBusy = false
  let repairBusy = false
  let taskSequence = 0
  let definitionSequence = 0
  let executionSequence = 0
  const pendingRepairs = new Map<string, () => Promise<void>>()
  const clearFailure = () => { failure = null; failureTraceId = null }
  const hideAll = () => { taskTypes = []; taskTypesVisible = false; definitionsVisible = false; executionsVisible = false; rawDefinitions.clearSelection(); rawExecutions.clearSelection() }
  const recordFailure = (error: unknown) => { const category = error instanceof SchedulerRequestError ? error.category : 'unavailable'; failure = isFailure(category) ? category : 'unavailable'; failureTraceId = error instanceof SchedulerRequestError ? error.traceId ?? null : null }
  const canManage = (permission: string) => {
    if (capabilities.scope() !== 'all') { hideAll(); return false }
    const allowed = capabilities.can(permission)
    if (!allowed && permission === 'scheduler.definitions.read') { taskTypes = []; taskTypesVisible = false; definitionsVisible = false }
    if (!allowed && permission === 'scheduler.executions.read') executionsVisible = false
    return allowed
  }
  const rawDefinitions = createListController<DefinitionFilters, Definition, string>({
    initialFilters: () => ({ search: '' }), normalizeFilters: filters => ({ search: filters.search.trim() }),
    validate: request => { if (!validSchedulerSearch(request.filters.search)) { definitionSequence += 1; failure = 'validation'; throw new SchedulerRequestError('validation') } },
    rowKey: row => row.id,
    load: async ({ filters, page, pageSize }) => { const sequence = ++definitionSequence; clearFailure(); try { if (!canManage('scheduler.definitions.read')) throw new SchedulerRequestError('forbidden'); const result = await client.listDefinitions(filters.search, page, pageSize); if (sequence === definitionSequence) definitionsVisible = true; return result } catch (error) { if (sequence === definitionSequence) { definitionsVisible = false; recordFailure(error) }; throw error } },
  })
  const rawExecutions = createListController<ExecutionFilters, Execution, string>({
    initialFilters: () => ({ definitionId: '', status: '' }), normalizeFilters: filters => ({ definitionId: filters.definitionId.trim(), status: filters.status }),
    rowKey: row => row.id,
    load: async ({ filters, page, pageSize }) => { const sequence = ++executionSequence; clearFailure(); try { if (!canManage('scheduler.executions.read')) throw new SchedulerRequestError('forbidden'); const result = await client.listExecutions(filters.definitionId, filters.status, page, pageSize); if (sequence === executionSequence) executionsVisible = true; return result } catch (error) { if (sequence === executionSequence) { executionsVisible = false; recordFailure(error) }; throw error } },
  })
  const definitions = failClosedList(rawDefinitions, () => definitionsVisible && canManage('scheduler.definitions.read'))
  const executions = failClosedList(rawExecutions, () => executionsVisible && canManage('scheduler.executions.read'))
  const refreshTaskTypes = async () => {
    const sequence = ++taskSequence; clearFailure()
    try { if (!canManage('scheduler.definitions.read')) throw new SchedulerRequestError('forbidden'); const values = await client.taskTypes(); if (sequence !== taskSequence) return; taskTypes = [...values]; taskTypesVisible = true }
    catch (error) { if (sequence === taskSequence) { taskTypes = []; taskTypesVisible = false; recordFailure(error) }; throw error }
  }
  const refreshDefinitions = () => definitions.refresh()
  const command = async (key: string, permission: string, operation: () => Promise<void>, options: { destructive?: boolean; valid?: boolean } = {}): Promise<SchedulerCommandResult> => {
    if (mutationBusy || repairBusy) return 'busy'
    if (pendingRepairs.size > 0) return 'refresh-failed'
    if (options.valid === false) { clearFailure(); return 'invalid' }
    if (!canManage(permission)) { failure = 'forbidden'; failureTraceId = null; return 'failed' }
    mutationBusy = true; clearFailure()
    try {
      if (options.destructive) { if (!await confirm(1)) return 'cancelled'; if (!canManage(permission)) { failure = 'forbidden'; failureTraceId = null; return 'failed' } }
      try { await operation() } catch (error) { recordFailure(error); if (failure === 'relogin' || failure === 'forbidden' || failure === 'unavailable') { taskSequence += 1; definitionSequence += 1; executionSequence += 1; hideAll() }; return 'failed' }
      pendingRepairs.set(key, refreshDefinitions)
      try { await refreshDefinitions(); pendingRepairs.delete(key); return 'completed' } catch (error) { recordFailure(error); return 'refresh-failed' }
    } finally { mutationBusy = false }
  }
  return {
    definitions, executions,
    taskTypes: () => taskTypesVisible && canManage('scheduler.definitions.read') ? [...taskTypes] : [],
    refreshTaskTypes, can: canManage, failure: () => failure, failureTraceId: () => failureTraceId, clearFailure,
    hasPendingRepair: () => pendingRepairs.size > 0,
    async repairProjection() { if (repairBusy || mutationBusy) return 'busy'; const pending = pendingRepairs.entries().next().value as [string, () => Promise<void>] | undefined; if (!pending) return 'completed'; repairBusy = true; clearFailure(); try { try { await pending[1](); pendingRepairs.delete(pending[0]); return 'completed' } catch (error) { recordFailure(error); return 'refresh-failed' } } finally { repairBusy = false } },
    get busy() { return mutationBusy || repairBusy },
    createDefinition(input) { return command(`create:${input.name}`, 'scheduler.definitions.write', async () => { await client.createDefinition(input) }, { valid: validDefinitionInput(input, taskTypes) }) },
    updateDefinition(definition, input) { return command(`update:${definition.id}`, 'scheduler.definitions.write', async () => { await client.updateDefinition(definition.id, definition.revision, input) }, { valid: !definition.enabled && validDefinitionInput(input, taskTypes) }) },
    runDefinition(definition) { return command(`run:${definition.id}`, 'scheduler.definitions.write', async () => { await client.runDefinition(definition.id, definition.revision) }) },
    enableDefinition(definition) { return command(`enable:${definition.id}`, 'scheduler.definitions.write', async () => { await client.enableDefinition(definition.id, definition.revision) }, { valid: !definition.enabled }) },
    stopDefinition(definition) { return command(`stop:${definition.id}`, 'scheduler.definitions.write', async () => { await client.stopDefinition(definition.id, definition.revision) }, { valid: definition.enabled }) },
    deleteDefinition(definition) { return command(`delete:${definition.id}`, 'scheduler.definitions.delete', () => client.deleteDefinition(definition.id, definition.revision), { destructive: true, valid: definition.id.length > 0 }) },
  }
}

const failClosedList = <F extends object, R, K>(raw: ListController<F, R, K>, visible: () => boolean): ListController<F, R, K> => ({
  snapshot: () => { const value = raw.snapshot(); return visible() ? value : { ...value, rows: [], total: 0, selectedKeys: [] } },
  refresh: () => raw.refresh(), search: filters => raw.search(filters), reset: () => raw.reset(), setPage: page => raw.setPage(page), setPageSize: pageSize => raw.setPageSize(pageSize), setSort: sort => raw.setSort(sort),
  select: rows => { if (visible()) raw.select(rows) }, clearSelection: () => raw.clearSelection(),
})
const isFailure = (value: string): value is SchedulerFailure => ['relogin', 'forbidden', 'validation', 'not-found', 'conflict', 'unavailable'].includes(value)
export const settleSchedulerPageOperation = async <T>(operation: () => Promise<T>, settled: () => void): Promise<T | undefined> => {
  try { return await operation() }
  catch { return undefined /* Controller owns stable failure classification. */ }
  finally { settled() }
}
