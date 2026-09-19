import { describe, expect, it } from 'vitest'
import { AuditRequestError, type AuditFact } from './audit'

describe('audit domain contract', () => {
  it('exposes redacted facts and fixed public failures only', () => {
    const fact: AuditFact = {
      id: 'audit-domain-0001', kind: 'operation', action: 'update', outcome: 'succeeded',
      actorType: 'system', source: 'server', subject: 'files:record-1', occurredAt: '2026-08-27T00:00:00Z',
    }
    expect(Object.keys(fact)).not.toContain('payload')
    expect(Object.keys(fact)).not.toContain('businessKey')
    const error = new AuditRequestError('forbidden')
    expect(error.message).toBe('Audit request failed')
    expect(JSON.stringify(error)).toBe('{"category":"forbidden","name":"AuditRequestError"}')
  })
})
