#!/usr/bin/env node

import { existsSync, readFileSync, readdirSync } from 'node:fs'
import { dirname, extname, join, relative, resolve, sep } from 'node:path'
import { fileURLToPath } from 'node:url'

const root = resolve(dirname(fileURLToPath(import.meta.url)), '../..')

export const documentationRoots = Object.freeze([
  'README.md', '.agents/skills', 'docs', 'deploy/README.md', 'database/README.md',
  'release', 'backend/README.md', 'backend/config/README.md', 'frontend'
])

const requiredContracts = Object.freeze({
  'README.md': [
    /task migrate PROFILE=server-sqlite/,
    /task dev TARGET=server PROFILE=server-sqlite/,
    /server-postgres/,
    /task dev TARGET=desktop/,
    /bootstrap --profile/,
    /--secret-file/,
    /task doctor/
  ],
  'database/README.md': [
    /serve.*worker.*PostgreSQL.*迁移/s,
    /bootstrap --profile/,
    /recover-admin/,
    /备份/
  ],
  'deploy/README.md': [
    /migrate-postgres/,
    /service_completed_successfully/,
    /备份/,
    /schema.*readiness/s
  ],
  'docs/development.md': [
    /server-sqlite/,
    /server-postgres/,
    /desktop-sqlite/,
    /bootstrap --profile/,
    /--secret-file/,
    /recover-admin/,
    /GO_ADMIN_LOG_LEVEL/,
    /task doctor/
  ],
  'docs/operations.md': [
    /schema mismatch/i,
    /recover-admin/,
    /Session/,
    /容量/,
    /备份恢复/,
    /secret-file/
  ],
  'docs/release.md': [
    /three-profile clean-room/i,
    /server-sqlite/,
    /server-postgres/,
    /desktop-sqlite/,
    /not-required/,
    /task release:verify/
  ]
})

const forbiddenContracts = Object.freeze([
  ['fixed administrator credential', /(?:admin(?:istrator)?|管理员)[^\n]{0,32}(?:password|密码)\s*[:=]\s*\S+/i],
  ['credential-bearing PostgreSQL URL', /postgres(?:ql)?:\/\/[^\s/:]+:[^\s@/]+@/i],
  ['password argv example', /--password(?:=|\s+)/i],
  ['obsolete database initializer', /\binit-db\b/i],
  ['obsolete server command', /\bgo-admin-plus\s+server\b/i],
  ['automatic PostgreSQL migration claim', /PostgreSQL[^\n]{0,80}(?:自动|隐式|automatically)[^\n]{0,24}迁移/i]
])

const walk = path => {
  if (!existsSync(path)) return []
  return readdirSync(path, { withFileTypes: true }).flatMap(entry => {
    if (['node_modules', 'dist', 'target'].includes(entry.name)) return []
    const child = join(path, entry.name)
    return entry.isDirectory() ? walk(child) : entry.isFile() ? [child] : []
  })
}

const posixRelative = (base, path) => relative(base, path).split(sep).join('/')

export const checkMarkdownLinks = (repository, files) => {
  const failures = []
  for (const item of files) {
    const source = readFileSync(item, 'utf8')
    for (const match of source.matchAll(/\[[^\]]+\]\(([^)]+)\)/g)) {
      const target = match[1].split('#', 1)[0]
      if (!target || /^(?:https?:|mailto:)/.test(target)) continue
      if (!existsSync(resolve(dirname(item), decodeURIComponent(target)))) {
        failures.push(`broken documentation link: ${posixRelative(repository, item)} -> ${target}`)
      }
    }
  }
  return failures
}

export const checkDocumentSource = (path, source) => {
  const failures = []
  for (const [name, pattern] of forbiddenContracts) {
    if (pattern.test(source)) failures.push(`${name} remains in ${path}`)
  }
  for (const pattern of requiredContracts[path] ?? []) {
    if (!pattern.test(source)) failures.push(`required documentation contract missing in ${path}: ${pattern.source}`)
  }
  return failures
}

export const checkDocumentation = repository => {
  const files = documentationRoots.flatMap(path => {
    const absolute = join(repository, path)
    if (!existsSync(absolute)) return []
    if (extname(absolute) === '.md') return [absolute]
    const markdown = walk(absolute).filter(file => extname(file) === '.md')
    return path === 'frontend'
      ? markdown.filter(file => file.toLowerCase().endsWith(`${sep}readme.md`))
      : markdown
  })
  const uniqueFiles = [...new Set(files)]
  const failures = checkMarkdownLinks(repository, uniqueFiles)
  for (const item of uniqueFiles) {
    const path = posixRelative(repository, item)
    failures.push(...checkDocumentSource(path, readFileSync(item, 'utf8')))
  }
  for (const path of Object.keys(requiredContracts)) {
    if (!existsSync(join(repository, path))) failures.push(`required documentation file missing: ${path}`)
  }
  return failures
}

const invoked = process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)
if (invoked) {
  const failures = checkDocumentation(root)
  if (failures.length) {
    console.error(`DOCS_CHECK_FAIL\n${failures.join('\n')}`)
    process.exit(1)
  }
  console.log('DOCS_CHECK_PASS')
}
