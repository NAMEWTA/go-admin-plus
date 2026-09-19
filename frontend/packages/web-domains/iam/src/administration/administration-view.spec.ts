import { describe, expect, it } from 'vitest'
import pageSource from './AdministrationPage.vue?raw'
import { administrationViewForPath } from './administration-view'

describe('administrationViewForPath', () => {
  it('derives the IAM management view only from an exact product route', () => {
    expect(administrationViewForPath('/iam/users')).toBe('users')
    expect(administrationViewForPath('/iam/roles')).toBe('roles')
    expect(administrationViewForPath('/iam/menus')).toBe('menus')
    expect(administrationViewForPath('/iam/users/')).toBeNull()
    expect(administrationViewForPath('/iam/unknown')).toBeNull()
  })

  it('keeps the rendered view route-derived without local tab navigation', () => {
    expect(pageSource).toContain('useRoute')
    expect(pageSource).toContain('administrationViewForPath(route.path)')
    expect(pageSource).not.toContain("const tab = ref")
    expect(pageSource).not.toContain('switchTab')
    expect(pageSource).not.toContain('class="tabs"')
  })

  it('renders classic scope and asynchronous deletion controls', () => {
    for (const scope of ['all', 'self']) {
      expect(pageSource).toContain(`value="${scope}"`)
    }
    for (const contract of [
      'startUserDeletion',
      'purgeConfirmed',
      'refreshUserDeletion',
      'cancelUserDeletion',
      "deletion?.status === 'queued'",
    ]) expect(pageSource).toContain(contract)
    expect(pageSource).toContain('value="transfer"')
    expect(pageSource).toContain('value="purge"')
  })
})
