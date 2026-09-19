#!/usr/bin/env node

import { createHash } from 'node:crypto'
import { execFileSync } from 'node:child_process'
import { existsSync, readFileSync, readdirSync } from 'node:fs'
import { basename, dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), '../..')
const SHA_PATTERN = /^[0-9a-f]{40}$/
const DIGEST_PATTERN = /^sha256:[0-9a-f]{64}$/
const VERSION_PATTERN = /^\d+\.\d+\.\d+$/

const platforms = {
  linux: {
    workflow: '.github/workflows/release.yml',
    platforms: ['linux/amd64', 'linux/arm64'],
    host: 'server-service',
    releaseClass: 'linux-service',
    checksums: ['SHA256SUMS'],
    sboms: [],
    signature: { type: 'none', required: false }
  },
  linux_desktop: {
    workflow: '.github/workflows/release.yml',
    platforms: ['linux/amd64'],
    host: 'desktop',
    releaseClass: 'private-release',
    checksums: ['SHA256SUMS'],
    sboms: [],
    signature: { type: 'none', required: false }
  },
  macos: {
    workflow: '.github/workflows/release.yml',
    platforms: ['darwin/arm64', 'darwin/amd64'],
    host: 'desktop',
    releaseClass: 'private-release',
    checksums: ['SHA256SUMS'],
    sboms: ['go-admin-plus-macos-arm64.spdx.json', 'go-admin-plus-macos-x64.spdx.json'],
    signature: { type: 'none', required: false }
  },
  windows: {
    workflow: '.github/workflows/release.yml',
    platforms: ['windows/amd64'],
    host: 'desktop',
    releaseClass: 'private-release',
    checksums: ['SHA256SUMS'],
    sboms: ['go-admin-plus-windows-x64.spdx.json'],
    signature: { type: 'none', required: false }
  }
}

const fail = message => { throw new Error(message) }
const run = (command, args) => execFileSync(command, args, { cwd: ROOT, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] }).trim()
const sha256File = path => createHash('sha256').update(readFileSync(path)).digest('hex')

const parseArgs = values => {
  const parsed = {}
  for (let index = 0; index < values.length; index += 1) {
    const value = values[index]
    if (!value.startsWith('--')) fail(`unexpected argument: ${value}`)
    const [name, inline] = value.slice(2).split('=', 2)
    if (inline !== undefined) parsed[name] = inline
    else if (values[index + 1] && !values[index + 1].startsWith('--')) parsed[name] = values[++index]
    else parsed[name] = true
  }
  return parsed
}

const requireOption = (options, name) => {
  const value = options[name]
  if (typeof value !== 'string' || value.length === 0) fail(`--${name} is required`)
  return value
}
const exactSha = (value, name) => {
  const normalized = String(value).toLowerCase()
  if (!SHA_PATTERN.test(normalized)) fail(`${name} must be an exact lowercase 40-character SHA`)
  return normalized
}
const walk = directory => readdirSync(directory, { withFileTypes: true }).flatMap(entry => {
  const path = join(directory, entry.name)
  return entry.isDirectory() ? walk(path) : [path]
})

const migrationVersion = () => {
  const versions = walk(join(ROOT, 'backend/internal'))
    .filter(path => path.endsWith('.sql'))
    .map(path => basename(path).match(/^(\d+)_/)?.[1])
    .filter(Boolean)
    .map(Number)
  if (versions.length === 0) fail('no numbered migration SQL files found')
  return String(Math.max(...versions))
}

const sourceContract = version => {
  if (!VERSION_PATTERN.test(version)) fail('version must use numeric major.minor.patch format')
  const tauri = JSON.parse(readFileSync(join(ROOT, 'frontend/apps/admin-desktop/src-tauri/tauri.conf.json'), 'utf8'))
  const macos = JSON.parse(readFileSync(join(ROOT, 'release/macos/identity.json'), 'utf8'))
  const windows = JSON.parse(readFileSync(join(ROOT, 'release/windows/identity.json'), 'utf8'))
  const linux = JSON.parse(readFileSync(join(ROOT, 'release/linux/identity.json'), 'utf8'))
  if (tauri.version !== version) fail(`version ${version} does not match Tauri product version ${tauri.version}`)
  if (macos.releaseClass !== 'private-release' || macos.signingRequired || macos.notarizationRequired || JSON.stringify(macos.architectures) !== JSON.stringify(['arm64', 'x86_64'])) fail('macOS identity is incomplete')
  if (windows.releaseClass !== 'private-release' || windows.signingRequired || windows.architecture !== 'x86_64') fail('Windows x64 identity is incomplete')
  if (JSON.stringify(linux.platforms) !== JSON.stringify(platforms.linux.platforms) ||
      JSON.stringify(linux.artifacts) !== JSON.stringify(['go-admin-plus-server'])) fail('Linux service identity is incomplete')
  const rootSha = exactSha(run('git', ['rev-parse', 'HEAD']), 'root SHA')
  const openapiPath = 'scripts/contracts/generated/openapi.json'
  return {
    rootSha,
    openapi: { path: openapiPath, sha256: sha256File(join(ROOT, openapiPath)) },
    migration: { max_version: migrationVersion() }
  }
}

const preflight = options => {
  const version = requireOption(options, 'version')
  const contract = sourceContract(version)
  if (options['root-ref'] && exactSha(options['root-ref'], 'root ref') !== contract.rootSha) fail('root ref does not match checkout')
  if (!options['allow-dirty']) {
    const dirty = run('git', ['status', '--porcelain', '--untracked-files=normal'])
    if (dirty) fail(`root workspace is dirty:\n${dirty}`)
  }
  process.stdout.write(`${JSON.stringify({ version, ...contract }, null, 2)}\n`)
}

const validateManifest = manifest => {
  if (manifest.schema_version !== 2) fail('manifest schema_version must be 2')
  if (manifest.product?.name !== 'Go Admin Plus') fail('manifest product name is invalid')
  if (!VERSION_PATTERN.test(manifest.product?.version ?? '')) fail('manifest product version is invalid')
  if (manifest.product.release_class !== 'production-candidate') fail('manifest release class is invalid')
  if (manifest.product.publication_authorized !== false) fail('manifest must not authorize publication')
  exactSha(manifest.provenance?.source_sha, 'manifest source SHA')
  if (!/^[0-9a-f]{64}$/.test(manifest.provenance?.openapi?.sha256 ?? '')) fail('manifest OpenAPI digest is invalid')
  if (!/^\d+$/.test(manifest.provenance?.migration?.max_version ?? '')) fail('manifest migration version is invalid')
  for (const [key, definition] of Object.entries(platforms)) {
    const item = manifest.artifacts?.[key]
    if (!item || JSON.stringify(item.platforms) !== JSON.stringify(definition.platforms) || item.host !== definition.host) fail(`${key} platform contract is invalid`)
    if (item.release?.product_version !== manifest.product.version || item.release?.class !== definition.releaseClass) fail(`${key} release identity drifted`)
    if (item.provenance?.head_sha !== manifest.provenance.source_sha) fail(`${key} source provenance drifted`)
    if (!DIGEST_PATTERN.test(item.artifact?.archive_sha256 ?? '')) fail(`${key} artifact digest is invalid`)
    if (!Array.isArray(item.checksums?.files) || item.checksums.files.length === 0) fail(`${key} checksums are missing`)
    if (!Array.isArray(item.sbom?.files) || JSON.stringify(item.sbom.files) !== JSON.stringify(definition.sboms)) fail(`${key} SBOM contract is invalid`)
    if (JSON.stringify(item.signature) !== JSON.stringify(definition.signature)) fail(`${key} signature evidence is invalid`)
  }
  return manifest
}

const verify = options => {
  const path = resolve(ROOT, requireOption(options, 'manifest'))
  if (!existsSync(path)) fail(`manifest does not exist: ${path}`)
  validateManifest(JSON.parse(readFileSync(path, 'utf8')))
  process.stdout.write(`GO_ADMIN_PRODUCT_MANIFEST_VERIFY_PASS manifest=${path}\n`)
}

const main = async () => {
  const [command, ...values] = process.argv.slice(2)
  const options = parseArgs(values)
  if (command === 'preflight') return preflight(options)
  if (command === 'verify') return verify(options)
  fail('usage: product-release.mjs <preflight|verify> [options]')
}

main().catch(error => {
  console.error(`GO_ADMIN_PRODUCT_MANIFEST_FAIL ${error.message}`)
  process.exitCode = 1
})
