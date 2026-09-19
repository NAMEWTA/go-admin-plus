import { describe, expect, it } from 'vitest'
import { auditFixture } from './fixture'

describe('Audit E2E cleanup fixture', () => {
  it('deletes the expired operation while preserving both login facts', () => {
    const operation = Date.parse(auditFixture.operationOccurredAt)
    const before = Date.parse(auditFixture.cleanupBefore)
    const cutoff = Date.parse(auditFixture.retentionCutoff)

    expect(Number.isFinite(operation) && Number.isFinite(before) && Number.isFinite(cutoff)).toBe(true)
    expect(operation).toBeLessThan(before)
    expect(before).toBeLessThanOrEqual(cutoff)
    expect(auditFixture.initialFactCount - auditFixture.postCleanupFactCount).toBe(0)
  })
})
