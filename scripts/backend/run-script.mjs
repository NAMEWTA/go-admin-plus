#!/usr/bin/env node

import { spawnSync } from 'node:child_process'
import { existsSync } from 'node:fs'
import { dirname, extname, isAbsolute, relative, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const repoRoot = resolve(dirname(fileURLToPath(import.meta.url)), '../..')
const [script, ...args] = process.argv.slice(2)

if (!script) {
  console.error('run-script: a repository-relative shell script is required')
  process.exit(2)
}

const scriptPath = resolve(repoRoot, script)
const relativePath = relative(repoRoot, scriptPath)
if (isAbsolute(relativePath) || relativePath.startsWith('..') || extname(scriptPath) !== '.sh') {
  console.error(`run-script: script must be a .sh file inside the repository: ${script}`)
  process.exit(2)
}
if (!existsSync(scriptPath)) {
  console.error(`run-script: managed script does not exist: ${relativePath}`)
  process.exit(2)
}

const shell = process.env.GO_ADMIN_POSIX_SHELL || (process.platform === 'win32' ? 'sh.exe' : 'sh')
const result = spawnSync(shell, [scriptPath, ...args], {
  cwd: repoRoot,
  env: process.env,
  stdio: 'inherit',
})

if (result.error) {
  if (result.error.code === 'ENOENT') {
    console.error(`run-script: required POSIX shell is not installed: ${shell}`)
    process.exit(127)
  }
  console.error(`run-script: failed to start ${relativePath}: ${result.error.message}`)
  process.exit(1)
}

process.exit(result.status ?? 1)
