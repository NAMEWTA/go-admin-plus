import type { components } from './generated/client'

export type AuditFact = components['schemas']['AuditFact']
export type AuditPage = components['schemas']['AuditPage']
export type AuditKind = components['schemas']['AuditKind']
export type AuditAction = components['schemas']['AuditAction']
export type AuditOutcome = components['schemas']['AuditOutcome']
export type AuditSource = components['schemas']['AuditSource']
export type CleanupResult = components['schemas']['CleanupAuditResponse']

export interface AuditFilters {
  readonly kind?: AuditKind
  readonly action?: AuditAction
  readonly outcome?: AuditOutcome
  readonly source?: AuditSource
  readonly from?: string
  readonly to?: string
}

export interface AuditListRequest {
  readonly filters: AuditFilters
  readonly page: number
  readonly pageSize: number
}

export interface AuditClient {
  list(request: AuditListRequest): Promise<AuditPage>
  detail(id: string): Promise<AuditFact>
  cleanup(before: string): Promise<CleanupResult>
}

export type AuditFailure = 'validation' | 'forbidden' | 'conflict' | 'not-found' | 'relogin' | 'unavailable'

export class AuditRequestError extends Error {
  readonly category: AuditFailure
  readonly traceId?: string

  constructor(category: AuditFailure, traceId: string | null = null) {
    super('Audit request failed')
    this.name = 'AuditRequestError'
    this.category = category
    if (traceId !== null) this.traceId = traceId
  }
}
