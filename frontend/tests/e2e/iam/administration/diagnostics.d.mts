export function safeRunnerDiagnostic(value: unknown): string
export function safeBrowserDiagnostic(value: unknown): string
export function browserDiagnostic(output: string): string
export function runnerFailureLine(value: unknown): string
export function administrationMountDiagnostic(state: {
  failure: string | null
  canUsersRead: boolean
  rows: number
  total: number
  loading: boolean
  alertText: string | null
  manifest: 'not-started' | 'pending' | 'success' | 'error'
  users: 'not-started' | 'pending' | 'success' | 'error'
  readyState: DocumentReadyState
  pageMounted: boolean
  permissionCount: number
  hasUsersRead: boolean
  hasManifestRead: boolean
  scope: 'all' | 'self' | 'unknown'
}): string
