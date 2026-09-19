#!/usr/bin/env node

import { dirname, posix, resolve, win32 } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'

import { hostTriple, outputName } from '../../../../release/shared/sidecar/build.mjs'
import { verifyDesktopProductionAssets, verifyDesktopProductionConfiguration, verifyDesktopProductionFiles } from './verify-production.mjs'

export const desktopProductionArtifactPaths = (repository, platform = process.platform, architecture = process.arch) => {
  const triple = hostTriple(platform, architecture)
  const targetPath = platform === 'win32' ? win32 : posix
  const root = targetPath.resolve(repository)
  const appRoot = targetPath.join(root, 'frontend/apps/admin-desktop')
  return {
    webview: targetPath.join(appRoot, 'dist'),
    sidecar: targetPath.join(appRoot, 'src-tauri/binaries', outputName(triple)),
    host: targetPath.join(appRoot, 'src-tauri/target/release', platform === 'win32' ? 'go-admin-plus-desktop.exe' : 'go-admin-plus-desktop')
  }
}

export const verifyDesktopProductionBuild = async (repository, platform = process.platform, architecture = process.arch) => {
  const artifacts = desktopProductionArtifactPaths(repository, platform, architecture)
  await verifyDesktopProductionConfiguration(resolve(repository, 'frontend/apps/admin-desktop'))
  await verifyDesktopProductionAssets(artifacts.webview)
  await verifyDesktopProductionFiles([artifacts.sidecar, artifacts.host])
}

const invoked = process.argv[1] && pathToFileURL(resolve(process.argv[1])).href === import.meta.url
if (invoked) {
  const repository = resolve(dirname(fileURLToPath(import.meta.url)), '../../../..')
  try {
    await verifyDesktopProductionBuild(repository)
    process.stdout.write('DESKTOP_PRODUCTION_BUILD_PASS\n')
  } catch (error) {
    process.stderr.write(`${error instanceof Error ? error.message : 'desktop production build verification failed'}\n`)
    process.exitCode = 1
  }
}
