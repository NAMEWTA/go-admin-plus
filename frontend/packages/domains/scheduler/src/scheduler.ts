import type { components } from './generated/schema'

export type Definition = components['schemas']['Definition']
export type DefinitionInput = components['schemas']['DefinitionInput']
export type DefinitionPage = components['schemas']['DefinitionPage']
export type Execution = components['schemas']['Execution']
export type ExecutionPage = components['schemas']['ExecutionPage']
export type ExecutionStatus = components['schemas']['ExecutionStatus']
export type ParameterField = components['schemas']['ParameterField']
export type Schedule = components['schemas']['Schedule']
export type TaskType = components['schemas']['TaskType']

export interface SchedulerClient {
  taskTypes(): Promise<ReadonlyArray<TaskType>>
  listDefinitions(search: string, page: number, pageSize: number): Promise<DefinitionPage>
  createDefinition(input: DefinitionInput): Promise<Definition>
  updateDefinition(id: string, revision: number, input: DefinitionInput): Promise<Definition>
  runDefinition(id: string, revision: number): Promise<{ executionId: string }>
  enableDefinition(id: string, revision: number): Promise<Definition>
  stopDefinition(id: string, revision: number): Promise<Definition>
  deleteDefinition(id: string, revision: number): Promise<void>
  listExecutions(definitionId: string, status: ExecutionStatus | '', page: number, pageSize: number): Promise<ExecutionPage>
}

export class SchedulerRequestError extends Error {
  readonly category: string
  readonly traceId?: string
  constructor(category: string, traceId: string | null = null) {
    super('Scheduler request failed')
    this.name = 'SchedulerRequestError'
    this.category = category
    if (traceId !== null) this.traceId = traceId
  }
}

export const codePointLength = (value: string) => Array.from(value).length
export const validSchedule = (value: Schedule): boolean => {
  if (typeof value.cron !== 'string' || value.cron.length > 256 || value.cron.trim().split(/\s+/).length !== 5 || !/^[0-9*,/\sA-Za-z-]+$/.test(value.cron)) return false
  try { new Intl.DateTimeFormat('zh-CN', { timeZone: value.timezone }); return value.timezone.length > 0 } catch { return false }
}

export const validDefinitionInput = (value: DefinitionInput, taskTypes: ReadonlyArray<TaskType>): boolean => {
  const name = value.name.trim()
  const task = taskTypes.find(candidate => candidate.key === value.taskType)
  if (codePointLength(name) < 1 || codePointLength(name) > 100 || hasControl(name) || !task || !validSchedule(value.schedule)) return false
  if (typeof value.parameters !== 'object' || value.parameters === null || Array.isArray(value.parameters)) return false
  const keys = Object.keys(value.parameters).sort()
  const fields = task.fields.map(field => field.name).sort()
  if (keys.length !== fields.length || keys.some((key, index) => key !== fields[index])) return false
  return task.fields.every(field => validParameter(value.parameters[field.name], field))
}

const validParameter = (value: string | number | boolean | undefined, field: ParameterField): boolean => {
  if (value === undefined) return !field.required
  if (field.kind === 'string') return typeof value === 'string' && codePointLength(value) <= 256 && (!field.allowedValues || field.allowedValues.includes(value))
  if (field.kind === 'integer') return typeof value === 'number' && Number.isSafeInteger(value) && (field.minimum === undefined || value >= field.minimum) && (field.maximum === undefined || value <= field.maximum)
  return field.kind === 'boolean' && typeof value === 'boolean'
}

const hasControl = (value: string) => /\p{Cc}/u.test(value)

export const validSchedulerSearch = (value: string): boolean => codePointLength(value) <= 100 && !hasControl(value)
