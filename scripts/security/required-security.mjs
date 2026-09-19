#!/usr/bin/env node

import { mkdirSync, readFileSync } from 'node:fs'
import { spawnSync } from 'node:child_process'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

export const images = Object.freeze({
  gitleaks: 'ghcr.io/gitleaks/gitleaks:v8.30.1@sha256:c00b6bd0aeb3071cbcb79009cb16a60dd9e0a7c60e2be9ab65d25e6bc8abbb7f',
  syft: 'anchore/syft:v1.50.0@sha256:1288ea4c8b38767b4e620c1e312c8cb26b6e887a99b4f07ab6cd19fc6f225026'
})

export const securityPlan = root => {
  const rustRoot = join(root, 'frontend/apps/admin-desktop/src-tauri')
  return [
    { name: 'govulncheck', command: 'govulncheck', args: ['./...'], cwd: join(root, 'backend') },
    { name: 'pnpm-production-audit', command: process.platform === 'win32' ? 'pnpm.cmd' : 'pnpm', args: ['audit', '--prod', '--audit-level', 'high'], cwd: join(root, 'frontend') },
    { name: 'cargo-audit', command: 'cargo', args: ['audit'], cwd: rustRoot },
    { name: 'cargo-deny', command: 'cargo', args: ['deny', '--config', join(root, 'scripts/security/deny.toml'), 'check', 'bans', 'sources'], cwd: rustRoot },
    { name: 'secret-scan', command: 'docker', args: ['run', '--rm', '-v', `${root}:/repo`, images.gitleaks, 'detect', '--source=/repo', '--config=/repo/scripts/security/gitleaks.toml', '--no-banner', '--redact', '--report-format=json', '--report-path=/repo/artifacts/security/gitleaks.json'], cwd: root },
    { name: 'sbom', command: 'docker', args: ['run', '--rm', '-v', `${root}:/repo`, images.syft, 'dir:/repo', '--exclude', './.git/**', '--exclude', './artifacts/**', '--exclude', './frontend/apps/admin-desktop/src-tauri/target/**', '-o', 'cyclonedx-json=/repo/artifacts/security/repository.cdx.json'], cwd: root },
    { name: 'generate-drift', command: 'node', args: ['scripts/contracts/cli.mjs', 'generate', '--check'], cwd: root }
  ]
}

export const verifySecurityArtifacts = root => {
  const leaks = JSON.parse(readFileSync(join(root, 'artifacts/security/gitleaks.json'), 'utf8'))
  if (!Array.isArray(leaks) || leaks.length !== 0) throw new Error('secret scan report must contain zero findings')
  const sbom = JSON.parse(readFileSync(join(root, 'artifacts/security/repository.cdx.json'), 'utf8'))
  if (sbom.bomFormat !== 'CycloneDX' || !Array.isArray(sbom.components) || sbom.components.length === 0) throw new Error('CycloneDX SBOM must contain components')
}

export const runRequiredSecurity = ({ root, environment = process.env, spawn = spawnSync, plan = securityPlan(root), verify = verifySecurityArtifacts }) => {
  if (environment.GO_ADMIN_CI_REQUIRE_SECURITY !== '1') throw new Error('GO_ADMIN_CI_REQUIRE_SECURITY=1 is required')
  if (plan.length === 0) throw new Error('required security plan contains zero commands')
  mkdirSync(join(root, 'artifacts/security'), { recursive: true })
  const completed = []
  for (const step of plan) {
    const result = spawn(step.command, step.args, { cwd: step.cwd, env: environment, encoding: 'utf8', maxBuffer: 64 * 1024 * 1024, timeout: 20 * 60 * 1000 })
    if (result.error || result.status !== 0) throw new Error(`required security step ${step.name} failed with status ${result.status ?? 'spawn'}`)
    completed.push(step.name)
  }
  verify(root)
  return completed
}

const repositoryRoot = resolve(dirname(fileURLToPath(import.meta.url)), '../..')
if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const completed = runRequiredSecurity({ root: repositoryRoot })
    console.log(`REQUIRED_SECURITY_PASS completed=${completed.length}`)
  } catch (error) {
    console.error(`REQUIRED_SECURITY_FAIL ${error instanceof Error ? error.message : 'unknown failure'}`)
    process.exit(1)
  }
}
