const sanitize = (value, fallback, maximum) => {
  const message = typeof value === 'string' ? value : value instanceof Error ? value.message : fallback
  return message
    .replace(/\b(?:https?|postgres(?:ql)?):\/\/\S+/gi, '[redacted-url]')
    .replace(/\bBearer\s+\S+/gi, 'Bearer [redacted]')
    .replace(/\b(password|token|secret|cookie|authorization|dsn)\b\s*[:=]\s*\S+/gi, '$1=[redacted]')
    .replace(/[^a-z0-9 .,:;_()[\]#@=?|-]+/gi, '?')
    .replace(/\s+/g, ' ')
    .trim()
    .slice(0, maximum) || fallback
}

export const safeRunnerDiagnostic = (value) => sanitize(value, 'unknown runner failure', 200)
export const safeBrowserDiagnostic = (value) => sanitize(value, 'unknown browser assertion', 200)

const failures = new Set(['relogin', 'forbidden', 'validation', 'conflict', 'unavailable'])
const alertCodes = new Map([
  ['登录状态已失效，请重新登录。', 'relogin'],
  ['当前账号没有执行该操作的权限。', 'forbidden'],
  ['请检查提交内容。', 'validation'],
  ['数据已发生变化或受系统保护。', 'conflict'],
  ['管理服务暂不可用。', 'unavailable'],
])

const requestPhases = new Set(['not-started', 'pending', 'success', 'error'])
const readyStates = new Set(['loading', 'interactive', 'complete'])

export const administrationMountDiagnostic = ({ failure, canUsersRead, rows, total, loading, alertText, manifest, users, readyState, pageMounted, permissionCount, hasUsersRead, hasManifestRead, scope }) => {
  const failureCode = failures.has(failure) ? failure : 'none'
  const alertCode = alertText ? alertCodes.get(alertText) ?? 'unrecognized' : 'none'
  const count = (value) => Number.isSafeInteger(value) && value >= 0 ? value : -1
  const phase = (value) => requestPhases.has(value) ? value : 'not-started'
  const documentState = readyStates.has(readyState) ? readyState : 'loading'
  const scopeCode = scope === 'all' || scope === 'self' ? scope : 'unknown'
  return `administration mount timeout f=${failureCode} can=${Boolean(canUsersRead)} rows=${count(rows)} total=${count(total)} load=${Boolean(loading)} alert=${alertCode} manifest=${phase(manifest)} users=${phase(users)} pc=${count(permissionCount)} ur=${Boolean(hasUsersRead)} mr=${Boolean(hasManifestRead)} scope=${scopeCode} ready=${documentState} mounted=${Boolean(pageMounted)}`
}

export const browserDiagnostic = (output) => {
  const match = output.match(/IAM_ADMIN_E2E_FAIL\|ASSERTION\|([^<\r\n]{1,200})/)
  return match ? safeRunnerDiagnostic(match[1]) : 'safe browser diagnostic unavailable'
}

export const runnerFailureLine = (value) => `IAM_ADMIN_E2E_RUN_FAIL|${safeRunnerDiagnostic(value)}`
