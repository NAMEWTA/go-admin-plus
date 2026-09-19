#!/usr/bin/env node

import { lstat, readFile, readdir } from 'node:fs/promises'
import { dirname, resolve } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'

export const desktopNativeControlMarkers = Object.freeze([
  '/__desktop/test-control',
  'native-e2e',
  'VITE_GO_ADMIN_NATIVE_E2E',
  'GO_ADMIN_DESKTOP_NATIVE_E2E',
  'GO_ADMIN_DESKTOP_E2E_',
  'desktop_native_e2e',
  'E2E authenticated boundary verified',
  'E2E unauthenticated boundary verified',
  'E2E boundary blocked:',
  'E2E self scope enforced',
  'E2E all scope restored',
  'E2E authorization denied',
  'E2E authorization restored',
  'E2E control failed: theme-dark',
  'E2E control failed:',
  'E2E open Files',
  'E2E open Roles',
  'E2E use dark theme',
  'E2E scope self',
  'E2E scope all',
  'E2E permissions off',
  'E2E permissions on',
  'E2E revoke session',
  'E2E reset theme',
  'E2E theme storage cleared',
  'E2E-FOREIGN',
  'e2e-role',
  'native E2E credential identity'
])

export const desktopProductionPermissions = Object.freeze([
  'allow-desktop-request',
  'allow-desktop-identity',
  'allow-desktop-first-setup-state',
  'allow-desktop-first-setup-submit',
  'allow-desktop-navigation',
  'allow-desktop-login',
  'allow-desktop-logout',
  'allow-desktop-session-heartbeat',
  'allow-desktop-session-renew',
  'allow-desktop-pick-file',
  'allow-desktop-save-file',
  'allow-desktop-notify',
  'allow-desktop-write-clipboard',
  'allow-desktop-connection-get',
  'allow-desktop-connection-test',
  'allow-desktop-connection-set',
  'allow-desktop-upload-managed-file',
  'allow-desktop-download-managed-file'
])

const exactArray = (actual, expected) => Array.isArray(actual) && actual.length === expected.length && actual.every((value, index) => value === expected[index])
const exactKeys = (actual, expected) => {
  if (!actual || typeof actual !== 'object' || Array.isArray(actual)) return false
  const keys = Object.keys(actual).sort()
  return exactArray(keys, [...expected].sort())
}

const productionCsp = "default-src 'self'; connect-src 'none'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self'; object-src 'none'; base-uri 'none'; frame-src 'none'"

export const validateDesktopProductionConfiguration = (config, capability) => {
  const windows = config?.app?.windows
  const mainWindow = Array.isArray(windows) && windows.length === 1 ? windows[0] : undefined
  if (
    !exactKeys(config, ['$schema', 'app', 'build', 'bundle', 'identifier', 'productName', 'version']) ||
    !exactKeys(config?.build, ['beforeBuildCommand', 'beforeDevCommand', 'devUrl', 'frontendDist']) ||
    !exactKeys(config?.app, ['security', 'windows']) ||
    !exactKeys(config?.app?.security, ['capabilities', 'csp']) ||
    !exactKeys(mainWindow, ['height', 'label', 'minHeight', 'minWidth', 'title', 'visible', 'width']) ||
    !exactKeys(config?.bundle, ['active', 'externalBin', 'icon', 'targets']) ||
    !exactKeys(capability, ['$schema', 'description', 'identifier', 'permissions', 'windows']) ||
    config?.$schema !== 'https://schema.tauri.app/config/2' ||
    config?.productName !== 'Go Admin Plus' || config?.version !== '0.0.3' ||
    config?.identifier !== 'com.goadmin.plus' ||
    config?.build?.beforeDevCommand !== 'pnpm dev' || config?.build?.beforeBuildCommand !== 'pnpm build' ||
    config?.build?.devUrl !== 'http://127.0.0.1:1420' ||
    config?.build?.frontendDist !== '../dist' ||
    config?.app?.security?.csp !== productionCsp ||
    !exactArray(config?.app?.security?.capabilities, ['main-window']) ||
    mainWindow?.label !== 'main' || mainWindow?.title !== 'Go Admin Plus' ||
    mainWindow?.width !== 1280 || mainWindow?.height !== 800 || mainWindow?.visible !== false ||
    mainWindow?.minWidth !== 960 || mainWindow?.minHeight !== 640 ||
    config?.bundle?.active !== true ||
    !exactArray(config?.bundle?.icon, ['icons/32x32.png', 'icons/128x128.png', 'icons/128x128@2x.png', 'icons/icon.icns', 'icons/icon.ico']) ||
    !exactArray(config?.bundle?.externalBin, ['binaries/go-admin-sidecar']) ||
    !exactArray(config?.bundle?.targets, ['app', 'dmg', 'nsis', 'deb', 'appimage']) ||
    capability?.$schema !== '../gen/schemas/desktop-schema.json' ||
    capability?.identifier !== 'main-window' ||
    capability?.description !== 'Minimal core permissions for the hidden-until-ready administration window.' ||
    !exactArray(capability?.windows, ['main']) ||
    !exactArray(capability?.permissions, desktopProductionPermissions)
  ) {
    throw new Error('desktop production capability configuration is invalid')
  }
}

const readJson = async (path, name) => {
  try {
    return JSON.parse(await readFile(path, 'utf8'))
  } catch {
    throw new Error(`desktop production ${name} is invalid`)
  }
}

export const verifyDesktopProductionConfiguration = async appRoot => {
  const root = resolve(appRoot)
  const [config, capability] = await Promise.all([
    readJson(resolve(root, 'src-tauri/tauri.conf.json'), 'Tauri configuration'),
    readJson(resolve(root, 'src-tauri/capabilities/main.json'), 'capability manifest')
  ])
  validateDesktopProductionConfiguration(config, capability)
}

const filesBelow = async directory => {
  const files = []
  for (const entry of await readdir(directory, { withFileTypes: true })) {
    const path = resolve(directory, entry.name)
    if (entry.isSymbolicLink()) throw new Error('desktop production assets contain a symbolic link')
    if (entry.isDirectory()) files.push(...await filesBelow(path))
    else if (entry.isFile()) files.push(path)
    else throw new Error('desktop production assets contain an unsupported entry')
  }
  return files
}

export const verifyDesktopProductionAssets = async directory => {
  const root = resolve(directory)
  const info = await lstat(root)
  if (!info.isDirectory() || info.isSymbolicLink()) throw new Error('desktop production assets directory is invalid')
  const files = await filesBelow(root)
  if (files.length === 0) throw new Error('desktop production assets are empty')
  await verifyDesktopProductionFiles(files)
}

export const verifyDesktopProductionFiles = async paths => {
  if (!Array.isArray(paths) || paths.length === 0) throw new Error('desktop production file list is empty')
  for (const unresolvedPath of paths) {
    const path = resolve(unresolvedPath)
    const info = await lstat(path)
    if (!info.isFile() || info.isSymbolicLink()) throw new Error('desktop production artifact is invalid')
    const content = await readFile(path)
    for (const marker of desktopNativeControlMarkers) {
      if (content.includes(Buffer.from(marker))) {
        throw new Error(`desktop production assets contain native test control: ${marker}`)
      }
    }
  }
}

const invoked = process.argv[1] && pathToFileURL(resolve(process.argv[1])).href === import.meta.url
if (invoked) {
  const appRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..')
  try {
    const args = process.argv.slice(2)
    if (args.length === 0) {
      await verifyDesktopProductionConfiguration(appRoot)
      await verifyDesktopProductionAssets(resolve(appRoot, 'dist'))
    }
    else if (args[0] === '--files' && args.length > 1) await verifyDesktopProductionFiles(args.slice(1))
    else throw new Error('desktop production verification arguments are invalid')
    process.stdout.write('DESKTOP_PRODUCTION_ASSETS_PASS\n')
  } catch (error) {
    process.stderr.write(`${error instanceof Error ? error.message : 'desktop production assets verification failed'}\n`)
    process.exitCode = 1
  }
}
