export const ADMIN_THEME_STORAGE_KEY = 'go-admin-plus.admin-theme'

export type ThemePreference = 'light' | 'dark' | 'system'
export type ThemeDensity = 'comfortable' | 'compact'
export type ResolvedTheme = Exclude<ThemePreference, 'system'>

export interface ThemeSnapshot {
  readonly preference: ThemePreference
  readonly density: ThemeDensity
  readonly resolved: ResolvedTheme
}

export interface ThemeController {
  snapshot(): ThemeSnapshot
  setPreference(preference: ThemePreference): void
  setDensity(density: ThemeDensity): void
  destroy(): void
}

export interface ThemeControllerOptions {
  readonly storage: Pick<Storage, 'getItem' | 'setItem'>
  readonly root: Pick<Element, 'removeAttribute' | 'setAttribute'>
  readonly media: {
    readonly matches: boolean
    subscribe(listener: (dark: boolean) => void): () => void
  }
}

const isPreference = (value: unknown): value is ThemePreference =>
  value === 'light' || value === 'dark' || value === 'system'

const isDensity = (value: unknown): value is ThemeDensity =>
  value === 'comfortable' || value === 'compact'

const readPreference = (storage: ThemeControllerOptions['storage']): Pick<ThemeSnapshot, 'density' | 'preference'> => {
  try {
    const stored = JSON.parse(storage.getItem(ADMIN_THEME_STORAGE_KEY) ?? 'null') as Record<string, unknown> | null
    return {
      preference: isPreference(stored?.preference) ? stored.preference : 'system',
      density: isDensity(stored?.density) ? stored.density : 'comfortable'
    }
  } catch {
    return { preference: 'system', density: 'comfortable' }
  }
}

const browserOptions = (): ThemeControllerOptions => {
  const query = window.matchMedia('(prefers-color-scheme: dark)')
  return {
    storage: window.localStorage,
    root: document.documentElement,
    media: {
      matches: query.matches,
      subscribe(listener) {
        const handleChange = (event: MediaQueryListEvent) => listener(event.matches)
        query.addEventListener('change', handleChange)
        return () => query.removeEventListener('change', handleChange)
      }
    }
  }
}

export const createThemeController = (provided?: ThemeControllerOptions): ThemeController => {
  const options = provided ?? browserOptions()
  let { density, preference } = readPreference(options.storage)
  let systemDark = options.media.matches

  const resolved = (): ResolvedTheme => preference === 'system' ? (systemDark ? 'dark' : 'light') : preference
  const apply = () => {
    options.root.setAttribute('data-theme', resolved())
    options.root.setAttribute('data-density', density)
  }
  const persist = () => {
    try {
      options.storage.setItem(ADMIN_THEME_STORAGE_KEY, JSON.stringify({ density, preference }))
    } catch {
      // Storage can be disabled by browser policy; the in-memory preference still applies.
    }
  }
  const unsubscribe = options.media.subscribe((dark) => {
    systemDark = dark
    if (preference === 'system') apply()
  })

  apply()
  let destroyed = false
  return {
    snapshot: () => ({ density, preference, resolved: resolved() }),
    setPreference(next) {
      preference = next
      persist()
      apply()
    },
    setDensity(next) {
      density = next
      persist()
      apply()
    },
    destroy() {
      if (destroyed) return
      destroyed = true
      unsubscribe()
    }
  }
}
