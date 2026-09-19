export type PermissionCode = `${string}.${string}` | `${string}.${string}.${string}`
export type DataScope = 'self' | 'all'

export type RuntimeIdentity =
  | { readonly kind: 'unauthenticated' }
  | {
      readonly kind: 'authenticated'
      readonly subjectId: string
      readonly permissions: ReadonlyArray<PermissionCode>
      readonly dataScope: DataScope
    }

export interface NavigationEntry {
  readonly path: `/${string}` | ''
  readonly id?: string
  readonly parentId?: string
  readonly kind?: 'directory' | 'page'
  readonly label?: string
  readonly routeKey?: string
  readonly sortOrder?: number
  readonly icon?: string
  readonly permission?: PermissionCode
}

export interface RuntimeRequest {
  readonly signal?: AbortSignal
}

export interface ShellRuntimePort {
  /** Returns identity facts only; session material remains inside the adapter. */
  loadIdentity(request?: RuntimeRequest): Promise<RuntimeIdentity>
  loadNavigation(request?: RuntimeRequest): Promise<ReadonlyArray<NavigationEntry>>
}

export type HostCapability = 'clipboard-write' | 'file-open' | 'file-save' | 'notification'

export interface HostFile {
  readonly name: string
  readonly mediaType: string
  readonly bytes: Uint8Array
}

export type HostFileSaveResult = 'saved' | 'cancelled'

export interface PlatformPort {
  readonly runtime: 'web' | 'desktop'
  /** 原生文件传输：文件内容保留在宿主进程内。 */
  uploadManagedFile?(): Promise<boolean>
  downloadManagedFile?(id: string, name: string): Promise<boolean>
  /** Callers must check capability presence before invoking an optional host operation. */
  listCapabilities(): ReadonlySet<HostCapability>
  /** Selects one bounded product file without exposing a native filesystem path. */
  pickFile(): Promise<HostFile | null>
  /** Saves one bounded product file and reports an explicit user cancellation. */
  saveFile(file: HostFile): Promise<HostFileSaveResult>
  notify(message: string): Promise<void>
  writeClipboard(text: string): Promise<void>
}

/** 两个宿主共用展示协议；组件只能从产品编译时路由表选择。 */
export const parseNavigationEntries = (value: unknown): ReadonlyArray<NavigationEntry> => {
 if (!Array.isArray(value)) throw new Error('invalid navigation')
 const entries: NavigationEntry[] = value.map(raw => {
  if (!raw || typeof raw !== 'object') throw new Error('invalid navigation entry')
  const v = raw as Record<string, unknown>
  const directory = v.kind === 'directory'
  if (typeof v.path !== 'string' || (directory ? v.path !== '' : !/^\/[a-z0-9][a-z0-9/_-]*$/.test(v.path))) throw new Error('invalid navigation entry')
  const permission = v.permission ?? v.permissionCode
  if (!directory && permission !== undefined && (typeof permission !== 'string' || !/^[a-z][a-z0-9-]*(\.[a-z][a-z0-9-]*)+$/.test(permission))) throw new Error('invalid navigation entry')
  for (const key of ['id','parentId','label','routeKey','icon']) if (v[key] !== undefined && (typeof v[key] !== 'string' || (v[key] as string).length > 160)) throw new Error('invalid navigation metadata')
  if (v.sortOrder !== undefined && (!Number.isInteger(v.sortOrder) || Number(v.sortOrder) < 0)) throw new Error('invalid navigation order')
  return { path: v.path as NavigationEntry['path'], ...(permission ? { permission: permission as PermissionCode } : {}),
   ...(v.id !== undefined ? {id: v.id as string} : {}), ...(v.parentId !== undefined ? {parentId: v.parentId as string} : {}),
   ...(v.kind !== undefined ? {kind: directory ? 'directory' as const : 'page' as const} : {}),
   ...(v.label !== undefined ? {label: v.label as string} : {}), ...(v.routeKey !== undefined ? {routeKey: v.routeKey as string} : {}),
   ...(v.sortOrder !== undefined ? {sortOrder: Number(v.sortOrder)} : {}), ...(v.icon !== undefined ? {icon: v.icon as string} : {}) }
 })
 const pages=entries.filter(v=>v.path)
 if (new Set(pages.map(v=>v.path)).size !== pages.length) throw new Error('duplicate navigation path')
 return entries
}
