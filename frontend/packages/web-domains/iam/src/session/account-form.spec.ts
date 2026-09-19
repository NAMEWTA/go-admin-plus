import { describe, expect, it } from 'vitest'

import { passwordsMatch } from './account-form'

describe('account password form', () => {
  it('requires the confirmation to match the new password exactly', () => {
    expect(passwordsMatch('new-sensitive-value', 'new-sensitive-value')).toBe(true)
    expect(passwordsMatch('new-sensitive-value', 'different-sensitive-value')).toBe(false)
  })
})
