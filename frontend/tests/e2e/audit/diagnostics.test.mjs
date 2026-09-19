import assert from 'node:assert/strict'
import test from 'node:test'
import { browserExceptionDiagnostic, createDiagnosticBuffer, redactDiagnostic } from './diagnostics.mjs'

test('diagnostics retain stable failures while redacting credential material', () => {
  const dsn = 'postgres://private-user:private-password@127.0.0.1:55439/postgres?sslmode=disable'
  const diagnostic = redactDiagnostic([
    'Error: filtered Audit list failed',
    dsn,
    'Set-Cookie: __Host-go-admin-session=raw-session-value; Secure',
    'X-CSRF-Token: raw-csrf-value',
    'password="raw password value"',
  ].join('\n'), [dsn, 'known-fixture-password'])

  assert.match(diagnostic, /filtered Audit list failed/)
  for (const secret of ['private-user', 'private-password', 'raw-session-value', 'raw-csrf-value', 'raw password value']) {
    assert.doesNotMatch(diagnostic, new RegExp(secret))
  }
})

test('diagnostic buffers are bounded and browser exception descriptions survive', () => {
  const buffer = createDiagnosticBuffer(32)
  buffer.append('x'.repeat(4_096))
  assert.ok(buffer.text().length > 0 && buffer.text().length <= 32)
  assert.doesNotMatch(buffer.text(), /x/)

  const capped = createDiagnosticBuffer(Number.MAX_SAFE_INTEGER)
  capped.append('safe-line\n'.repeat(10_000))
  assert.ok(capped.text().length <= 65_536)
  assert.match(browserExceptionDiagnostic({ exception: { description: 'Error: cleanup count mismatch' } }), /cleanup count mismatch/)
})

test('stream boundaries cannot reassemble raw diagnostic credentials', () => {
  const dsn = 'postgres://private-user:private-password@127.0.0.1:55439/postgres?sslmode=disable'
  const cases = [
    { line: `database=${dsn}\n`, forbidden: ['private-user', 'private-password'] },
    { line: 'Set-Cookie: __Host-go-admin-session=raw-session-value; Secure\n', forbidden: ['raw-session-value'] },
    { line: 'X-CSRF-Token: raw-csrf-value\n', forbidden: ['raw-csrf-value'] },
    { line: 'password="raw password value"\n', forbidden: ['raw password value'] },
  ]

  for (const { line, forbidden } of cases) {
    for (let split = 1; split < line.length; split += 1) {
      const buffer = createDiagnosticBuffer(512, [dsn])
      buffer.append(line.slice(0, split))
      assert.equal(buffer.text(), '[incomplete output line]')
      buffer.append(line.slice(split))
      const diagnostic = buffer.text()
      for (const secret of forbidden) assert.doesNotMatch(diagnostic, new RegExp(secret))
    }
  }
})
