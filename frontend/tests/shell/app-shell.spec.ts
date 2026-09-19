import { describe, expect, it, vi } from 'vitest'
import { createShellNavigator, resolveShellState } from '@go-admin-plus/app-shell'
import {
  createProductMemoryHistory,
  createProductRouter,
  productBreadcrumbs,
  productHistoryMode,
  productRoutesFor,
  resolveAuthorizedProductRoutes
} from '@go-admin-plus/app-shell/product'
import type { RuntimeIdentity, RuntimeRequest, ShellRuntimePort } from '@go-admin-plus/platform'

const deferred = <T>() => {
  let resolve!: (value: T) => void
  const promise = new Promise<T>(done => { resolve = done })
  return { promise, resolve }
}

const runtime = (overrides: Partial<ShellRuntimePort> = {}): ShellRuntimePort => ({
  loadIdentity: async () => ({
    kind: 'authenticated',
    subjectId: 'user-1',
    permissions: ['files.objects.read'],
    dataScope: 'all'
  }),
  loadNavigation: async () => [{ path: '/files', permission: 'files.objects.read' }],
  ...overrides
})

describe('app shell state', () => {
  it('returns a stable authenticated state for an allowed route', async () => {
    await expect(resolveShellState(runtime(), '/files')).resolves.toEqual({
      kind: 'authenticated',
      path: '/files',
      subjectId: 'user-1'
    })
  })

  it('returns login without requesting navigation for an unauthenticated identity', async () => {
    const loadNavigation = vi.fn()
    const result = await resolveShellState(runtime({
      loadIdentity: async () => ({ kind: 'unauthenticated' }),
      loadNavigation
    }), '/files')

    expect(result).toEqual({ kind: 'unauthenticated', redirectTo: '/login' })
    expect(loadNavigation).not.toHaveBeenCalled()
  })

  it('distinguishes unauthorized and unknown routes', async () => {
    await expect(resolveShellState(runtime({
      loadIdentity: async () => ({
        kind: 'authenticated',
        subjectId: 'user-2',
        permissions: [],
        dataScope: 'self'
      })
    }), '/files')).resolves.toEqual({ kind: 'unauthorized', path: '/files' })

    await expect(resolveShellState(runtime(), '/missing')).resolves.toEqual({
      kind: 'not-found',
      path: '/missing'
    })
  })

  it('contains no adapter error or credential material in failure state', async () => {
    const result = await resolveShellState(runtime({
      loadIdentity: async () => { throw new Error('secret=session-value') }
    }), '/files')

    expect(result).toEqual({ kind: 'adapter-failed', retryable: true })
    expect(JSON.stringify(result)).not.toContain('session-value')
  })

  it('commits only the latest navigation when requests resolve out of order', async () => {
    const first = deferred<RuntimeIdentity>()
    const second = deferred<RuntimeIdentity>()
    const requests: AbortSignal[] = []
    const loadIdentity = vi.fn(({ signal }: RuntimeRequest = {}) => {
      requests.push(signal as AbortSignal)
      return requests.length === 1 ? first.promise : second.promise
    })
    const commit = vi.fn()
    const setLoading = vi.fn()
    const navigator = createShellNavigator(runtime({ loadIdentity }), { commit, setLoading })

    const staleNavigation = navigator.navigate('/old')
    const latestNavigation = navigator.navigate('/files')
    expect(requests[0]?.aborted).toBe(true)
    expect(requests[1]?.aborted).toBe(false)

    second.resolve({
      kind: 'authenticated',
      subjectId: 'user-2',
      permissions: ['files.objects.read'],
      dataScope: 'all'
    })
    await latestNavigation
    first.resolve({
      kind: 'authenticated',
      subjectId: 'user-1',
      permissions: ['files.objects.read'],
      dataScope: 'self'
    })
    await staleNavigation

    expect(commit).toHaveBeenCalledTimes(1)
    expect(commit).toHaveBeenCalledWith('/files', {
      kind: 'authenticated',
      path: '/files',
      subjectId: 'user-2'
    })
    expect(setLoading.mock.calls).toEqual([[true], [true], [false]])
  })

  it('invalidates pending navigation without committing after unmount', async () => {
    const identity = deferred<RuntimeIdentity>()
    const commit = vi.fn()
    const setLoading = vi.fn()
    const navigator = createShellNavigator(runtime({ loadIdentity: () => identity.promise }), {
      commit,
      setLoading
    })

    const navigation = navigator.navigate('/files')
    navigator.invalidate()
    identity.resolve({
      kind: 'authenticated',
      subjectId: 'user-1',
      permissions: ['files.objects.read'],
      dataScope: 'all'
    })
    await navigation

    expect(commit).not.toHaveBeenCalled()
    expect(setLoading).toHaveBeenCalledTimes(1)
  })
})

describe('product router', () => {
  const productRuntime = (
    permissions: Extract<RuntimeIdentity, { kind: 'authenticated' }>['permissions'] = ['files.objects.read']
  ): ShellRuntimePort => ({
    loadIdentity: async () => ({
      kind: 'authenticated',
      subjectId: 'user-1',
      permissions,
      dataScope: 'all'
    }),
    loadNavigation: async () => [
      { path: '/files', permission: 'files.objects.read' },
      { path: '/files', permission: 'files.objects.read' }
    ]
  })

  it('uses host-specific history with one compiled business manifest', () => {
    expect(productHistoryMode('web')).toBe('html5')
    expect(productHistoryMode('desktop')).toBe('hash')
    expect(productRoutesFor('web').map(route => route.name))
      .toEqual(productRoutesFor('desktop').map(route => route.name))
  })

  it('resolves deep links and derives title hierarchy from the current route', async () => {
    const router = createProductRouter('web', productRuntime(), { history: createProductMemoryHistory() })
    await router.push('/files')
    await router.isReady()

    expect(router.currentRoute.value.name).toBe('files-objects')
    expect(productBreadcrumbs('web', router.currentRoute.value)).toEqual([
      { title: '工作台', path: '/' },
      { title: '文件管理' },
      { title: '文件管理' }
    ])
  })

  it('keeps forbidden and unknown routes distinct', async () => {
    const forbidden = createProductRouter('web', productRuntime([]), { history: createProductMemoryHistory() })
    await forbidden.push('/files')
    await forbidden.isReady()
    expect(forbidden.currentRoute.value.name).toBe('forbidden')

    const unknown = createProductRouter('web', productRuntime(), { history: createProductMemoryHistory() })
    await unknown.push('/missing')
    await unknown.isReady()
    expect(unknown.currentRoute.value.name).toBe('not-found')
  })

  it('uses a recoverable unavailable route for bounded runtime failures', async () => {
    const runtimeFailure: ShellRuntimePort = {
      loadIdentity: async () => { throw new Error('secret=adapter-detail') },
      loadNavigation: async () => []
    }
    const router = createProductRouter('web', runtimeFailure, { history: createProductMemoryHistory() })
    await router.push('/files')
    await router.isReady()
    expect(router.currentRoute.value.name).toBe('unavailable')
    expect(JSON.stringify(router.currentRoute.value)).not.toContain('adapter-detail')
  })

  it('restores the same route truth through history navigation', async () => {
    const router = createProductRouter(
      'web',
      productRuntime(['files.objects.read', 'files.objects.read']),
      { history: createProductMemoryHistory() }
    )
    await router.push('/files')
    await router.isReady()
    await router.push('/files')

    const restored = new Promise<void>(resolve => {
      const remove = router.afterEach(to => {
        if (to.name === 'files-objects') {
          remove()
          resolve()
        }
      })
    })
    router.back()
    await restored
    expect(router.currentRoute.value.path).toBe('/files')
  })

  it('intersects server navigation with compiled routes and ignores component payloads', () => {
    const identity = {
      kind: 'authenticated' as const,
      subjectId: 'user-1',
      permissions: ['files.objects.read' as const],
      dataScope: 'all' as const
    }
    const navigation = [{
      path: '/files' as const,
      permission: 'files.objects.read' as const,
      component: 'https://outside.example/remote.js'
    }]

    const allowed = resolveAuthorizedProductRoutes('web', identity, navigation)
    expect(allowed.map(route => route.name)).toEqual(['files-objects'])
    expect(allowed[0]?.component).toBe(productRoutesFor('web').find(route => route.name === 'files-objects')?.component)
  })
})
