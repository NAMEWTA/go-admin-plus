import assert from 'node:assert/strict'
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { test } from 'node:test'
import { images, runRequiredSecurity, securityPlan, verifySecurityArtifacts } from './required-security.mjs'

test('pins secret and SBOM scanners by immutable digest', () => {
  for (const image of Object.values(images)) assert.match(image, /^[^@]+@sha256:[0-9a-f]{64}$/)
})

test('declares every required security and drift step without allow-failure syntax', () => {
  const plan = securityPlan('/repo')
  const names = plan.map(step => step.name)
  assert.deepEqual(names, ['govulncheck', 'pnpm-production-audit', 'cargo-audit', 'cargo-deny', 'secret-scan', 'sbom', 'generate-drift'])
  assert.deepEqual(plan.find(step => step.name === 'cargo-audit').args, ['audit'])
  assert.deepEqual(plan.find(step => step.name === 'cargo-deny').args, ['deny', '--config', join('/repo', 'scripts/security/deny.toml'), 'check', 'bans', 'sources'])
  assert.ok(plan.find(step => step.name === 'secret-scan').args.includes('--config=/repo/scripts/security/gitleaks.toml'))
  assert.ok(!plan.find(step => step.name === 'secret-scan').args.includes('--no-git'))
  for (const path of ['./.git/**', './artifacts/**', './frontend/apps/admin-desktop/src-tauri/target/**']) {
    assert.ok(plan.find(step => step.name === 'sbom').args.includes(path))
  }
  assert.doesNotMatch(JSON.stringify(plan), /\|\| true|continue-on-error/)
})

test('requires the explicit security flag and nonempty plan', () => {
  assert.throws(() => runRequiredSecurity({ root: '/repo', environment: {}, plan: [], spawn: () => ({ status: 0 }), verify: () => {} }), /REQUIRE_SECURITY/)
  assert.throws(() => runRequiredSecurity({ root: '/repo', environment: { GO_ADMIN_CI_REQUIRE_SECURITY: '1' }, plan: [], spawn: () => ({ status: 0 }), verify: () => {} }), /zero commands/)
})

test('stops on a scan finding or tool failure and verifies artifacts after all commands', () => {
  const plan = [{ name: 'first', command: 'first', args: [], cwd: '/repo' }, { name: 'finding', command: 'finding', args: [], cwd: '/repo' }]
  const root = mkdtempSync(join(tmpdir(), 'required-security-test-'))
  test.after(() => rmSync(root, { recursive: true, force: true }))
  let calls = 0, verified = false
  assert.throws(() => runRequiredSecurity({ root, environment: { GO_ADMIN_CI_REQUIRE_SECURITY: '1' }, plan, spawn: () => ({ status: ++calls === 2 ? 1 : 0 }), verify: () => { verified = true } }), /finding failed/)
  assert.equal(calls, 2); assert.equal(verified, false)
})

test('rejects secret findings and empty SBOMs while accepting complete evidence', t => {
  const root = mkdtempSync(join(tmpdir(), 'required-security-artifacts-'))
  t.after(() => rmSync(root, { recursive: true, force: true }))
  const directory = join(root, 'artifacts/security')
  mkdirSync(directory, { recursive: true })
  writeFileSync(join(directory, 'gitleaks.json'), '[{"RuleID":"test"}]')
  writeFileSync(join(directory, 'repository.cdx.json'), '{"bomFormat":"CycloneDX","components":[{"name":"fixture"}]}')
  assert.throws(() => verifySecurityArtifacts(root), /zero findings/)
  writeFileSync(join(directory, 'gitleaks.json'), '[]')
  writeFileSync(join(directory, 'repository.cdx.json'), '{"bomFormat":"CycloneDX","components":[]}')
  assert.throws(() => verifySecurityArtifacts(root), /contain components/)
  writeFileSync(join(directory, 'repository.cdx.json'), '{"bomFormat":"CycloneDX","components":[{"name":"fixture"}]}')
  assert.doesNotThrow(() => verifySecurityArtifacts(root))
})
