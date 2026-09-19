import { describe, expect, it, vi } from 'vitest'

import { createBrowserSessionFetch, type BrowserSessionFetchOptions } from './session-fetch'

const origin = 'https://app.example.test'
const csrf = (value: string) => value.repeat(43)
const profile = { id: '1', username: 'admin', displayName: 'Admin', email: 'admin@example.test' }
const response = (status: number, value: unknown, token?: string) => new Response(JSON.stringify(value), {
  status,
  headers: { 'content-type': 'application/json', ...(token ? { 'X-CSRF-Token': token } : {}) }
})

class FakeClock {
  time = 1_000
  private sequence = 0
  private readonly timers = new Map<number, { at: number, callback: () => void }>()

  now = () => this.time
  setTimer = (callback: () => void, delay: number): ReturnType<typeof setTimeout> => {
    const id = ++this.sequence
    this.timers.set(id, { at: this.time + delay, callback })
    return id as unknown as ReturnType<typeof setTimeout>
  }
  clearTimer = (timer: ReturnType<typeof setTimeout>) => {
    this.timers.delete(timer as unknown as number)
  }
  async advance(milliseconds: number) {
    this.time += milliseconds
    for (;;) {
      const due = [...this.timers.entries()]
        .filter(([, timer]) => timer.at <= this.time)
        .sort((left, right) => left[1].at - right[1].at || left[0] - right[0])
      if (due.length === 0) break
      for (const [id, timer] of due) {
        this.timers.delete(id)
        timer.callback()
      }
      await new Promise(resolve => setTimeout(resolve, 0))
    }
    await new Promise(resolve => setTimeout(resolve, 0))
  }
}

class ChannelBus {
  readonly messages: unknown[] = []
  private readonly ports = new Set<Set<(event: MessageEvent<unknown>) => void>>()

  port() {
    const listeners = new Set<(event: MessageEvent<unknown>) => void>()
    this.ports.add(listeners)
    return {
      postMessage: (message: unknown) => {
        this.messages.push(structuredClone(message))
        for (const port of this.ports) {
          if (port === listeners) continue
          for (const listener of port) listener({ data: structuredClone(message) } as MessageEvent<unknown>)
        }
      },
      addEventListener: (_type: 'message', listener: (event: MessageEvent<unknown>) => void) => listeners.add(listener),
      removeEventListener: (_type: 'message', listener: (event: MessageEvent<unknown>) => void) => listeners.delete(listener),
      close: () => this.ports.delete(listeners)
    }
  }
}

const fakeStorage = () => {
  const values = new Map<string, string>()
  return {
    getItem: (key: string) => values.get(key) ?? null,
    setItem: (key: string, value: string) => { values.set(key, value) },
    removeItem: (key: string) => { values.delete(key) }
  }
}

describe('browser session fetch', () => {
  it('keeps one stable CSRF across API clients and ignores rotating business headers', async () => {
    const requests: Request[] = []
    const fetcher = vi.fn<typeof fetch>(async input => {
      requests.push(new Request(input))
      if (requests.length === 1) return response(200, { profile, csrfToken: csrf('a') }, csrf('a'))
      return response(200, {}, csrf('b'))
    })
    const shared = createBrowserSessionFetch(fetcher, origin, { storage: null, channel: null })

    await shared(`${origin}/api/iam/session/login`, { method: 'POST' })
    await shared(`${origin}/api/scheduler/definitions`, { method: 'POST' })
    await shared(`${origin}/api/scheduler/definitions`, { method: 'POST' })

    expect(requests[0]?.headers.has('X-CSRF-Token')).toBe(false)
    expect(requests[1]?.headers.get('X-CSRF-Token')).toBe(csrf('a'))
    expect(requests[2]?.headers.get('X-CSRF-Token')).toBe(csrf('a'))
    shared.close()
  })

  it('allows parallel protected requests to use the same family token', async () => {
    let release!: () => void
    const gate = new Promise<void>(resolve => { release = resolve })
    const requests: Request[] = []
    const fetcher = vi.fn<typeof fetch>(async input => {
      const request = new Request(input)
      requests.push(request)
      if (requests.length === 1) return response(200, { profile, csrfToken: csrf('a') }, csrf('a'))
      if (requests.length === 2) await gate
      return response(200, {})
    })
    const shared = createBrowserSessionFetch(fetcher, origin, { storage: null, channel: null })
    await shared(`${origin}/api/iam/session/current`)

    const first = shared(`${origin}/api/scheduler/definitions`, { method: 'POST' })
    const second = shared(`${origin}/api/files`, { method: 'POST' })
    await Promise.resolve()
    expect(fetcher).toHaveBeenCalledTimes(3)
    expect(requests[1]?.headers.get('X-CSRF-Token')).toBe(csrf('a'))
    expect(requests[2]?.headers.get('X-CSRF-Token')).toBe(csrf('a'))
    release()
    await Promise.all([first, second])
    shared.close()
  })

  it('fails closed when authoritative CSRF header and body disagree', async () => {
    const requests: Request[] = []
    const fetcher = vi.fn<typeof fetch>(async input => {
      requests.push(new Request(input))
      if (requests.length === 1) return response(200, { profile, csrfToken: csrf('a') }, csrf('b'))
      return response(200, {})
    })
    const shared = createBrowserSessionFetch(fetcher, origin, { storage: null, channel: null })
    await shared(`${origin}/api/iam/session/current`)
    await shared(`${origin}/api/scheduler/definitions`, { method: 'POST' })
    expect(requests[1]?.headers.has('X-CSRF-Token')).toBe(false)
    shared.close()
  })

  it('propagates successful logout and stops using the stable CSRF', async () => {
    const bus = new ChannelBus()
    const requests: Request[] = []
    const fetcher = vi.fn<typeof fetch>(async input => {
      requests.push(new Request(input))
      if (requests.length === 1) return response(200, { profile, csrfToken: csrf('a') }, csrf('a'))
      if (requests.length === 2) return new Response(null, { status: 204 })
      return response(200, {})
    })
    const shared = createBrowserSessionFetch(fetcher, origin, { storage: null, channel: bus.port() })
    await shared(`${origin}/api/iam/session/current`)
    await shared(`${origin}/api/iam/session/logout`, { method: 'POST' })
    await shared(`${origin}/api/scheduler/definitions`, { method: 'POST' })
    expect(requests[1]?.headers.get('X-CSRF-Token')).toBe(csrf('a'))
    expect(requests[2]?.headers.has('X-CSRF-Token')).toBe(false)
    expect(bus.messages.at(-1)).toMatchObject({ type: 'session-invalidated' })
    shared.close()
  })

  it('never carries session state cross-origin and clears it after CSRF rejection', async () => {
    const requests: Request[] = []
    const fetcher = vi.fn<typeof fetch>(async input => {
      const request = new Request(input)
      requests.push(request)
      if (requests.length === 1) return response(200, { profile, csrfToken: csrf('a') }, csrf('a'))
      if (requests.length === 2) return response(200, {})
      if (requests.length === 3) return response(403, { code: 'CSRF_REJECTED' })
      return response(200, {})
    })
    const shared = createBrowserSessionFetch(fetcher, origin, { storage: null, channel: null })

    await shared(`${origin}/api/iam/session/current`)
    await shared('https://outside.example/api/probe', { method: 'POST' })
    await shared(`${origin}/api/scheduler/definitions`, { method: 'POST' })
    await shared(`${origin}/api/scheduler/definitions`, { method: 'POST' })

    expect(requests[1]?.headers.has('X-CSRF-Token')).toBe(false)
    expect(requests[2]?.headers.get('X-CSRF-Token')).toBe(csrf('a'))
    expect(requests[3]?.headers.has('X-CSRF-Token')).toBe(false)
    shared.close()
  })

  it('elects one maintenance writer and recovers after its lease owner closes', async () => {
    const clock = new FakeClock()
    const bus = new ChannelBus()
    const storage = fakeStorage()
    const calls: string[] = []
    const makeFetcher = (name: string): typeof fetch => async input => {
      const path = new URL(String(input), origin).pathname
      calls.push(`${name}:${path}`)
      return response(200, { profile, csrfToken: csrf('a') }, csrf('a'))
    }
    const common = {
      now: clock.now,
      setTimer: clock.setTimer,
      clearTimer: clock.clearTimer,
      storage,
      heartbeatIntervalMs: 100,
      renewIntervalMs: 1_000,
      activeWindowMs: 2_000,
      leaderLeaseMs: 50
    } satisfies Omit<BrowserSessionFetchOptions, 'channel' | 'instanceId'>
    const first = createBrowserSessionFetch(makeFetcher('first'), origin, {
      ...common, channel: bus.port(), instanceId: 'first'
    })
    const second = createBrowserSessionFetch(makeFetcher('second'), origin, {
      ...common, channel: bus.port(), instanceId: 'second'
    })

    await first(`${origin}/api/iam/session/login`, { method: 'POST' })
    await new Promise(resolve => setTimeout(resolve, 0))
    calls.length = 0
    await clock.advance(100)
    expect(calls.filter(call => call.endsWith('/heartbeat'))).toHaveLength(1)

    first.close()
    calls.length = 0
    await clock.advance(50)
    expect(calls).toContain('second:/api/iam/session/heartbeat')
    expect(bus.messages.every(message => {
      const keys = Object.keys(message as Record<string, unknown>).sort()
      return JSON.stringify(keys) === JSON.stringify(['timeBucket', 'type', 'version'])
    })).toBe(true)
    expect(JSON.stringify(bus.messages)).not.toMatch(/csrf|token|profile|account|admin/i)
    second.close()
  })

  it('uses a conservative bounded interval without shared lease storage', async () => {
    const clock = new FakeClock()
    const calls: string[] = []
    const fetcher: typeof fetch = async input => {
      calls.push(new URL(String(input), origin).pathname)
      return response(200, { profile, csrfToken: csrf('a') }, csrf('a'))
    }
    const shared = createBrowserSessionFetch(fetcher, origin, {
      now: clock.now,
      setTimer: clock.setTimer,
      clearTimer: clock.clearTimer,
      storage: null,
      channel: null,
      heartbeatIntervalMs: 100,
      renewIntervalMs: 1_000,
      activeWindowMs: 2_000
    })
    await shared(`${origin}/api/iam/session/current`)
    calls.length = 0
    await clock.advance(100)
    expect(calls).toEqual([])
    await clock.advance(100)
    expect(calls).toEqual(['/api/iam/session/heartbeat'])
    shared.close()
  })

  it('invalidates maintenance when the server changes the stable family CSRF', async () => {
    const clock = new FakeClock()
    const requests: Request[] = []
    const fetcher: typeof fetch = async input => {
      requests.push(new Request(new URL(String(input), origin)))
      if (requests.length === 1) return response(200, { profile, csrfToken: csrf('a') }, csrf('a'))
      if (requests.length === 2) return response(200, { profile, csrfToken: csrf('b') }, csrf('b'))
      return response(200, {})
    }
    const shared = createBrowserSessionFetch(fetcher, origin, {
      now: clock.now,
      setTimer: clock.setTimer,
      clearTimer: clock.clearTimer,
      storage: fakeStorage(),
      channel: null,
      instanceId: 'only',
      heartbeatIntervalMs: 100,
      renewIntervalMs: 1_000,
      activeWindowMs: 2_000
    })
    await shared(`${origin}/api/iam/session/current`)
    await clock.advance(100)
    await shared(`${origin}/api/scheduler/definitions`, { method: 'POST' })
    expect(requests[2]?.headers.has('X-CSRF-Token')).toBe(false)
    shared.close()
  })

  it('backs off after maintenance throttling instead of creating a request loop', async () => {
    const clock = new FakeClock()
    const calls: string[] = []
    const fetcher: typeof fetch = async input => {
      const path = new URL(String(input), origin).pathname
      calls.push(path)
      if (path.endsWith('/heartbeat')) return response(429, { code: 'RATE_LIMITED' })
      return response(200, { profile, csrfToken: csrf('a') }, csrf('a'))
    }
    const shared = createBrowserSessionFetch(fetcher, origin, {
      now: clock.now,
      setTimer: clock.setTimer,
      clearTimer: clock.clearTimer,
      storage: fakeStorage(),
      channel: null,
      instanceId: 'only',
      heartbeatIntervalMs: 100,
      renewIntervalMs: 1_000,
      activeWindowMs: 2_000,
      leaderLeaseMs: 50
    })
    await shared(`${origin}/api/iam/session/current`)
    calls.length = 0
    await clock.advance(100)
    expect(calls).toEqual(['/api/iam/session/heartbeat'])
    await clock.advance(1)
    expect(calls).toEqual(['/api/iam/session/heartbeat'])
    await clock.advance(99)
    expect(calls).toEqual(['/api/iam/session/heartbeat', '/api/iam/session/heartbeat'])
    shared.close()
  })

  it('treats channel failures as coordination loss rather than request failure', async () => {
    const fetcher: typeof fetch = async () => response(200, { profile, csrfToken: csrf('a') }, csrf('a'))
    const channel = {
      postMessage: () => { throw new Error('channel unavailable') },
      addEventListener: () => undefined,
      removeEventListener: () => { throw new Error('channel unavailable') },
      close: () => { throw new Error('channel unavailable') }
    }
    const shared = createBrowserSessionFetch(fetcher, origin, { storage: null, channel })
    await expect(shared(`${origin}/api/iam/session/current`)).resolves.toHaveProperty('status', 200)
    expect(() => shared.close()).not.toThrow()
  })
})
