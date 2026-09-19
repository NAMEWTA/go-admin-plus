import { describe, expect, it } from 'vitest'
import { validDefinitionInput, validSchedule } from './scheduler'

const taskTypes = [{ key: 'reports.daily', label: 'Daily report', fields: [
  { name: 'name', label: 'Name', kind: 'string' as const, required: true, allowedValues: ['sales'] },
  { name: 'limit', label: 'Limit', kind: 'integer' as const, required: true, minimum: 1, maximum: 10 },
] }]

describe('scheduler model validation', () => {
  it('accepts structured UTC schedules and rejects ambiguous day selectors', () => {
    expect(validSchedule({ cron: '0 1 * * *', timezone: 'UTC' })).toBe(true)
    expect(validSchedule({ cron: '* * *', timezone: 'UTC' })).toBe(false)
    expect(validSchedule({ cron: '* * * * *', timezone: 'invalid/zone' })).toBe(false)
  })

  it('validates the selected typed registry descriptor without extra parameters', () => {
    const base = { name: 'Sales', taskType: 'reports.daily', schedule: { cron: '0 1 * * *', timezone: 'UTC' }, parameters: { name: 'sales', limit: 5 } }
    expect(validDefinitionInput(base, taskTypes)).toBe(true)
    expect(validDefinitionInput({ ...base, parameters: { ...base.parameters, secret: 'x' } }, taskTypes)).toBe(false)
    expect(validDefinitionInput({ ...base, parameters: { name: 'sales', limit: 11 } }, taskTypes)).toBe(false)
    expect(validDefinitionInput({ ...base, parameters: { name: 'sales', limit: Number.MAX_SAFE_INTEGER + 1 } }, taskTypes)).toBe(false)
  })
})
