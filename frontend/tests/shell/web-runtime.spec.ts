import { describe, expect, it, vi } from 'vitest'

import { createWebRuntime } from '@go-admin-plus/adapter-browser'

const response = (status: number, value?: unknown): Response => ({
  ok: status >= 200 && status < 300,
  status,
  json: async () => value
} as Response)

describe('web runtime adapter', () => {
  it('uses cookie credentials without accepting or returning a raw session', async () => {
    const fetch = vi.fn(async () => response(200, {
      kind: 'authenticated',
      subjectId: 'user-1',
      permissions: ['files.objects.read'],
      dataScope: 'self'
    }))
    const runtime = createWebRuntime(fetch as typeof globalThis.fetch)

    await expect(runtime.loadIdentity()).resolves.toEqual({
      kind: 'authenticated',
      subjectId: 'user-1',
      permissions: ['files.objects.read'],
      dataScope: 'self'
    })
    expect(fetch).toHaveBeenCalledWith('/api/runtime/identity', {
      credentials: 'include',
      headers: { accept: 'application/json' }
    })
    expect(JSON.stringify(await runtime.loadIdentity())).not.toMatch(/session|secret|token/i)
  })

  it('maps 401 to unauthenticated and rejects malformed runtime data', async () => {
    await expect(createWebRuntime(async () => response(401) as never).loadIdentity())
      .resolves.toEqual({ kind: 'unauthenticated' })
    await expect(createWebRuntime(async () => response(200, { kind: 'authenticated' }) as never).loadIdentity())
      .rejects.toThrow('invalid identity response')
  })

  it('accepts only the exact unauthenticated identity shape', async () => {
    const runtime = createWebRuntime(async () => response(200, { kind: 'unauthenticated' }) as never)
    await expect(runtime.loadIdentity()).resolves.toEqual({ kind: 'unauthenticated' })

    for (const extra of ['sessionToken', 'token', 'secret', 'unexpected']) {
      const unsafe = createWebRuntime(async () => response(200, {
        kind: 'unauthenticated',
        [extra]: 'sensitive'
      }) as never)
      await expect(unsafe.loadIdentity()).rejects.toThrow('invalid identity response')
    }
  })

  it('accepts only the exact authenticated identity shape', async () => {
    const valid = {
      kind: 'authenticated',
      subjectId: 'user-1',
      permissions: ['files.objects.read'],
      dataScope: 'all'
    }
    await expect(createWebRuntime(async () => response(200, valid) as never).loadIdentity())
      .resolves.toEqual(valid)

    for (const extra of ['sessionToken', 'token', 'secret', 'unexpected']) {
      const unsafe = createWebRuntime(async () => response(200, {
        ...valid,
        [extra]: 'sensitive'
      }) as never)
      await expect(unsafe.loadIdentity()).rejects.toThrow('invalid identity response')
    }
  })

  it('rejects unsafe, duplicate, and malformed navigation entries', async () => {
    const unsafe = createWebRuntime(async () => response(200, [{ path: '//outside.example' }]) as never)
    await expect(unsafe.loadNavigation()).rejects.toThrow('invalid navigation entry')

    const duplicate = createWebRuntime(async () => response(200, [
      { path: '/files', permission: 'files.objects.read' },
      { path: '/files', permission: 'files.objects.write' }
    ]) as never)
    await expect(duplicate.loadNavigation()).rejects.toThrow('duplicate navigation path')

    const remoteComponent = createWebRuntime(async () => response(200, [{
      path: '/files',
      permission: 'files.objects.read',
      component: 'https://outside.example/remote.js'
    }]) as never)
    await expect(remoteComponent.loadNavigation()).resolves.toEqual([{
      path: '/files',
      permission: 'files.objects.read'
    }])
    expect(JSON.stringify(await remoteComponent.loadNavigation())).not.toContain('component')
  })
})
