type Fetch = typeof globalThis.fetch

interface Problem { code?: string }
interface CoordinationMessage {
  readonly type: 'session-changed' | 'session-invalidated'
  readonly version: number
  readonly timeBucket: number
}
interface CoordinationChannel {
  postMessage(message: CoordinationMessage): void
  addEventListener(type: 'message', listener: (event: MessageEvent<unknown>) => void): void
  removeEventListener(type: 'message', listener: (event: MessageEvent<unknown>) => void): void
  close(): void
}
interface LeaseStorage {
  getItem(key: string): string | null
  setItem(key: string, value: string): void
  removeItem(key: string): void
}

export interface BrowserSessionFetchOptions {
  readonly now?: () => number
  readonly setTimer?: (callback: () => void, delay: number) => ReturnType<typeof setTimeout>
  readonly clearTimer?: (timer: ReturnType<typeof setTimeout>) => void
  readonly storage?: LeaseStorage | null
  readonly channel?: CoordinationChannel | null
  readonly instanceId?: string
  readonly heartbeatIntervalMs?: number
  readonly renewIntervalMs?: number
  readonly activeWindowMs?: number
  readonly leaderLeaseMs?: number
}

export interface BrowserSessionFetch extends Fetch {
  close(): void
}

const csrfPattern = /^[A-Za-z0-9_-]{43}$/
const safeMethods = new Set(['GET', 'HEAD', 'OPTIONS'])
const coordinationKey = 'go-admin-plus.session.leader.v1'
const channelName = 'go-admin-plus.session.v1'

const defaultChannel = (): CoordinationChannel | null => {
  try {
    return typeof BroadcastChannel === 'undefined' ? null : new BroadcastChannel(channelName)
  } catch {
    return null
  }
}

const defaultStorage = (): LeaseStorage | null => {
  try {
    return typeof localStorage === 'undefined' ? null : localStorage
  } catch {
    return null
  }
}

const defaultInstanceId = (): string => {
  try {
    return crypto.randomUUID()
  } catch {
    return `tab-${Math.random().toString(36).slice(2)}`
  }
}

const isCoordinationMessage = (value: unknown): value is CoordinationMessage => {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return false
  const record = value as Record<string, unknown>
  return Object.keys(record).length === 3
    && (record.type === 'session-changed' || record.type === 'session-invalidated')
    && Number.isSafeInteger(record.version)
    && Number(record.version) >= 0
    && Number.isSafeInteger(record.timeBucket)
    && Number(record.timeBucket) >= 0
}

const leaseOwner = (storage: LeaseStorage | null, now: number): string | null => {
  if (!storage) return null
  try {
    const raw = storage.getItem(coordinationKey)
    if (!raw) return null
    const lease = JSON.parse(raw) as Record<string, unknown>
    if (typeof lease.owner !== 'string' || !Number.isFinite(lease.expiresAt) || Number(lease.expiresAt) <= now) return null
    return lease.owner
  } catch {
    return null
  }
}

/**
 * Owns the browser session's stable CSRF value and bounded maintenance writes.
 * Broadcast messages are hints only; every remote change is verified with current.
 */
export const createBrowserSessionFetch = (
  fetcher: Fetch = globalThis.fetch,
  apiOrigin = globalThis.location.origin,
  options: BrowserSessionFetchOptions = {}
): BrowserSessionFetch => {
  const now = options.now ?? Date.now
  const setTimer = options.setTimer ?? ((callback, delay) => setTimeout(callback, delay))
  const clearTimer = options.clearTimer ?? (timer => clearTimeout(timer))
  const storage = options.storage === undefined ? defaultStorage() : options.storage
  const channel = options.channel === undefined ? defaultChannel() : options.channel
  const instanceId = options.instanceId ?? defaultInstanceId()
  const heartbeatInterval = options.heartbeatIntervalMs ?? 5 * 60_000
  const renewInterval = options.renewIntervalMs ?? 20 * 60_000
  const activeWindow = options.activeWindowMs ?? 10 * 60_000
  const leaderLease = options.leaderLeaseMs ?? 45_000
  const fallbackFactor = storage ? 1 : 2

  let csrf = ''
  let lastActivityAt = -1
  let lastHeartbeatAt = 0
  let lastRenewAt = 0
  let version = 0
  let timer: ReturnType<typeof setTimeout> | undefined
  let closed = false
  let maintenanceRunning = false

  const clearSchedule = () => {
    if (timer !== undefined) clearTimer(timer)
    timer = undefined
  }
  const publish = (type: CoordinationMessage['type']) => {
    version += 1
    try {
      channel?.postMessage({ type, version, timeBucket: Math.floor(now() / 60_000) })
    } catch {
      // Coordination loss never changes the local server-backed session fact.
    }
  }
  const releaseLeadership = () => {
    try {
      if (storage && leaseOwner(storage, now()) === instanceId) storage.removeItem(coordinationKey)
    } catch {
      // The bounded lease remains the recovery path when storage is unavailable.
    }
  }
  const clearSession = (notify: boolean) => {
    csrf = ''
    lastActivityAt = -1
    lastHeartbeatAt = 0
    lastRenewAt = 0
    clearSchedule()
    releaseLeadership()
    if (notify) publish('session-invalidated')
  }
  const claimLeadership = (time: number): boolean => {
    if (!storage) return true
    const owner = leaseOwner(storage, time)
    if (owner !== null && owner !== instanceId) return false
    try {
      storage.setItem(coordinationKey, JSON.stringify({ owner: instanceId, expiresAt: time + leaderLease }))
      return leaseOwner(storage, time) === instanceId
    } catch {
      return true
    }
  }
  const learnStableCSRF = (candidate: string | null, authoritative: boolean): boolean => {
    if (!candidate || !csrfPattern.test(candidate)) return false
    if (csrf && csrf !== candidate && !authoritative) return false
    const changed = csrf !== candidate
    csrf = candidate
    return changed
  }
  const sessionResponseCSRF = async (response: Response): Promise<string | null> => {
    const header = response.headers.get('X-CSRF-Token')
    let bodyToken: unknown
    try {
      const body = await response.clone().json() as unknown
      if (body && typeof body === 'object' && !Array.isArray(body) && Object.hasOwn(body, 'csrfToken')) {
        bodyToken = (body as Record<string, unknown>).csrfToken
      }
    } catch {
      bodyToken = undefined
    }
    if (header !== null && !csrfPattern.test(header)) return null
    if (bodyToken !== undefined && (typeof bodyToken !== 'string' || !csrfPattern.test(bodyToken))) return null
    if (header !== null && bodyToken !== undefined && header !== bodyToken) return null
    return header ?? (bodyToken as string | undefined) ?? null
  }
  const schedule = () => {
    clearSchedule()
    if (closed || !csrf || lastActivityAt < 0) return
    const time = now()
    const heartbeatDue = lastHeartbeatAt + heartbeatInterval * fallbackFactor
    const renewDue = lastRenewAt + renewInterval * fallbackFactor
    const delay = Math.max(1, Math.min(heartbeatDue, renewDue) - time)
    timer = setTimer(() => { void maintain() }, delay)
  }
  const maintain = async () => {
    if (closed || maintenanceRunning || !csrf) return
    const time = now()
    if (time - lastActivityAt > activeWindow) { clearSchedule(); return }
    if (!claimLeadership(time)) {
      timer = setTimer(() => { void maintain() }, leaderLease)
      return
    }
    maintenanceRunning = true
    const renew = time - lastRenewAt >= renewInterval * fallbackFactor
    const heartbeat = time - lastHeartbeatAt >= heartbeatInterval * fallbackFactor
    if (!renew && !heartbeat) { maintenanceRunning = false; schedule(); return }
    const path = renew ? '/api/iam/session/renew' : '/api/iam/session/heartbeat'
    try {
      const response = await fetcher(path, {
        method: 'POST',
        credentials: 'include',
        headers: { 'X-CSRF-Token': csrf }
      })
      if (response.status === 401 || response.status === 403) {
        clearSession(true)
        return
      }
      if (response.ok) {
        const returnedCSRF = await sessionResponseCSRF(response)
        if (!returnedCSRF || returnedCSRF !== csrf) {
          clearSession(true)
          return
        }
        if (renew) lastRenewAt = time
        else lastHeartbeatAt = time
      } else if (renew) {
        lastRenewAt = time
      } else {
        lastHeartbeatAt = time
      }
    } catch {
      if (renew) lastRenewAt = time
      else lastHeartbeatAt = time
    } finally {
      maintenanceRunning = false
      schedule()
    }
  }
  const refreshFromServer = async () => {
    if (closed) return
    try {
      const response = await fetcher('/api/iam/session/current', {
        credentials: 'include',
        headers: { accept: 'application/json' }
      })
      if (!response.ok) { clearSession(false); return }
      const returnedCSRF = await sessionResponseCSRF(response)
      if (!returnedCSRF) { clearSession(false); return }
      if (learnStableCSRF(returnedCSRF, true)) {
        const time = now()
        lastActivityAt = time
        lastHeartbeatAt ||= time
        lastRenewAt ||= time
      }
      schedule()
    } catch {
      // A channel hint never changes authentication when the server cannot confirm it.
    }
  }
  const onMessage = (event: MessageEvent<unknown>) => {
    if (!isCoordinationMessage(event.data)) return
    if (event.data.type === 'session-invalidated') clearSession(false)
    else void refreshFromServer()
  }
  channel?.addEventListener('message', onMessage)

  const shared = (async (input, init) => {
    const request = new Request(input, init)
    const url = new URL(request.url)
    if (url.origin !== apiOrigin || !url.pathname.startsWith('/api/')) return fetcher(request)

    const headers = new Headers(request.headers)
    if (safeMethods.has(request.method.toUpperCase()) || !csrf) headers.delete('X-CSRF-Token')
    else headers.set('X-CSRF-Token', csrf)

    const response = await fetcher(new Request(request, { credentials: 'include', headers }))
    const sessionFact = ['/api/iam/session/login', '/api/iam/session/current'].includes(url.pathname)
    const endsSession = ['/api/iam/session/logout', '/api/iam/account/password'].includes(url.pathname)
    if (response.ok && endsSession) {
      clearSession(true)
    } else if (response.ok && sessionFact) {
      const time = now()
      const returnedCSRF = await sessionResponseCSRF(response)
      if (!returnedCSRF) {
        clearSession(true)
      } else {
        if (learnStableCSRF(returnedCSRF, true)) publish('session-changed')
        lastActivityAt = time
        lastHeartbeatAt ||= time
        lastRenewAt ||= time
        schedule()
      }
    } else if (response.ok && csrf) {
      lastActivityAt = now()
      schedule()
    }
    if (response.status === 401) clearSession(true)
    if (response.status === 403) {
      const problem = await response.clone().json().catch(() => null) as Problem | null
      if (problem?.code === 'CSRF_REJECTED') clearSession(true)
    }
    return response
  }) as BrowserSessionFetch

  shared.close = () => {
    if (closed) return
    closed = true
    clearSchedule()
    try {
      channel?.removeEventListener('message', onMessage)
      channel?.close()
    } catch {
      // Channel cleanup is best effort; no secret or authority lives there.
    }
    releaseLeadership()
  }
  Object.defineProperty(shared,Symbol.for('go-admin-plus.session-fetch'),{value:true})
  return shared
}
