import type { ShellRuntimePort } from '@go-admin-plus/platform'

export type ShellState =
  | { readonly kind: 'authenticated', readonly path: string, readonly subjectId: string }
  | { readonly kind: 'unauthenticated', readonly redirectTo: '/login' }
  | { readonly kind: 'unauthorized', readonly path: string }
  | { readonly kind: 'not-found', readonly path: string }
  | { readonly kind: 'adapter-failed', readonly retryable: true }

/** Resolves navigation into a display-safe state and never exposes adapter errors. */
export const resolveShellState = async (
  runtime: ShellRuntimePort,
  path: string,
  signal?: AbortSignal
): Promise<ShellState> => {
  try {
    const identity = await runtime.loadIdentity({ signal })
    if (identity.kind === 'unauthenticated') {
      return { kind: 'unauthenticated', redirectTo: '/login' }
    }

    const navigation = await runtime.loadNavigation({ signal })
    const route = navigation.find(entry => entry.path === path)
    if (!route) return { kind: 'not-found', path }
    if (route.permission && !identity.permissions.includes(route.permission)) {
      return { kind: 'unauthorized', path }
    }

    return { kind: 'authenticated', path, subjectId: identity.subjectId }
  } catch {
    return { kind: 'adapter-failed', retryable: true }
  }
}

export interface ShellNavigationSink {
  setLoading(loading: boolean): void
  commit(path: string, state: ShellState): void
}

export interface ShellNavigator {
  navigate(path: string): Promise<void>
  invalidate(): void
}

/** Coordinates replaceable shell requests so stale work cannot update the host view. */
export const createShellNavigator = (
  runtime: ShellRuntimePort,
  sink: ShellNavigationSink
): ShellNavigator => {
  let activeRequest: AbortController | undefined
  let invalidated = false
  let requestSequence = 0

  return {
    async navigate(path) {
      if (invalidated) return
      const sequence = ++requestSequence
      activeRequest?.abort()
      const request = new AbortController()
      activeRequest = request
      sink.setLoading(true)

      const state = await resolveShellState(runtime, path, request.signal)
      if (invalidated || request.signal.aborted || sequence !== requestSequence) return

      try {
        sink.commit(path, state)
      } finally {
        if (!invalidated && !request.signal.aborted && sequence === requestSequence) {
          sink.setLoading(false)
        }
        if (activeRequest === request) activeRequest = undefined
      }
    },
    invalidate() {
      invalidated = true
      requestSequence += 1
      activeRequest?.abort()
      activeRequest = undefined
    }
  }
}
