import assert from 'node:assert/strict'
import test from 'node:test'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

import { verifyIdentity, verifyRepository, verifyWorkflow } from './verify-policy.mjs'

const repository = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../../..')

test('repository macOS release policy targets Apple Silicon self-use', async () => {
  await verifyRepository(repository)
})

test('identity rejects Intel or signed release drift', () => {
  assert.throws(() => verifyIdentity({
    schemaVersion: 1,
    identityStatus: 'approved',
    bundleIdentifier: 'com.goadmin.plus',
    architectures: ['x86_64', 'arm64'],
    targetTriple: 'aarch64-apple-darwin',
    releaseClass: 'private-release',
    signingRequired: false,
    notarizationRequired: false,
    hardenedRuntimeRequired: false,
    remotePublish: false,
    evidence: ['SHA256SUMS', 'SPDX JSON', 'provenance.json']
  }))
})

test('workflow rejects signing or non-tag release regressions', () => {
  const invalid = `
tags:
  - '*.*.*'
macos-15
aarch64-apple-darwin
build.sh
--no-sign
gh release create
APPLE_SIGNING_IDENTITY
`
  assert.throws(() => verifyWorkflow(invalid))
})
