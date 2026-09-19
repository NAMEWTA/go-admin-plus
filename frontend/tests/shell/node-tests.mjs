#!/usr/bin/env node

import { spawnSync } from 'node:child_process'
import { readdir } from 'node:fs/promises'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'

const testsRoot = fileURLToPath(new URL('../', import.meta.url))

const discover = async directory => {
  const tests = []
  for (const entry of await readdir(directory, { withFileTypes: true })) {
    const path = join(directory, entry.name)
    if (entry.isDirectory()) tests.push(...await discover(path))
    else if (entry.name.endsWith('.test.mjs')) tests.push(path)
  }
  return tests
}

const tests = (await discover(testsRoot)).sort()
if (tests.length === 0) throw new Error('no Node unit tests discovered')

const result = spawnSync(process.execPath, ['--test', ...tests], { stdio: 'inherit' })
if (result.error) throw result.error
if (result.signal) throw new Error(`Node unit tests terminated by ${result.signal}`)
process.exitCode = result.status ?? 1
