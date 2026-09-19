#!/usr/bin/env node

import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

export function verifyIdentity(identity) {
  assert.equal(identity.schemaVersion, 1)
  assert.equal(identity.identityStatus, 'approved')
  assert.equal(identity.bundleIdentifier, 'com.goadmin.plus')
  assert.equal(identity.architecture, 'x86_64')
  assert.equal(identity.targetTriple, 'x86_64-pc-windows-msvc')
  assert.equal(identity.releaseClass, 'private-release')
  assert.equal(identity.signingRequired, false)
  assert.equal(identity.installerType, 'nsis')
  assert.equal(identity.installMode, 'currentUser')
  assert.equal(identity.webviewInstallMode, 'offlineInstaller')
  assert.deepEqual(identity.uninstallPolicy, {
    preserveAppData: true,
    preserveCredential: true,
    removeInstallDirectory: true
  })
  assert.deepEqual(identity.dataPaths, {
    data: '%LOCALAPPDATA%\\com.goadmin.plus\\data',
    logs: '%LOCALAPPDATA%\\com.goadmin.plus\\logs'
  })
  assert.equal(identity.remotePublish, false)
  assert.deepEqual(identity.evidence, ['SHA256SUMS', 'SPDX JSON', 'provenance.json', 'install-evidence.json'])
}

export function verifyWorkflow(workflow) {
  assert.match(workflow, /tags:\s*[\s\S]*['"]\*\.\*\.\*['"]|tags:\s*\n\s+- ['"]\*\.\*\.\*['"]/
  )
  assert.match(workflow, /windows-2025/)
  assert.match(workflow, /PROCESSOR_ARCHITECTURE/)
  assert.match(workflow, /build-nsis\.ps1/)
  assert.match(workflow, /verify-install\.ps1/)
  assert.match(workflow, /gh release create/)
  assert.doesNotMatch(workflow, /wails|AZURE_|APPLE_|signing|notariz|Authenticode|signed-production/i)
}

export async function verifyRepository(repository) {
  const read = relative => readFile(path.join(repository, relative), 'utf8')
  const [identityText, tauriText, runtime, builder, verifier, installer, artifacts, workflow] = await Promise.all([
    read('release/windows/identity.json'),
    read('frontend/apps/admin-desktop/src-tauri/tauri.conf.json'),
    read('frontend/apps/admin-desktop/src-tauri/src/main.rs'),
    read('scripts/release/windows/build-nsis.ps1'),
    read('scripts/release/windows/verify-artifacts.ps1'),
    read('scripts/release/windows/verify-install.ps1'),
    read('scripts/release/windows/emit-artifacts.ps1'),
    read('.github/workflows/release.yml')
  ])
  const identity = JSON.parse(identityText)
  const tauri = JSON.parse(tauriText)
  verifyIdentity(identity)
  assert.equal(tauri.identifier, identity.bundleIdentifier)
  assert.match(runtime, /app_local_data_dir\(\)/)
  assert.match(builder, /x86_64-pc-windows-msvc/)
  assert.match(builder, /installMode = 'currentUser'/)
  assert.doesNotMatch(builder, /signCommand|artifact-signing|AZURE_/i)
  assert.match(verifier, /Assert-X64Pe/)
  assert.doesNotMatch(verifier, /Authenticode|SignerCertificate|thumbprint/i)
  assert.match(installer, /installDirectory = Join-Path \$env:RUNNER_TEMP/)
  assert.match(installer, /dataRoot = Join-Path \$env:LOCALAPPDATA/)
  assert.match(installer, /installPathSelected = \$true/)
  assert.match(artifacts, /releaseClass = 'private-release'/)
  assert.doesNotMatch(artifacts, /Authenticode|signing|thumbprint/i)
  verifyWorkflow(workflow)
}

const current = fileURLToPath(import.meta.url)
if (process.argv[1] && path.resolve(process.argv[1]) === current) {
  const repository = path.resolve(path.dirname(current), '../../..')
  await verifyRepository(repository)
  console.log('GO_ADMIN_WINDOWS_RELEASE_POLICY_PASS')
}
