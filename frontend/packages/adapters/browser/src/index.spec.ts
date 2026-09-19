import { describe, expect, it, vi } from 'vitest'

import { createWebPlatform, createWebRuntime } from './index'

describe('browser product runtime', () => {
  it('implements the same host capability port without leaking browser globals', async () => {
    const notifications: string[] = []
    const clipboard: string[] = []
    const selected = { name: 'report.pdf', mediaType: 'application/pdf', bytes: new Uint8Array([37, 80, 68, 70]) }
    const saved: string[] = []
    const platform = createWebPlatform({
      notify: async message => { notifications.push(message) },
      writeClipboard: async text => { clipboard.push(text) },
      pickFile: async () => selected,
      saveFile: async file => { saved.push(file.name); return 'saved' }
    })

    expect([...platform.listCapabilities()].sort()).toEqual([
      'clipboard-write', 'file-open', 'file-save', 'notification'
    ])
    await platform.notify('保存成功')
    await platform.writeClipboard('demo-sku')
    await expect(platform.pickFile()).resolves.toEqual(selected)
    await expect(platform.saveFile(selected)).resolves.toBe('saved')
    expect(notifications).toEqual(['保存成功'])
    expect(clipboard).toEqual(['demo-sku'])
    expect(saved).toEqual(['report.pdf'])
  })

  it('rejects invalid files returned by or sent to an injected browser host', async () => {
    const platform = createWebPlatform({
      notify: async () => {},
      writeClipboard: async () => {},
      pickFile: async () => ({ name: '../notes.txt', mediaType: 'text/plain', bytes: new Uint8Array([1]) }),
      saveFile: async () => 'saved'
    })

    await expect(platform.pickFile()).rejects.toThrow('host file is invalid')
    await expect(platform.saveFile({
      name: 'payload.bin',
      mediaType: 'application/octet-stream',
      bytes: new Uint8Array(1)
    })).rejects.toThrow('host file is invalid')
    await expect(platform.saveFile({
      name: 'notes.txt',
      mediaType: 'text/plain',
      bytes: new Uint8Array(10 * 1024 * 1024 + 1)
    })).rejects.toThrow('host file is invalid')
  })

  it('accepts canonical dot-separated permissions and rejects legacy separators', async () => {
    const fetcher = vi.fn<typeof fetch>()
      .mockResolvedValueOnce(new Response(JSON.stringify({
        kind: 'authenticated', subjectId: 'account-1', permissions: ['iam.users.read', 'files.objects.write'], dataScope: 'self'
      }), { status: 200, headers: { 'content-type': 'application/json' } }))
      .mockResolvedValueOnce(new Response(JSON.stringify([
        { path: '/iam/users', permission: 'iam.users.read' },
        { path: '/files', permission: 'files.objects.write' }
      ]), { status: 200, headers: { 'content-type': 'application/json' } }))
    const runtime = createWebRuntime(fetcher)
    await expect(runtime.loadIdentity()).resolves.toMatchObject({ kind: 'authenticated', permissions: ['iam.users.read', 'files.objects.write'], dataScope: 'self' })
    await expect(runtime.loadNavigation()).resolves.toHaveLength(2)

    const legacy = createWebRuntime(vi.fn<typeof fetch>().mockResolvedValue(new Response(JSON.stringify({
      kind: 'authenticated', subjectId: 'account-1', permissions: ['iam:users:read'], dataScope: 'all'
    }), { status: 200, headers: { 'content-type': 'application/json' } })))
    await expect(legacy.loadIdentity()).rejects.toThrow('invalid identity response')
  })

  it('requires an explicit valid data scope for every authenticated identity', async () => {
    for (const dataScope of [undefined, null, 'invalid', '']) {
      const runtime = createWebRuntime(vi.fn<typeof fetch>().mockResolvedValue(new Response(JSON.stringify({
        kind: 'authenticated', subjectId: 'account-1', permissions: ['files.objects.read'],
        ...(dataScope === undefined ? {} : { dataScope })
      }), { status: 200, headers: { 'content-type': 'application/json' } })))
      await expect(runtime.loadIdentity()).rejects.toThrow('invalid identity response')
    }
  })
})
