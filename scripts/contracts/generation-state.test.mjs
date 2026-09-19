import assert from 'node:assert/strict'
import {
  existsSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  readdirSync,
  rmSync,
  symlinkSync,
  writeFileSync
} from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import test from 'node:test'
import { fileURLToPath } from 'node:url'
import { checkGeneration, synchronizeGeneration } from './cli.mjs'

const directory = dirname(fileURLToPath(import.meta.url))
const fixture = name => join(directory, 'fixtures', name)
const generatedTemporaryDirectories = root => readdirSync(root)
  .filter(name => /^go-admin-(?:contract|generate|module|oapi)/.test(name))
  .sort()

test('failed generation leaves existing outputs and temporary resources unchanged', () => {
  const outputRoot = mkdtempSync(join(tmpdir(), 'contract-state-output-'))
  const isolatedTemporaryRoot = mkdtempSync(join(tmpdir(), 'contract-generator-tmp-'))
  const previousTemporaryRoot = process.env.TMPDIR
  process.env.TMPDIR = isolatedTemporaryRoot
  try {
    synchronizeGeneration(outputRoot, [fixture('valid-module.yaml')])
    const manifest = join(outputRoot, 'scripts', 'contracts', 'generated', 'manifest.json')
    const beforeManifest = readFileSync(manifest, 'utf8')

    assert.throws(
      () => synchronizeGeneration(outputRoot, [fixture('invalid-module-owner.yaml')]),
      /owner|output/i
    )

    assert.equal(readFileSync(manifest, 'utf8'), beforeManifest)
    assert.deepEqual(generatedTemporaryDirectories(isolatedTemporaryRoot), [])
  } finally {
    if (previousTemporaryRoot === undefined) delete process.env.TMPDIR
    else process.env.TMPDIR = previousTemporaryRoot
    rmSync(outputRoot, { recursive: true, force: true })
    rmSync(isolatedTemporaryRoot, { recursive: true, force: true })
  }
})

test('detects and contracts outputs left by a removed module fragment', () => {
  const outputRoot = mkdtempSync(join(tmpdir(), 'contract-state-output-'))
  try {
    synchronizeGeneration(outputRoot, [fixture('valid-module.yaml')])
    assert.throws(() => checkGeneration(outputRoot, []), /manifest\.json|drift/i)

    synchronizeGeneration(outputRoot, [])
    assert.doesNotThrow(() => checkGeneration(outputRoot, []))
    assert.equal(existsSync(join(
      outputRoot,
      'frontend/packages/domains/contract-fixture/src/generated/client.ts'
    )), false)
  } finally {
    rmSync(outputRoot, { recursive: true, force: true })
  }
})

test('detects and contracts orphaned module outputs when the fragment manifest is also removed', () => {
  const outputRoot = mkdtempSync(join(tmpdir(), 'contract-state-output-'))
  try {
    synchronizeGeneration(outputRoot, [fixture('valid-module.yaml')])
    rmSync(join(
      outputRoot,
      'backend/internal/modules/contract-fixture/transport/openapi.manifest.json'
    ))

    assert.throws(() => checkGeneration(outputRoot, []), /openapi\.gen\.go|drift/i)
    synchronizeGeneration(outputRoot, [])
    assert.equal(existsSync(join(
      outputRoot,
      'frontend/packages/domains/contract-fixture/src/generated/client.ts'
    )), false)
  } finally {
    rmSync(outputRoot, { recursive: true, force: true })
  }
})

test('keeps the shared manifest stable when module fragments are added', () => {
  const outputRoot = mkdtempSync(join(tmpdir(), 'contract-state-output-'))
  try {
    synchronizeGeneration(outputRoot, [])
    const sharedManifest = join(outputRoot, 'scripts', 'contracts', 'generated', 'manifest.json')
    const canonical = readFileSync(sharedManifest, 'utf8')

    synchronizeGeneration(outputRoot, [fixture('valid-module.yaml')])

    assert.equal(readFileSync(sharedManifest, 'utf8'), canonical)
    assert.ok(existsSync(join(
      outputRoot,
      'backend/internal/modules/contract-fixture/transport/openapi.manifest.json'
    )))
  } finally {
    rmSync(outputRoot, { recursive: true, force: true })
  }
})

test('regenerates a legal nested owner path without manifest drift', () => {
  const outputRoot = mkdtempSync(join(tmpdir(), 'contract-state-output-'))
  try {
    const contracts = [fixture('valid-nested-module.yaml')]
    synchronizeGeneration(outputRoot, contracts)
    assert.doesNotThrow(() => synchronizeGeneration(outputRoot, contracts))
    assert.doesNotThrow(() => checkGeneration(outputRoot, contracts))
  } finally {
    rmSync(outputRoot, { recursive: true, force: true })
  }
})

test('rejects a manifest path outside the generated owner grammar without deleting it', () => {
  const outputRoot = mkdtempSync(join(tmpdir(), 'contract-state-output-'))
  try {
    synchronizeGeneration(outputRoot, [])
    const unmanagedPaths = [
      'frontend/packages/domains/iam/manual/generated/client.ts',
      'backend/internal/modules/transport/openapi.gen.go'
    ]
    for (const path of unmanagedPaths) {
      const unmanaged = join(outputRoot, path)
      mkdirSync(dirname(unmanaged), { recursive: true })
      writeFileSync(unmanaged, `keep ${path}`)
    }

    const manifestPath = join(outputRoot, 'scripts/contracts/generated/manifest.json')
    const manifest = JSON.parse(readFileSync(manifestPath, 'utf8'))
    manifest.outputs.push(...unmanagedPaths)
    writeFileSync(manifestPath, `${JSON.stringify(manifest, null, 2)}\n`)

    assert.throws(() => synchronizeGeneration(outputRoot, []), /exactly the canonical output paths/)
    for (const path of unmanagedPaths) {
      assert.equal(readFileSync(join(outputRoot, path), 'utf8'), `keep ${path}`)
    }
  } finally {
    rmSync(outputRoot, { recursive: true, force: true })
  }
})

test('rejects a module manifest that claims a sibling slice output', () => {
  const outputRoot = mkdtempSync(join(tmpdir(), 'contract-state-output-'))
  try {
    synchronizeGeneration(outputRoot, [fixture('valid-nested-module.yaml')])
    const manifestPath = join(
      outputRoot,
      'backend/internal/modules/iam/session_v2/transport/openapi.manifest.json'
    )
    const manifest = JSON.parse(readFileSync(manifestPath, 'utf8'))
    manifest.outputs[3] = 'frontend/packages/domains/iam/src/administration/generated/client.ts'
    writeFileSync(manifestPath, `${JSON.stringify(manifest, null, 2)}\n`)

    assert.throws(
      () => synchronizeGeneration(outputRoot, [fixture('valid-nested-module.yaml')]),
      /exactly its own generated outputs/
    )
  } finally {
    rmSync(outputRoot, { recursive: true, force: true })
  }
})

test('rejects a symbolic-link output ancestor without writing outside the output root', () => {
  const outputRoot = mkdtempSync(join(tmpdir(), 'contract-state-output-'))
  const outsideRoot = mkdtempSync(join(tmpdir(), 'contract-state-outside-'))
  try {
    const sentinel = join(outsideRoot, 'sentinel.txt')
    writeFileSync(sentinel, 'preserve outside data')
    const modulesRoot = join(outputRoot, 'backend', 'internal', 'modules')
    mkdirSync(modulesRoot, { recursive: true })
    symlinkSync(
      outsideRoot,
      join(modulesRoot, 'contract-fixture'),
      process.platform === 'win32' ? 'junction' : 'dir'
    )

    assert.throws(
      () => synchronizeGeneration(outputRoot, [fixture('valid-module.yaml')]),
      /symbolic link/
    )
    assert.deepEqual(readdirSync(outsideRoot), ['sentinel.txt'])
    assert.equal(readFileSync(sentinel, 'utf8'), 'preserve outside data')
  } finally {
    rmSync(outputRoot, { recursive: true, force: true })
    rmSync(outsideRoot, { recursive: true, force: true })
  }
})
