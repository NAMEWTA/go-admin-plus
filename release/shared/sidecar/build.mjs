#!/usr/bin/env node

import { access, chmod, mkdir, mkdtemp, realpath, rename, rm } from 'node:fs/promises'
import { constants } from 'node:fs'
import { spawn } from 'node:child_process'
import { fileURLToPath } from 'node:url'
import { tmpdir } from 'node:os'
import path from 'node:path'

export const targets = Object.freeze({
  'aarch64-apple-darwin': { goos: 'darwin', goarch: 'arm64', extension: '' },
  'x86_64-apple-darwin': { goos: 'darwin', goarch: 'amd64', extension: '' },
  'x86_64-unknown-linux-gnu': { goos: 'linux', goarch: 'amd64', extension: '' },
  'x86_64-pc-windows-msvc': { goos: 'windows', goarch: 'amd64', extension: '.exe' }
})

const scriptDirectory = path.dirname(fileURLToPath(import.meta.url))
const repository = path.resolve(scriptDirectory, '../../..')
const goRoot = path.join(repository, 'backend')
const outputRoot = path.join(repository, 'frontend/apps/admin-desktop/src-tauri/binaries')

const run = (command, args, options = {}) => new Promise((resolve, reject) => {
  const child = spawn(command, args, { ...options, stdio: ['ignore', 'inherit', 'inherit'] })
  child.once('error', () => reject(new Error('sidecar build command unavailable')))
  child.once('exit', (code, signal) => {
    if (code === 0 && signal === null) resolve()
    else reject(new Error('sidecar build failed'))
  })
})

const capture = (command, args, options = {}) => new Promise((resolveCapture, rejectCapture) => {
  const child = spawn(command, args, { ...options, stdio: ['ignore', 'pipe', 'ignore'] })
  const chunks = []
  let size = 0
  child.stdout.on('data', chunk => {
    size += chunk.length
    if (size > 4096) child.kill('SIGKILL')
    else chunks.push(chunk)
  })
  child.once('error', () => rejectCapture(new Error('Go toolchain metadata unavailable')))
  child.once('exit', (code, signal) => {
    if (code !== 0 || signal !== null || size > 4096) rejectCapture(new Error('Go toolchain metadata invalid'))
    else resolveCapture(Buffer.concat(chunks).toString('utf8').trim())
  })
})

const canonicalizeModuleCache = async moduleCache => {
  if (!path.isAbsolute(moduleCache)) throw new Error('Go module cache path is not absolute')
  await mkdir(moduleCache, { recursive: true, mode: 0o700 })
  return realpath(moduleCache)
}

const resolveToolchain = async () => {
  const names = process.platform === 'win32' ? ['go.exe'] : ['go']
  if (process.env.GOROOT && path.isAbsolute(process.env.GOROOT)) {
    const goExecutable = path.join(process.env.GOROOT, 'bin', names[0])
    const moduleCache = process.env.GOMODCACHE && path.isAbsolute(process.env.GOMODCACHE)
      ? process.env.GOMODCACHE
      : process.env.GOENV_ROOT && path.isAbsolute(process.env.GOENV_ROOT)
        ? path.join(process.env.GOENV_ROOT, 'shared', 'go-mod')
        : undefined
    if (moduleCache) {
      await access(goExecutable, constants.X_OK)
      return {
        goExecutable: await realpath(goExecutable),
        moduleCache: await canonicalizeModuleCache(moduleCache)
      }
    }
  }
  const roots = []
  for (const value of (process.env.PATH ?? '').split(path.delimiter)) {
    if (path.isAbsolute(value)) roots.push(value)
  }
  if (process.env.GOROOT && path.isAbsolute(process.env.GOROOT)) roots.push(path.join(process.env.GOROOT, 'bin'))
  for (const root of roots) {
    for (const name of names) {
      const candidate = path.join(root, name)
      try {
        await access(candidate, constants.X_OK)
        const command = await realpath(candidate)
        const metadata = await capture(command, ['env', 'GOROOT', 'GOMODCACHE'], {
          env: {
            PATH: path.dirname(command),
            ...(process.env.HOME && path.isAbsolute(process.env.HOME) ? { HOME: process.env.HOME } : {}),
            // Windows 的 Go 使用 USERPROFILE 定位默认模块缓存，不能只传入 HOME。
            ...(process.platform === 'win32' && process.env.USERPROFILE && path.isAbsolute(process.env.USERPROFILE)
              ? { USERPROFILE: process.env.USERPROFILE } : {})
          }
        })
        const [goRoot, moduleCache, ...extra] = metadata.split(/\r?\n/)
        if (extra.length !== 0 || !path.isAbsolute(goRoot) || !path.isAbsolute(moduleCache)) {
          throw new Error('Go toolchain metadata invalid')
        }
        return {
          goExecutable: await realpath(path.join(goRoot, 'bin', process.platform === 'win32' ? 'go.exe' : 'go')),
          moduleCache: await canonicalizeModuleCache(moduleCache)
        }
      } catch {
        // Continue through the bounded, explicit search path.
      }
    }
  }
  throw new Error('Go toolchain is unavailable')
}

export const buildEnvironment = (target, sandbox, goExecutable, moduleCache, hostPath = path) => {
  return {
    PATH: hostPath.dirname(goExecutable),
    HOME: hostPath.join(sandbox, 'home'),
    TMPDIR: hostPath.join(sandbox, 'tmp'),
    GOCACHE: hostPath.join(sandbox, 'go-build'),
    GOMODCACHE: moduleCache,
    GOENV: 'off',
    GOFLAGS: '',
    GOWORK: 'off',
    GOPROXY: 'off',
    GOSUMDB: 'off',
    GOTOOLCHAIN: 'local',
    CGO_ENABLED: '0',
    GOOS: target.goos,
    GOARCH: target.goarch
  }
}

export const parseTargets = args => {
  if (args.length === 1 && args[0] === '--all') return Object.keys(targets)
  if (args.length === 1 && args[0] === '--host') return [hostTriple()]
  if (args.length === 2 && args[0] === '--target' && Object.hasOwn(targets, args[1])) return [args[1]]
  throw new Error('usage: build.mjs --host | --target <supported-triple> | --all')
}

export const parseBuildRequest = args => {
  const nativeE2E = args[0] === '--native-e2e'
  const selected = parseTargets(nativeE2E ? args.slice(1) : args)
  if (nativeE2E && selected.length !== 1) throw new Error('native E2E sidecar requires one target')
  return { selected, nativeE2E }
}

export const hostTriple = (platform = process.platform, architecture = process.arch) => {
  if (platform === 'darwin' && architecture === 'arm64') return 'aarch64-apple-darwin'
  if (platform === 'darwin' && architecture === 'x64') return 'x86_64-apple-darwin'
  if (platform === 'linux' && architecture === 'x64') return 'x86_64-unknown-linux-gnu'
  if (platform === 'win32' && architecture === 'x64') return 'x86_64-pc-windows-msvc'
  throw new Error('unsupported desktop host target')
}

export const outputName = triple => {
  const target = targets[triple]
  if (!target) throw new Error('unsupported sidecar target')
  return `go-admin-sidecar-${triple}${target.extension}`
}

export const buildTarget = async (triple, { nativeE2E = false } = {}) => {
  const target = targets[triple]
  if (!target) throw new Error('unsupported sidecar target')
  await mkdir(outputRoot, { recursive: true, mode: 0o700 })
  const output = path.join(outputRoot, outputName(triple))
  const staging = `${output}.incomplete-${process.pid}`
  const sandbox = await realpath(await mkdtemp(path.join(tmpdir(), 'go-admin-sidecar-build-')))
  await rm(staging, { force: true })
  try {
    await Promise.all([
      mkdir(path.join(sandbox, 'home'), { mode: 0o700 }),
      mkdir(path.join(sandbox, 'tmp'), { mode: 0o700 }),
      mkdir(path.join(sandbox, 'go-build'), { mode: 0o700 })
    ])
    const { goExecutable, moduleCache } = await resolveToolchain()
    const tags = nativeE2E ? ['-tags=desktop_native_e2e'] : []
    await run(goExecutable, ['build', '-trimpath', '-buildvcs=false', '-ldflags=-s -w', ...tags, '-o', staging, './cmd/desktop-sidecar'], {
      cwd: goRoot,
      env: buildEnvironment(target, sandbox, goExecutable, moduleCache)
    })
    if (target.goos !== 'windows') await chmod(staging, 0o700)
    await rename(staging, output)
  } catch (error) {
    await rm(staging, { force: true })
    throw error
  } finally {
    await rm(sandbox, { recursive: true, force: true })
  }
  return output
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const request = parseBuildRequest(process.argv.slice(2))
    for (const triple of request.selected) await buildTarget(triple, { nativeE2E: request.nativeE2E })
    process.stdout.write(`${JSON.stringify({ built: request.selected, nativeE2E: request.nativeE2E })}\n`)
  } catch {
    process.stderr.write('sidecar packaging failed\n')
    process.exitCode = 1
  }
}
