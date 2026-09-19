import { parseNavigationEntries } from '@go-admin-plus/platform'
import type {
  HostFile,
  HostFileSaveResult,
  PlatformPort,
  PermissionCode,
  RuntimeIdentity,
  RuntimeRequest,
  ShellRuntimePort
} from '@go-admin-plus/platform'

export {
  createBrowserSessionFetch,
  type BrowserSessionFetch,
  type BrowserSessionFetchOptions
} from './session-fetch'

type Fetch = typeof globalThis.fetch
interface WebHostOperations {
  pickFile(): Promise<HostFile | null>
  saveFile(file: HostFile): Promise<HostFileSaveResult>
  notify(message: string): Promise<void>
  writeClipboard(text: string): Promise<void>
}

const browserHostOperations = (): WebHostOperations => ({
  pickFile: () => new Promise((resolvePick, rejectPick) => {
    const input = document.createElement('input')
    input.type = 'file'
    input.accept = '.pdf,.jpg,.jpeg,.png,.txt,application/pdf,image/jpeg,image/png,text/plain'
    input.addEventListener('cancel', () => resolvePick(null), { once: true })
    input.addEventListener('change', () => {
      const file = input.files?.[0]
      if (!file) { resolvePick(null); return }
      if (file.size > maximumHostFileBytes) { rejectPick(new Error('selected file exceeds the product limit')); return }
      file.arrayBuffer().then(buffer => resolvePick({
        name: file.name,
        mediaType: file.type,
        bytes: new Uint8Array(buffer)
      }), rejectPick)
    }, { once: true })
    input.click()
  }),
  async saveFile(file) {
    validateHostFile(file)
    const blob = new Blob([file.bytes.slice().buffer], { type: file.mediaType })
    const url = URL.createObjectURL(blob)
    try {
      const anchor = document.createElement('a')
      anchor.href = url
      anchor.download = file.name
      anchor.click()
      return 'saved'
    } finally {
      URL.revokeObjectURL(url)
    }
  },
  async notify(message) {
    if (!('Notification' in globalThis)) throw new Error('browser notifications are unavailable')
    let permission = Notification.permission
    if (permission === 'default') permission = await Notification.requestPermission()
    if (permission !== 'granted') throw new Error('browser notification permission denied')
    new Notification('Go Admin Plus', { body: message })
  },
  async writeClipboard(text) {
    if (!globalThis.navigator?.clipboard?.writeText) throw new Error('browser clipboard is unavailable')
    await globalThis.navigator.clipboard.writeText(text)
  }
})

const maximumHostFileBytes = 10 * 1024 * 1024
const validHostFileName = (name: string) => name.length > 0 && name.length <= 255 && !/[\\/\0]/.test(name)
const validHostMediaType = (mediaType: string) => ['application/pdf', 'image/jpeg', 'image/png', 'text/plain'].includes(mediaType)
const validateHostFile = (file: HostFile) => {
  if (!validHostFileName(file.name) || !validHostMediaType(file.mediaType) || file.bytes.length > maximumHostFileBytes) {
    throw new Error('host file is invalid')
  }
}

const hostCapabilities = Object.freeze(['clipboard-write', 'file-open', 'file-save', 'notification'] as const)

export const createWebPlatform = (host: WebHostOperations = browserHostOperations()): PlatformPort => ({
  runtime: 'web',
  listCapabilities: () => new Set(hostCapabilities),
  pickFile: async () => {
    const file = await host.pickFile()
    if (file) validateHostFile(file)
    return file
  },
  saveFile: async file => {
    validateHostFile(file)
    const result = await host.saveFile(file)
    if (result !== 'saved' && result !== 'cancelled') throw new Error('host save result is invalid')
    return result
  },
  notify: message => host.notify(message),
  writeClipboard: text => host.writeClipboard(text)
})

const permissionPattern = /^[a-z][a-z0-9-]*(?:\.[a-z][a-z0-9-]*){1,2}$/

const hasExactKeys = (record: Record<string, unknown>, expected: ReadonlyArray<string>) => {
  const keys = Object.keys(record).sort()
  const expectedKeys = [...expected].sort()
  return keys.length === expectedKeys.length && keys.every((key, index) => key === expectedKeys[index])
}

const parseIdentity = (value: unknown): RuntimeIdentity => {
  if (!value || typeof value !== 'object') throw new Error('invalid identity response')
  const record = value as Record<string, unknown>
  if (record.kind === 'unauthenticated') {
    if (!hasExactKeys(record, ['kind'])) throw new Error('invalid identity response')
    return { kind: 'unauthenticated' }
  }
  if (
    record.kind !== 'authenticated'
    || !hasExactKeys(record, ['dataScope', 'kind', 'permissions', 'subjectId'])
    || typeof record.subjectId !== 'string'
    || record.subjectId.length === 0
    || (record.dataScope !== 'self' && record.dataScope !== 'all')
    || !Array.isArray(record.permissions)
    || !record.permissions.every(permission =>
      typeof permission === 'string' && permissionPattern.test(permission)
    )
  ) {
    throw new Error('invalid identity response')
  }
  return {
    kind: 'authenticated',
    subjectId: record.subjectId,
    permissions: record.permissions as PermissionCode[],
    dataScope: record.dataScope
  }
}

const parseNavigation = parseNavigationEntries

const readJson = async (fetch: Fetch, path: string, request?: RuntimeRequest): Promise<unknown> => {
  const response = await fetch(path, {
    credentials: 'include',
    headers: { accept: 'application/json' },
    ...(request?.signal ? { signal: request.signal } : {})
  })
  if (!response.ok) throw new Error(`runtime request failed with ${response.status}`)
  return response.json()
}

/** Creates the browser adapter; cookie credentials remain inside fetch and never cross the port. */
export const createWebRuntime = (fetch: Fetch = globalThis.fetch): ShellRuntimePort => ({
  async loadIdentity(request) {
    const response = await fetch('/api/runtime/identity', {
      credentials: 'include',
      headers: { accept: 'application/json' },
      ...(request?.signal ? { signal: request.signal } : {})
    })
    if (response.status === 401) return { kind: 'unauthenticated' }
    if (!response.ok) throw new Error(`identity request failed with ${response.status}`)
    return parseIdentity(await response.json())
  },
  async loadNavigation(request) {
    return parseNavigation(await readJson(fetch, '/api/runtime/navigation', request))
  }
})
