import assert from 'node:assert/strict'
import { mkdirSync, mkdtempSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { test } from 'node:test'

import { checkDocumentSource, checkDocumentation, checkMarkdownLinks } from './docs-check.mjs'

test('rejects credential examples and obsolete initialization commands', () => {
  const source = [
    'administrator password: changeme',
    'postgres://operator:secret@localhost/product',
    'go-admin-plus server --password leaked',
    'init-db'
  ].join('\n')
  const failures = checkDocumentSource('docs/example.md', source)
  assert.deepEqual(failures, [
    'fixed administrator credential remains in docs/example.md',
    'credential-bearing PostgreSQL URL remains in docs/example.md',
    'password argv example remains in docs/example.md',
    'obsolete database initializer remains in docs/example.md',
    'obsolete server command remains in docs/example.md'
  ])
})

test('requires the complete three-profile release contract', () => {
  const failures = checkDocumentSource('docs/release.md', '# Release\nserver-sqlite\n')
  assert.ok(failures.some(message => message.includes('three-profile clean-room')))
  assert.ok(failures.some(message => message.includes('server-postgres')))
  assert.ok(failures.some(message => message.includes('desktop-sqlite')))
  assert.ok(failures.some(message => message.includes('not-required')))
  assert.ok(failures.some(message => message.includes('task release:verify')))
})

test('checks relative Markdown links without rejecting anchors or external URLs', () => {
  const root = mkdtempSync(join(tmpdir(), 'go-admin-docs-'))
  const readme = join(root, 'README.md')
  const target = join(root, 'target.md')
  writeFileSync(target, '# Target\n')
  writeFileSync(readme, '[ok](target.md#section) [web](https://example.test) [bad](missing.md)\n')
  assert.deepEqual(checkMarkdownLinks(root, [readme]), ['broken documentation link: README.md -> missing.md'])
})

test('scans release documents and workspace README files', () => {
  const root = mkdtempSync(join(tmpdir(), 'go-admin-docs-'))
  mkdirSync(join(root, 'release/linux'), { recursive: true })
  mkdirSync(join(root, 'frontend/packages/example'), { recursive: true })
  mkdirSync(join(root, 'frontend/node_modules/vendor'), { recursive: true })
  writeFileSync(join(root, 'README.md'), '# Product\n')
  writeFileSync(join(root, 'release/linux/README.md'), 'administrator password: changeme\n')
  writeFileSync(join(root, 'frontend/packages/example/README.md'), 'go-admin-plus server --password leaked\n')
  writeFileSync(join(root, 'frontend/node_modules/vendor/README.md'), '[broken](missing.md)\n')

  const failures = checkDocumentation(root)
  assert.ok(failures.some(message => message.includes('release/linux/README.md')))
  assert.ok(failures.some(message => message.includes('frontend/packages/example/README.md')))
})
