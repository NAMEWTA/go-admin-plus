import { createAuditController, createWebAuditClient } from '@go-admin-plus/web-domain-audit'
import { auditFixture } from './fixture'

const baseURL = process.env.GO_ADMIN_AUDIT_E2E_BASE_URL
if (!baseURL) throw new Error('Audit E2E base URL is required')

const assert: (condition: unknown, message: string) => asserts condition = (condition, message) => {
  if (!condition) throw new Error(message)
}

const snapshot = async (): Promise<{ count: number }> => {
  const response = await fetch(new URL('/__test/snapshot', baseURL))
  assert(response.ok, 'Audit E2E snapshot failed')
  return response.json() as Promise<{ count: number }>
}

const shutdown = async () => {
  await fetch(new URL('/__test/shutdown', baseURL), { method: 'POST' })
}

const run = async () => {
  const login = await fetch(new URL('/api/iam/session/login', baseURL), {
    method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ username: 'admin', password: 'correct horse battery' }),
  })
  assert(login.ok, 'Audit E2E IAM login failed')
  const cookie = login.headers.get('set-cookie')?.split(';', 1)[0]
  assert(cookie, 'Audit E2E IAM cookie is missing')
  const authenticatedFetch: typeof fetch = async (input, init) => {
    const request = new Request(input, init)
    const headers = new Headers(request.headers)
    headers.set('Cookie', cookie)
    return fetch(new Request(request, { headers }))
  }
  const client = createWebAuditClient(authenticatedFetch, new URL('/api', baseURL).toString())
  const denied = createAuditController(client, async () => false)
  assert(await denied.cleanup(auditFixture.cleanupBefore) === 'cancelled', 'Audit E2E confirmation rejection failed')
  assert((await snapshot()).count === auditFixture.initialFactCount, 'Audit E2E rejected cleanup changed state')

  const controller = createAuditController(client, async () => true)
  await controller.list.search({ source: 'web', action: 'update' })
  const listed = controller.list.snapshot()
  assert(listed.total === 1 && listed.rows[0]?.subject === 'files:ui-record-revision-2' && listed.rows[0]?.actorRef === auditFixture.accountRef, 'Audit E2E filtered list failed')
  const detail = await controller.detail(listed.rows[0].id)
  assert(detail.action === 'update' && detail.source === 'web', 'Audit E2E detail failed')
  assert(!JSON.stringify(detail).includes('payload') && !JSON.stringify(detail).includes('businessKey'), 'Audit E2E detail leaked envelope')

  assert(await controller.cleanup(auditFixture.cleanupBefore) === 'completed', 'Audit E2E cleanup failed')
  assert(controller.lastCleanup()?.deleted === 1, 'Audit E2E cleanup result failed')
  assert(controller.list.snapshot().total === 0, 'Audit E2E cleanup did not refresh list')
  assert((await snapshot()).count === auditFixture.postCleanupFactCount, 'Audit E2E cleanup removed recent login facts')
}

try {
  await run()
  console.log('AUDIT_E2E_PROFILE_PASS')
} finally {
  await shutdown().catch(() => undefined)
}
