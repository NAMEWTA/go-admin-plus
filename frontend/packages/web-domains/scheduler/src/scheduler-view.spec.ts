import { describe, expect, it } from 'vitest'
import pageSource from './SchedulerPage.vue?raw'
import { schedulerViewForPath } from './scheduler-view'

describe('schedulerViewForPath', () => {
  it('maps only exact scheduler routes', () => {
    expect(schedulerViewForPath('/scheduler/definitions')).toBe('definitions')
    expect(schedulerViewForPath('/scheduler/executions')).toBe('executions')
    expect(schedulerViewForPath('/scheduler/definitions/')).toBeNull()
    expect(schedulerViewForPath('/scheduler/unknown')).toBeNull()
  })

  it('keeps the page route-derived without local tab navigation', () => {
    expect(pageSource).toContain('useRoute')
    expect(pageSource).toContain('schedulerViewForPath(route.path)')
    expect(pageSource).not.toContain("tab = ref<'definitions' | 'executions'>")
    expect(pageSource).not.toContain('class="tabs"')
  })
})
