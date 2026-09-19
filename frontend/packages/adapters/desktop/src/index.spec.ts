import { afterEach, describe, expect, it, vi } from 'vitest'

import { createDesktopFetch, createDesktopPlatform, createDesktopRuntime, createDesktopSession, createDesktopSessionClient, createDesktopTransport } from './index'

afterEach(() => { vi.unstubAllGlobals() })

describe('desktop adapter security boundary', () => {
  it('implements the explicit host capability port through bounded native commands', async () => {
    const calls: Array<readonly [string, Record<string, unknown> | undefined]> = []
    const platform = createDesktopPlatform(async <T>(command: string, args?: Record<string, unknown>) => {
      calls.push([command, args])
      if (command === 'desktop_pick_file') return {
        name: 'design.txt', mediaType: 'text/plain', sizeBytes: 3, data: 'YWJj'
      } as T
      if (command === 'desktop_save_file') return { status: 'saved' } as T
      return undefined as T
    })

    expect([...platform.listCapabilities()].sort()).toEqual([
      'clipboard-write', 'file-open', 'file-save', 'notification'
    ])
    await platform.notify('生成任务已完成')
    await platform.writeClipboard('product-001')
    const selected = await platform.pickFile()
    expect(selected).toEqual({
      name: 'design.txt', mediaType: 'text/plain', bytes: new Uint8Array([97, 98, 99])
    })
    await expect(platform.saveFile(selected!)).resolves.toBe('saved')
    expect(calls).toEqual([
      ['desktop_notify', { message: '生成任务已完成' }],
      ['desktop_write_clipboard', { text: 'product-001' }],
      ['desktop_pick_file', undefined],
      ['desktop_save_file', { file: { name: 'design.txt', mediaType: 'text/plain', data: 'YWJj' } }]
    ])
  })

  it('decodes only the exact canonical Files binary envelope', async () => {
    vi.stubGlobal('window', { location: { origin: 'https://desktop.test' } })
    const fetcher = createDesktopFetch(async <T>() => ({
      status: 200,
      body: { encoding: 'base64', mediaType: 'application/octet-stream', data: '/wBh' }
    }) as T)
    const response = await fetcher('/api/files/objects/00000000-0000-4000-8000-000000000013/content')
    expect([...new Uint8Array(await response.arrayBuffer())]).toEqual([0xff, 0x00, 0x61])
    expect(response.headers.get('content-type')).toBe('application/octet-stream')

    for (const body of [
      { encoding: 'base64', mediaType: 'image/png', data: '/wBh' },
      { encoding: 'base64', mediaType: 'application/octet-stream', data: '/wBh', sessionToken: 'hidden' },
      { encoding: 'base64', mediaType: 'application/octet-stream', data: 'YQ' }
    ]) {
      const invalid = createDesktopFetch(async <T>() => ({ status: 200, body }) as T)
      await expect(invalid('/api/files/objects/00000000-0000-4000-8000-000000000013/content'))
        .rejects.toThrow('invalid desktop binary response')
    }
  })

  it('accepts only the public identity projection and rejects hidden session material', async () => {
    const runtime = createDesktopRuntime(async <T>(command: string) => {
      if (command === 'desktop_identity') return {
        kind: 'authenticated',
        profile: { id: 'account-1', username: 'admin', displayName: 'Admin', email: 'admin@example.test' },
        permissions: ['files.objects.read'],
        dataScope: 'all'
      } as T
      throw new Error('unexpected command')
    })
    await expect(runtime.loadIdentity()).resolves.toEqual({
      kind: 'authenticated', subjectId: 'account-1', permissions: ['files.objects.read'], dataScope: 'all'
    })

    const poisoned = createDesktopRuntime(async <T>() => ({
      kind: 'authenticated',
      profile: { id: 'account-1', username: 'admin', displayName: 'Admin', email: 'admin@example.test' },
      permissions: ['files.objects.read'],
      dataScope: 'all',
      csrfToken: 'abcdefghijklmnopqrstuvwxyzABCDEFGH123456789'
    }) as T)
    await expect(poisoned.loadIdentity()).rejects.toThrow('invalid desktop identity')
  })

  it('keeps self-scoped identities fail closed for global administrative capabilities', async () => {
    const runtime = createDesktopRuntime(async <T>() => ({
      kind: 'authenticated',
      profile: { id: 'account-1', username: 'admin', displayName: 'Admin', email: 'admin@example.test' },
      permissions: [],
      dataScope: 'self'
    }) as T)
    await expect(runtime.loadIdentity()).resolves.toMatchObject({ dataScope: 'self', permissions: [] })
  })

  it('never adds headers, origins, or session data to a business invocation', async () => {
    const calls: Array<readonly [string, Record<string, unknown> | undefined]> = []
    const invoke = async <T>(command: string, args?: Record<string, unknown>) => {
      calls.push([command, args])
      return { status: 200, body: { rows: [], total: 0 } } as T
    }
    const transport = createDesktopTransport(invoke)
    await transport.request('/files?page=1', 'GET')
    expect(calls).toEqual([['desktop_request', {
      request: { path: '/files?page=1', method: 'GET', body: undefined }
    }]])
  })

  it('uses dedicated typed commands for login and logout', async () => {
    const commands: string[] = []
    const invoke = async <T>(command: string) => {
      commands.push(command)
      return (command === 'desktop_login'
        ? { id: 'account-1', username: 'admin', displayName: 'Admin', email: 'admin@example.test' }
        : { localCleared: true, remoteRevoked: true }) as T
    }
    const session = createDesktopSession(invoke)
    await expect(session.login('admin', 'administrator password')).resolves.toMatchObject({ id: 'account-1' })
    await session.logout()
    expect(commands).toEqual(['desktop_login', 'desktop_logout'])
  })

  it('rejects logout responses that do not confirm local vault clearing', async () => {
    const session = createDesktopSession(async <T>() => ({ localCleared: false, remoteRevoked: true }) as T)
    await expect(session.logout()).rejects.toThrow('invalid desktop logout result')
  })

  it('routes heartbeat and renew through secret-free dedicated native commands', async () => {
    const calls: Array<readonly [string, Record<string, unknown> | undefined]> = []
    const client = createDesktopSessionClient(async <T>(command: string, args?: Record<string, unknown>) => {
      calls.push([command, args])
      return { id: 'account-1', username: 'admin', displayName: 'Admin', email: 'admin@example.test' } as T
    })
    await client.heartbeat()
    await client.renew()
    expect(calls).toEqual([
      ['desktop_session_heartbeat', undefined],
      ['desktop_session_renew', undefined]
    ])
    expect(JSON.stringify(calls)).not.toMatch(/csrf|token|cookie/i)
  })

  it.each([
    ['desktop session authentication failed', 'authentication'],
    ['desktop session authorization failed', 'authorization'],
    ['desktop runtime unavailable', 'unavailable']
  ])('maps native maintenance failure %s to %s', async (failure, category) => {
    const client = createDesktopSessionClient(async () => { throw failure })
    await expect(client.heartbeat()).rejects.toMatchObject({ name: 'SessionRequestError', category })
  })

  it('rejects native maintenance projections with extra session material', async () => {
    const client = createDesktopSessionClient(async <T>() => ({
      id: 'account-1', username: 'admin', displayName: 'Admin', email: 'admin@example.test', csrfToken: 'hidden'
    }) as T)
    await expect(client.renew()).rejects.toMatchObject({ name: 'SessionRequestError', category: 'unavailable' })
  })
})
