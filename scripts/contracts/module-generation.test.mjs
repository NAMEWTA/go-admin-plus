import assert from 'node:assert/strict'
import { existsSync, mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import test from 'node:test'
import { fileURLToPath } from 'node:url'
import { generate } from './cli.mjs'

const directory = dirname(fileURLToPath(import.meta.url))

test('generates Go and TypeScript transports for a module contract', () => {
  const outputRoot = mkdtempSync(join(tmpdir(), 'go-admin-module-generation-'))
  try {
    const outputs = generate(outputRoot, [join(directory, 'fixtures', 'valid-module.yaml')])
    const goOutput = join('backend', 'internal', 'modules', 'contract-fixture', 'transport', 'openapi.gen.go')
    const typescriptRoot = join('frontend', 'packages', 'domains', 'contract-fixture', 'src', 'generated')

    assert.ok(outputs.includes(goOutput))
    assert.ok(outputs.includes(join(typescriptRoot, 'schema.ts')))
    assert.ok(outputs.includes(join(typescriptRoot, 'client.ts')))
    assert.ok(outputs.includes(join(dirname(goOutput), 'openapi.manifest.json')))
    assert.match(readFileSync(join(outputRoot, goOutput), 'utf8'), /package contractfixturetransport/)
    assert.ok(existsSync(join(outputRoot, typescriptRoot, 'schema.ts')))
    assert.ok(existsSync(join(outputRoot, typescriptRoot, 'client.ts')))
    assert.match(readFileSync(join(outputRoot, typescriptRoot, 'client.ts'), 'utf8'), /@go-admin-plus\/api-client\/contract/)
  } finally {
    rmSync(outputRoot, { recursive: true, force: true })
  }
})
