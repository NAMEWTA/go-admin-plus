#!/usr/bin/env node

import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

export function verifyIdentity(identity) {
  assert.equal(identity.schemaVersion, 1)
  assert.equal(identity.identityStatus, 'approved')
  assert.equal(identity.bundleIdentifier, 'com.goadmin.plus')
  assert.deepEqual(identity.architectures, ['arm64', 'x86_64'])
  assert.deepEqual(identity.targetTriples, ['aarch64-apple-darwin', 'x86_64-apple-darwin'])
  assert.equal(identity.releaseClass, 'private-release')
  assert.equal(identity.signingRequired, false)
  assert.equal(identity.notarizationRequired, false)
  assert.equal(identity.hardenedRuntimeRequired, false)
  assert.equal(identity.remotePublish, false)
  assert.deepEqual(identity.evidence, ['SHA256SUMS', 'SPDX JSON', 'provenance.json'])
}

export function verifyWorkflow(workflow) {
  assert.match(workflow, /tags:\s*[\s\S]*['"]\*\.\*\.\*['"]|tags:\s*\n\s+- ['"]\*\.\*\.\*['"]/
  )
  assert.match(workflow, /macos-15/)
  assert.match(workflow, /uname -m/)
  assert.match(workflow, /aarch64-apple-darwin/)
  assert.match(workflow, /build\.sh/)
  assert.match(workflow, /verify-dmg\.sh/)
  assert.match(workflow, /gh release create/)
  assert.doesNotMatch(workflow, /APPLE_SIGNING|notarize|sign-app|universal-apple-darwin/)
}

export async function verifyRepository(repository) {
  const read = relative => readFile(path.join(repository, relative), 'utf8')
  const [identityText, tauriConfigText, runtime, builder, verifier, installer, artifacts, workflow] = await Promise.all([
    read('release/macos/identity.json'),
    read('frontend/apps/admin-desktop/src-tauri/tauri.conf.json'),
    read('frontend/apps/admin-desktop/src-tauri/src/main.rs'),
    read('scripts/release/macos/build.sh'),
    read('scripts/release/macos/verify-app.sh'),
    read('scripts/release/macos/verify-install.sh'),
    read('scripts/release/macos/emit-artifacts.sh'),
    read('.github/workflows/release.yml')
  ])
  const identity = JSON.parse(identityText)
  const tauri = JSON.parse(tauriConfigText)
  verifyIdentity(identity)
  assert.equal(tauri.identifier, identity.bundleIdentifier)
  assert.match(runtime, /app_local_data_dir\(\)/)
  assert.match(runtime, /app_log_dir\(\)/)
  assert.match(builder, /aarch64-apple-darwin/)
  assert.match(builder, /--no-sign/)
  assert.doesNotMatch(builder, /universal-apple-darwin|lipo -create/)
  assert.match(verifier, /private-release/)
  assert.doesNotMatch(verifier, /codesign|spctl|xattr/)
  assert.match(installer, /install_root=.*Applications\/Go Admin Plus\.app/)
  assert.match(installer, /Library\/Application Support/)
  assert.match(installer, /launch_and_stop restart/)
  assert.match(artifacts, /go-admin-plus-macos-\$artifact_arch\.app\.zip/)
  assert.match(artifacts, /releaseClass:\"private-release\"/)
  assert.doesNotMatch(artifacts, /notary|signed:true|signed-production|codesign/i)
  verifyWorkflow(workflow)
}

const current = fileURLToPath(import.meta.url)
if (process.argv[1] && path.resolve(process.argv[1]) === current) {
  const repository = path.resolve(path.dirname(current), '../../..')
  await verifyRepository(repository)
  console.log('GO_ADMIN_MACOS_RELEASE_POLICY_PASS')
}
