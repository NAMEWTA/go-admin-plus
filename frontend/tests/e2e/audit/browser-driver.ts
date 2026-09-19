import { createAuditController, createWebAuditClient, mountAuditPage } from '@go-admin-plus/web-domain-audit'
import { auditFixture } from './fixture'

const assert: (condition: unknown, message: string) => asserts condition = (condition, message) => {
  if (!condition) throw new Error(message)
}

let confirmCleanup = false
const login = await fetch('/api/iam/session/login', {
  method: 'POST',
  headers: { 'Content-Type': 'application/json' },
  body: JSON.stringify({ username: 'admin', password: 'correct horse battery' }),
})
assert(login.ok, 'real IAM login failed')
const loginCSRF = login.headers.get('X-CSRF-Token') ?? ''
assert(/^[A-Za-z0-9_-]{43}$/.test(loginCSRF), 'IAM login CSRF contract failed')
const loginBody = await login.text()
assert(!/(password|sessionToken)/i.test(loginBody), 'IAM login response exposed credential material')
const advance = await fetch('/__test/advance-session', { method: 'POST' })
assert(advance.ok, 'Audit browser Session clock advance failed')
const renew = await fetch('/api/iam/session/renew', { method: 'POST', headers: { 'X-CSRF-Token': loginCSRF } })
assert(renew.ok && renew.headers.get('X-CSRF-Token') === loginCSRF, 'explicit IAM Session renewal failed')
assert(!/(password|sessionToken)/i.test(await renew.text()), 'IAM renewal response exposed credential material')
const controller = createAuditController(createWebAuditClient(fetch, '/api'), async () => confirmCleanup)
mountAuditPage('#app', controller)

const waitFor = async (predicate: () => boolean, message: string) => {
  for (let attempt = 0; attempt < 200; attempt += 1) {
    if (predicate()) return
    await new Promise((resolvePromise) => setTimeout(resolvePromise, 25))
  }
  throw new Error(message)
}

const select = (testID: string, value: string) => {
  const element = document.querySelector<HTMLSelectElement>(`[data-testid="${testID}"]`)
  assert(element, `${testID} is unavailable`)
  element.value = value
  element.dispatchEvent(new Event('change', { bubbles: true }))
}

const click = (testID: string) => {
  const element = document.querySelector<HTMLButtonElement>(`[data-testid="${testID}"]`)
  assert(element, `${testID} is unavailable`)
  element.click()
}

const snapshot = async (): Promise<{ count: number }> => {
  const response = await fetch('/__test/snapshot')
  assert(response.ok, 'Audit browser snapshot failed')
  return response.json() as Promise<{ count: number }>
}

const sessionState = async (): Promise<{ active: number; rotated: number; replacementCookie: boolean; csrf: boolean }> => {
  const response = await fetch('/__test/session-state')
  assert(response.ok, 'Audit browser Session state failed')
  return response.json() as Promise<{ active: number; rotated: number; replacementCookie: boolean; csrf: boolean }>
}

const driver = {
  async run() {
		await waitFor(() => document.querySelectorAll('[data-testid="audit-row"]').length === auditFixture.initialFactCount, 'initial Audit page load failed')
		const rotatedState = await sessionState()
		assert(rotatedState.active === 1 && rotatedState.rotated === 0 && !rotatedState.replacementCookie && rotatedState.csrf, 'stable Session renewal was not propagated to the Audit page')
		const factsResponse = await fetch('/api/audit/records?page=1&pageSize=20')
		assert(factsResponse.ok, 'Audit fact verification request failed')
		const rawFacts = await factsResponse.text()
		assert(!/(payload|businessKey|password|secret|session|credential)/i.test(rawFacts), 'Audit response exposed a private envelope')
		const facts = JSON.parse(rawFacts) as { records: Array<{ kind: string; actorRef?: string; subject: string }> }
		assert(facts.records.some((fact) => fact.kind === 'login' && fact.actorRef === auditFixture.accountRef && /^login:[a-f0-9]{32}$/.test(fact.subject)), 'Audit login actor fact is missing')
		assert(facts.records.some((fact) => fact.kind === 'login' && !fact.actorRef && /^login:[a-f0-9]{32}$/.test(fact.subject)), 'Audit failed login fact is missing')
		assert(facts.records.some((fact) => fact.kind === 'operation' && fact.actorRef === auditFixture.accountRef && fact.subject === 'files:ui-record-revision-2'), 'Audit operation actor fact is missing')
    select('audit-source', 'web')
    select('audit-action', 'update')
    click('audit-search')
    await waitFor(() => controller.list.snapshot().filters.source === 'web' && controller.list.snapshot().total === 1, 'Audit browser filter failed')

    click('audit-view')
    await waitFor(() => Boolean(document.querySelector('[data-testid="audit-detail-dialog"][role="dialog"]')), 'Audit browser detail did not open')
    const detail = document.querySelector('[data-testid="audit-detail-dialog"]')?.textContent ?? ''
		assert(detail.includes('files:ui-record-revision-2') && detail.includes(auditFixture.accountRef) && detail.includes(auditFixture.operationOutcome === 'succeeded' ? '成功' : '失败'), 'Audit browser detail content failed')

    const before = document.querySelector<HTMLInputElement>('[data-testid="audit-cleanup-before"]')
    assert(before, 'Audit cleanup boundary is unavailable')
    before.value = auditFixture.cleanupBefore.slice(0, 10)
    before.dispatchEvent(new Event('input', { bubbles: true }))
		await new Promise((resolvePromise) => setTimeout(resolvePromise, 0))

    confirmCleanup = false
    click('audit-cleanup')
    await new Promise((resolvePromise) => setTimeout(resolvePromise, 50))
		assert((await snapshot()).count === auditFixture.initialFactCount, 'cancelled Audit cleanup changed state')

    confirmCleanup = true
    click('audit-cleanup')
		await waitFor(() => (document.querySelector('[data-testid="audit-cleanup-status"]')?.textContent ?? '').includes('已删除 1 条记录'), 'Audit cleanup status failed')
		assert(document.querySelectorAll('[data-testid="audit-row"]').length === 0, 'filtered Audit list did not refresh after cleanup')
		assert((await snapshot()).count === auditFixture.postCleanupFactCount, 'Audit browser cleanup removed a recent login fact')

		assert((await fetch('/__test/audit-permission?enabled=false', { method: 'POST' })).ok, 'Audit permission revoke failed')
		click('audit-search')
		await waitFor(() => (document.querySelector('[role="alert"]')?.textContent ?? '').includes('没有执行该审计操作的权限'), 'Audit forbidden state was not rendered')
		assert((await fetch('/__test/audit-permission?enabled=true', { method: 'POST' })).ok, 'Audit permission restore failed')
		click('audit-search')
		await waitFor(() => !document.querySelector('[role="alert"]'), 'Audit permission recovery failed')
		assert((await fetch('/__test/revoke-sessions', { method: 'POST' })).ok, 'Session revoke failed')
		click('audit-search')
		await waitFor(() => (document.querySelector('[role="alert"]')?.textContent ?? '').includes('会话已失效，请重新登录'), 'Audit relogin state was not rendered')
    return true
  },
  async shutdown() {
    await fetch('/__test/shutdown', { method: 'POST' })
    return true
  },
}

declare global {
  interface Window { __auditE2E: typeof driver }
}

window.__auditE2E = driver
