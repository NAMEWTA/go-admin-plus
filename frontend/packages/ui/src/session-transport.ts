type Fetcher = typeof globalThis.fetch

const csrfPattern = /^[A-Za-z0-9_-]{8,128}$/
const safeMethods = new Set(['GET', 'HEAD', 'OPTIONS'])
const managedFetchers = new WeakSet<Function>()
const sharedFetchers = new WeakMap<Function, SessionAwareFetch>()
const managedSymbol = Symbol.for('go-admin-plus.session-fetch')

export interface SessionAwareFetch extends Fetcher {
  close?: () => void
  resetSession?: () => void
}

/**
 * Shared browser boundary for cookie credentials, stable CSRF and mutation order.
 * Domain clients keep their own response-to-domain error mapping; this function
 * owns the session capability so it cannot drift between modules.
 */
export const createSessionAwareFetch = (fetcher: Fetcher = globalThis.fetch): SessionAwareFetch => {
  if (managedFetchers.has(fetcher) || Reflect.get(fetcher,managedSymbol) === true) return fetcher as SessionAwareFetch
  const existing=sharedFetchers.get(fetcher);if(existing)return existing

  let csrf = ''
  let mutationTail: Promise<void> = Promise.resolve()
  let closed = false

  const learnCSRF = async (response: Response): Promise<boolean> => {
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
    if (header !== null && !csrfPattern.test(header)) { csrf = ''; return false }
    if (bodyToken !== undefined && (typeof bodyToken !== 'string' || !csrfPattern.test(bodyToken))) { csrf = ''; return false }
    if (header !== null && bodyToken !== undefined && header !== bodyToken) { csrf = ''; return false }
    const candidate = header ?? (bodyToken as string | undefined)
    if (!candidate) return true
    if (!csrf || csrf === candidate) csrf = candidate
    return true
  }

  const execute = async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
    if (closed) throw new DOMException('session transport is closed', 'AbortError')
    const request = new Request(input, init)
    const method = request.method.toUpperCase()
    const headers = new Headers(request.headers)
    if (safeMethods.has(method) || !csrf) headers.delete('X-CSRF-Token')
    else headers.set('X-CSRF-Token', csrf)
    const response = await fetcher(new Request(request, { credentials: 'include', headers }))
    if (!await learnCSRF(response)) {
      return new Response(JSON.stringify({ category: 'authentication', code: 'CSRF_REJECTED' }), {
        status: 403,
        headers: { 'Content-Type': 'application/problem+json' }
      })
    }
    const path = new URL(request.url).pathname
    if (response.ok && (path === '/api/iam/session/logout' || path === '/api/iam/account/password')) csrf = ''
    if (response.status === 401) {
      csrf = ''
      return new Response(JSON.stringify({ category: 'authentication', code: 'AUTHENTICATION_REQUIRED' }), { status: 401, headers: { 'Content-Type': 'application/problem+json' } })
    }
    if (response.status === 403) {
      const problem = await response.clone().json().catch(() => null) as { code?: unknown } | null
      if (problem?.code === 'CSRF_REJECTED') csrf = ''
    }
    return response
  }

  const shared = (async (input: RequestInfo | URL, init?: RequestInit) => {
    const request = new Request(input, init)
    if (safeMethods.has(request.method.toUpperCase())) return execute(request)
    const result = mutationTail.then(() => execute(request), () => execute(request))
    mutationTail = result.then(() => undefined, () => undefined)
    return result
  }) as SessionAwareFetch
  shared.close = () => { closed = true; csrf = '' }
  shared.resetSession = () => { csrf = '' }
  Object.defineProperty(shared,managedSymbol,{value:true})
  sharedFetchers.set(fetcher,shared)
  managedFetchers.add(shared)
  return shared
}

export const isSessionAwareFetch = (fetcher: Fetcher): fetcher is SessionAwareFetch => managedFetchers.has(fetcher) || Reflect.get(fetcher,managedSymbol) === true
