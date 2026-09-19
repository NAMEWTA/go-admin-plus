// @vitest-environment happy-dom

import { shallowMount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'

import {
  ADMIN_THEME_STORAGE_KEY,
  createFormController,
  createListController,
  createRemovalController,
  createThemeController
} from '@go-admin-plus/ui'
import {
  AppPage,
  DataTable,
  FormDialog,
  FormGrid,
  Pagination,
  QueryBar,
  StatusTag,
  TableToolbar
} from '@go-admin-plus/ui/components'

const deferred = <T>() => {
  let resolve!: (value: T) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((done, fail) => {
    resolve = done
    reject = fail
  })
  return { promise, reject, resolve }
}

describe('shared theme preference', () => {
  it('persists an explicit theme and compact density on the root element', () => {
    const stored = new Map<string, string>()
    const attributes = new Map<string, string>()
    const theme = createThemeController({
      media: { matches: false, subscribe: () => () => undefined },
      root: {
        removeAttribute: (name) => attributes.delete(name),
        setAttribute: (name, value) => attributes.set(name, value)
      },
      storage: {
        getItem: (key) => stored.get(key) ?? null,
        setItem: (key, value) => stored.set(key, value)
      }
    })

    theme.setPreference('dark')
    theme.setDensity('compact')

    expect(theme.snapshot()).toEqual({ density: 'compact', preference: 'dark', resolved: 'dark' })
    expect(attributes).toEqual(new Map([['data-theme', 'dark'], ['data-density', 'compact']]))
    expect(JSON.parse(stored.get(ADMIN_THEME_STORAGE_KEY) ?? '')).toEqual({ density: 'compact', preference: 'dark' })
  })

  it('follows system changes only while the system preference is selected', () => {
    let listener: ((dark: boolean) => void) | undefined
    const attributes = new Map<string, string>()
    const theme = createThemeController({
      media: {
        matches: false,
        subscribe: (next) => {
          listener = next
          return () => { listener = undefined }
        }
      },
      root: {
        removeAttribute: (name) => attributes.delete(name),
        setAttribute: (name, value) => attributes.set(name, value)
      },
      storage: { getItem: () => null, setItem: () => undefined }
    })

    listener?.(true)
    expect(theme.snapshot().resolved).toBe('dark')
    theme.setPreference('light')
    listener?.(true)
    expect(theme.snapshot().resolved).toBe('light')
    expect(attributes.get('data-theme')).toBe('light')
    theme.destroy()
    theme.destroy()
    expect(listener).toBeUndefined()
  })

  it('still applies preferences when persistent storage is unavailable', () => {
    const attributes = new Map<string, string>()
    const theme = createThemeController({
      media: { matches: false, subscribe: () => () => undefined },
      root: {
        removeAttribute: (name) => attributes.delete(name),
        setAttribute: (name, value) => attributes.set(name, value)
      },
      storage: {
        getItem: () => { throw new Error('storage denied') },
        setItem: () => { throw new Error('storage denied') }
      }
    })

    expect(() => theme.setPreference('dark')).not.toThrow()
    expect(theme.snapshot().resolved).toBe('dark')
    expect(attributes.get('data-theme')).toBe('dark')
  })
})

describe('shared management components', () => {
  it('exposes keyboard search and explicit reset commands', async () => {
    const wrapper = shallowMount(QueryBar, {
      props: { busy: false },
      global: { renderStubDefaultSlot: true }
    })

    await wrapper.get('form[role="search"]').trigger('submit')
    expect(wrapper.emitted('search')).toHaveLength(1)

    const buttons = wrapper.findAllComponents({ name: 'ElButton' })
    expect(buttons.map((button) => button.text())).toEqual(['重置', '查询'])
    await buttons[0]?.trigger('click')
    expect(wrapper.emitted('reset')).toHaveLength(1)
  })

  it('keeps page errors and busy states explicit', () => {
    const error = shallowMount(AppPage, { props: { title: 'Very long administration workspace title', error: '加载失败' } })
    expect(error.get('h1').text()).toContain('Very long administration workspace title')
    expect(error.findComponent({ name: 'ElAlert' }).props()).toMatchObject({ closable: false, showIcon: true, type: 'error' })

    const busy = shallowMount(AppPage, { props: { title: '账号', busy: true } })
    expect(busy.attributes('aria-busy')).toBe('true')
    expect(busy.findComponent({ name: 'ElSkeleton' }).exists()).toBe(true)

    const empty = shallowMount(AppPage, { props: { title: '账号' } })
    expect(empty.findComponent({ name: 'ElEmpty' }).props('description')).toBe('暂无内容')
  })

  it('gives icon tools accessible names and stable selection context', async () => {
    const wrapper = shallowMount(TableToolbar, {
      props: { selectedCount: 3 },
      global: { renderStubDefaultSlot: true }
    })
    expect(wrapper.text()).toContain('已选择 3 项')
    const refresh = wrapper.findComponent({ name: 'ElButton' })
    expect(refresh.attributes('aria-label')).toBe('刷新列表')
    await refresh.trigger('click')
    expect(wrapper.emitted('refresh')).toHaveLength(1)
  })

  it('delegates focus trapping and Escape behavior to Element Plus dialogs', () => {
    const regular = shallowMount(FormDialog, { props: { modelValue: true, title: '编辑账号' } })
    expect(regular.findComponent({ name: 'ElDialog' }).props()).toMatchObject({
      alignCenter: true,
      closeOnClickModal: false,
      closeOnPressEscape: true,
      destroyOnClose: true,
      modelValue: true
    })

    const busy = shallowMount(FormDialog, { props: { modelValue: true, title: '编辑账号', busy: true } })
    expect(busy.findComponent({ name: 'ElDialog' }).props()).toMatchObject({ closeOnPressEscape: false, showClose: false })
  })

  it('represents status with an icon and text instead of color alone', () => {
    const wrapper = shallowMount(StatusTag, {
      props: { label: '已启用', tone: 'success' },
      global: { renderStubDefaultSlot: true }
    })
    expect(wrapper.text()).toContain('已启用')
    expect(wrapper.find('anonymous-stub[aria-hidden="true"]').exists()).toBe(true)
    expect(wrapper.findComponent({ name: 'ElTag' }).props('type')).toBe('success')
  })

  it('renders stable table, grid, and pagination state contracts', () => {
    const loading = shallowMount(DataTable, { props: { rows: [], loading: true } })
    expect(loading.attributes('aria-busy')).toBe('true')
    expect(loading.findComponent({ name: 'ElSkeleton' }).exists()).toBe(true)

    const failed = shallowMount(DataTable, { props: { rows: [], error: '无法读取数据' } })
    expect(failed.findComponent({ name: 'ElAlert' }).props()).toMatchObject({ closable: false, type: 'error' })

    const grid = shallowMount(FormGrid, { props: { columns: 3 } })
    expect(grid.attributes('style')).toContain('--ga-form-columns: 3')

    const pagination = shallowMount(Pagination, { props: { page: 1, pageSize: 20, total: 52 } })
    expect(pagination.get('nav').attributes('aria-label')).toBe('分页')
    expect(pagination.findComponent({ name: 'ElPagination' }).props()).toMatchObject({ currentPage: 1, pageSize: 20, total: 52 })
  })
})

describe('shared list interaction', () => {
  it('searches from page one and keeps the selected page when refreshing', async () => {
    const load = vi.fn(async ({ filters, page, pageSize }: {
      filters: { name: string }
      page: number
      pageSize: number
    }) => ({ rows: [{ id: `${filters.name}-${page}` }], total: pageSize }))
    const list = createListController({
      initialFilters: () => ({ name: '' }),
      load,
      pageSize: 20,
      rowKey: (row: { id: string }) => row.id
    })

    await list.setPage(3)
    await list.search({ name: 'Ada' })
    expect(load).toHaveBeenLastCalledWith({ filters: { name: 'Ada' }, page: 1, pageSize: 20 })

    await list.setPage(2)
    await list.refresh()
    expect(load).toHaveBeenLastCalledWith({ filters: { name: 'Ada' }, page: 2, pageSize: 20 })
  })

  it('ignores a stale successful request after the latest result is visible', async () => {
    const first = deferred<{ rows: Array<{ id: string }>, total: number }>()
    const second = deferred<{ rows: Array<{ id: string }>, total: number }>()
    const load = vi.fn()
      .mockReturnValueOnce(first.promise)
      .mockReturnValueOnce(second.promise)
    const list = createListController({
      initialFilters: () => ({ name: '' }),
      load,
      rowKey: (row: { id: string }) => row.id
    })

    const staleRequest = list.search({ name: 'old' })
    const latestRequest = list.search({ name: 'new' })
    second.resolve({ rows: [{ id: 'new' }], total: 1 })
    await latestRequest
    first.resolve({ rows: [{ id: 'old' }], total: 99 })
    await expect(staleRequest).resolves.toBeUndefined()

    expect(list.snapshot()).toMatchObject({
      filters: { name: 'new' },
      loading: false,
      rows: [{ id: 'new' }],
      total: 1
    })
  })

  it('silently discards a stale rejection without changing the latest state', async () => {
    const first = deferred<{ rows: Array<{ id: string }>, total: number }>()
    const second = deferred<{ rows: Array<{ id: string }>, total: number }>()
    const load = vi.fn()
      .mockReturnValueOnce(first.promise)
      .mockReturnValueOnce(second.promise)
    const list = createListController({
      initialFilters: () => ({ name: '' }),
      load,
      rowKey: (row: { id: string }) => row.id
    })

    const staleRequest = list.search({ name: 'old' })
    const latestRequest = list.search({ name: 'new' })
    second.resolve({ rows: [{ id: 'new' }], total: 1 })
    await latestRequest
    first.reject(new Error('stale request failed'))
    await expect(staleRequest).resolves.toBeUndefined()

    expect(list.snapshot()).toMatchObject({
      filters: { name: 'new' },
      loading: false,
      rows: [{ id: 'new' }],
      total: 1
    })
  })

  it('normalizes before transport and keeps the last successful state when validation cancels an older request', async () => {
    const pending = deferred<{ rows: Array<{ id: string }>, total: number }>()
    const load = vi.fn()
      .mockResolvedValueOnce({ rows: [{ id: 'stable' }], total: 1 })
      .mockReturnValueOnce(pending.promise)
    const list = createListController({
      initialFilters: () => ({ name: '' }),
      load,
      normalizeFilters: filters => ({ name: filters.name.trim() }),
      validate: request => {
        if (request.filters.name.length > 4) throw new Error('invalid filters')
      },
      rowKey: (row: { id: string }) => row.id
    })

    await list.search({ name: ' ok ' })
    list.select(list.snapshot().rows)
    const stale = list.search({ name: 'next' })
    await expect(list.search({ name: 'invalid' })).rejects.toThrow('invalid filters')
    expect(load).toHaveBeenCalledTimes(2)
    expect(list.snapshot()).toMatchObject({
      filters: { name: 'ok' },
      loading: false,
      rows: [{ id: 'stable' }],
      selectedKeys: ['stable'],
      total: 1
    })
    pending.resolve({ rows: [{ id: 'stale' }], total: 99 })
    await stale
    expect(list.snapshot()).toMatchObject({ filters: { name: 'ok' }, rows: [{ id: 'stable' }], total: 1 })
  })

  for (const staleOutcome of ['resolve', 'reject'] as const) {
    it(`clears loading after normalization throws and the stale request later ${staleOutcome}s`, async () => {
      const pending = deferred<{ rows: Array<{ id: string }>, total: number }>()
      const load = vi.fn()
        .mockResolvedValueOnce({ rows: [{ id: 'stable' }], total: 1 })
        .mockReturnValueOnce(pending.promise)
      const list = createListController({
        initialFilters: () => ({ name: '' }),
        load,
        normalizeFilters: filters => {
          if (filters.name === 'explode') throw new Error('normalization failed')
          return { name: filters.name.trim() }
        },
        rowKey: (row: { id: string }) => row.id
      })

      await list.search({ name: 'stable' })
      const stale = list.search({ name: 'pending' })
      await expect(list.search({ name: 'explode' })).rejects.toThrow('normalization failed')
      expect(list.snapshot()).toMatchObject({ filters: { name: 'stable' }, loading: false, rows: [{ id: 'stable' }], total: 1 })
      if (staleOutcome === 'resolve') pending.resolve({ rows: [{ id: 'stale' }], total: 99 })
      else pending.reject(new Error('stale request failed'))
      await expect(stale).resolves.toBeUndefined()
      expect(list.snapshot()).toMatchObject({ filters: { name: 'stable' }, loading: false, rows: [{ id: 'stable' }], total: 1 })
    })
  }
})

describe('shared form interaction', () => {
  it('does not send a command when validation fails', async () => {
    const submit = vi.fn()
    const form = createFormController({
      validate: async () => false,
      submit
    })

    await expect(form.run({ name: '' })).resolves.toBe('invalid')
    expect(submit).not.toHaveBeenCalled()
  })

  it('guards validation and submission as one in-flight operation', async () => {
    const gate = deferred<boolean>()
    const submit = vi.fn(async () => undefined)
    const form = createFormController({ validate: () => gate.promise, submit })

    const first = form.run({ name: 'Ada' })
    await expect(form.run({ name: 'Ada' })).resolves.toBe('busy')
    gate.resolve(true)
    await expect(first).resolves.toBe('submitted')
    expect(submit).toHaveBeenCalledTimes(1)
  })

  it('refreshes once after a successful command', async () => {
    const refreshed = vi.fn(async () => undefined)
    const form = createFormController({
      validate: async () => true,
      submit: async () => undefined,
      submitted: refreshed
    })

    await expect(form.run({ name: 'Ada' })).resolves.toBe('submitted')
    expect(refreshed).toHaveBeenCalledTimes(1)
  })

  it('reports refresh failure separately after writing exactly once', async () => {
    const submit = vi.fn(async () => undefined)
    const failed = vi.fn(async () => undefined)
    const form = createFormController({
      validate: async () => true,
      submit,
      submitted: async () => { throw new Error('refresh failed') },
      failed
    })

    await expect(form.run({ name: 'Ada' })).resolves.toBe('refresh-failed')
    expect(submit).toHaveBeenCalledTimes(1)
    expect(failed).not.toHaveBeenCalled()
  })
})

describe('shared destructive interaction', () => {
  it('confirms once, writes once, then refreshes and clears selection', async () => {
    const confirmation = deferred<boolean>()
    const confirm = vi.fn(() => confirmation.promise)
    const execute = vi.fn(async () => undefined)
    const refreshed = vi.fn(async () => undefined)
    const clearSelection = vi.fn()
    const removal = createRemovalController({ confirm, execute, refreshed, clearSelection })

    const first = removal.run(['user-1'])
    await expect(removal.run(['user-1'])).resolves.toBe('busy')
    confirmation.resolve(true)
    await expect(first).resolves.toBe('completed')

    expect(confirm).toHaveBeenCalledTimes(1)
    expect(execute).toHaveBeenCalledTimes(1)
    expect(refreshed).toHaveBeenCalledTimes(1)
    expect(clearSelection).toHaveBeenCalledTimes(1)
  })

  it('does not write or refresh when confirmation is cancelled', async () => {
    const execute = vi.fn()
    const refreshed = vi.fn()
    const removal = createRemovalController({
      confirm: async () => false,
      execute,
      refreshed,
      clearSelection: vi.fn()
    })

    await expect(removal.run(['user-1'])).resolves.toBe('cancelled')
    expect(execute).not.toHaveBeenCalled()
    expect(refreshed).not.toHaveBeenCalled()
  })

  it('reports post-write UI failure separately without repeating the write', async () => {
    const execute = vi.fn(async () => undefined)
    const refreshed = vi.fn(async () => { throw new Error('refresh failed') })
    const failed = vi.fn(async () => undefined)
    const removal = createRemovalController({
      confirm: async () => true,
      execute,
      refreshed,
      clearSelection: vi.fn(),
      failed
    })

    await expect(removal.run(['user-1'])).resolves.toBe('refresh-failed')
    expect(execute).toHaveBeenCalledTimes(1)
    expect(refreshed).toHaveBeenCalledTimes(1)
    expect(failed).not.toHaveBeenCalled()
  })

  it('still refreshes after selection cleanup fails following a successful write', async () => {
    const execute = vi.fn(async () => undefined)
    const refreshed = vi.fn(async () => undefined)
    const failed = vi.fn(async () => undefined)
    const removal = createRemovalController({
      confirm: async () => true,
      execute,
      refreshed,
      clearSelection: () => { throw new Error('selection cleanup failed') },
      failed
    })

    await expect(removal.run(['user-1'])).resolves.toBe('refresh-failed')
    expect(execute).toHaveBeenCalledTimes(1)
    expect(refreshed).toHaveBeenCalledTimes(1)
    expect(failed).not.toHaveBeenCalled()
  })
})
