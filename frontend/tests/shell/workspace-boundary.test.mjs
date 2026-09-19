import assert from 'node:assert/strict'
import { readFile, readdir } from 'node:fs/promises'
import { dirname, join, relative } from 'node:path'
import { fileURLToPath } from 'node:url'
import test from 'node:test'
import { loadConfigFromFile } from 'vite'

const workspaceRoot = join(dirname(fileURLToPath(import.meta.url)), '../..')
const readJson = async path => JSON.parse(await readFile(path, 'utf8'))
const browserAdapterWorkspaceDependencies = ['@go-admin-plus/platform']
const desktopAdapterWorkspaceDependencies = ['@go-admin-plus/domain-iam', '@go-admin-plus/platform']
const assertBrowserAdapterDependencies = dependencies => {
  for (const dependency of browserAdapterWorkspaceDependencies) {
    assert.equal(dependencies[dependency], 'workspace:*')
  }
  assert.deepEqual(
    Object.keys(dependencies).filter(name => name.startsWith('@go-admin-plus/')).sort(),
    browserAdapterWorkspaceDependencies,
    'browser adapter may only depend on the platform port'
  )
}
const assertDesktopAdapterDependencies = dependencies => {
  for (const dependency of desktopAdapterWorkspaceDependencies) {
    assert.equal(dependencies[dependency], 'workspace:*')
  }
  assert.deepEqual(
    Object.keys(dependencies).filter(name => name.startsWith('@go-admin-plus/')).sort(),
    desktopAdapterWorkspaceDependencies,
    'desktop adapter may only depend on Session and platform ports'
  )
}
const requiredPackageNames = [
  '@go-admin-plus/adapter-browser',
  '@go-admin-plus/adapter-desktop',
  '@go-admin-plus/admin-desktop',
  '@go-admin-plus/admin-web',
  '@go-admin-plus/app-shell',
  '@go-admin-plus/domain-audit',
  '@go-admin-plus/domain-files',
  '@go-admin-plus/domain-iam',
  '@go-admin-plus/domain-scheduler',
  '@go-admin-plus/platform',
  '@go-admin-plus/ui',
  '@go-admin-plus/web-domain-audit',
  '@go-admin-plus/web-domain-files',
  '@go-admin-plus/web-domain-iam',
  '@go-admin-plus/web-domain-scheduler',
]

const sourceFiles = async root => {
  const files = []
  for (const entry of await readdir(root, { withFileTypes: true }).catch(() => [])) {
    if (entry.name === 'node_modules' || entry.name === 'dist') continue
    const path = join(root, entry.name)
    if (entry.isDirectory()) files.push(...await sourceFiles(path))
    else if (/\.(?:[cm]?[jt]s|vue)$/.test(entry.name)) files.push(path)
  }
  return files
}

const packageDirectories = async () => {
  const roots = [
    ['apps'],
    ['packages'],
    ['packages', 'adapters'],
    ['packages', 'domains'],
    ['packages', 'web-domains']
  ]
  const directories = []
  for (const segments of roots) {
    const root = join(workspaceRoot, ...segments)
    for (const entry of await readdir(root, { withFileTypes: true }).catch(() => [])) {
      if (!entry.isDirectory()) continue
      const directory = join(root, entry.name)
      try {
        await readFile(join(directory, 'package.json'))
        directories.push(directory)
      } catch {}
    }
  }
  return directories
}

test('all planned packages are private and expose only public entry points', async () => {
  const directories = await packageDirectories()
  const manifests = await Promise.all(directories.map(async directory => ({
    directory,
    manifest: await readJson(join(directory, 'package.json'))
  })))
  const names = new Set(manifests.map(({ manifest }) => manifest.name))

  assert.equal(names.size, manifests.length, 'workspace package names must be unique')
  for (const name of requiredPackageNames) {
    assert.ok(names.has(name), `required workspace package ${name} is missing`)
  }
  for (const { directory, manifest } of manifests) {
    assert.match(manifest.name, /^@go-admin-plus\//, `${relative(workspaceRoot, directory)} uses a non-canonical package scope`)
    assert.equal(manifest.private, true, relative(workspaceRoot, directory))
    assert.equal(manifest.type, 'module', relative(workspaceRoot, directory))
    assert.ok(manifest.exports, `${relative(workspaceRoot, directory)} has no public exports`)
    for (const [dependency, version] of Object.entries({
      ...manifest.dependencies,
      ...manifest.devDependencies
    })) {
      if (dependency.startsWith('@go-admin-plus/')) {
        assert.equal(version, 'workspace:*', `${manifest.name} must link ${dependency} through the workspace`)
      }
    }
  }
})

test('workspace dependencies are acyclic', async () => {
  const manifests = await Promise.all((await packageDirectories()).map(async directory =>
    readJson(join(directory, 'package.json'))
  ))
  const graph = new Map(manifests.map(manifest => [
    manifest.name,
    Object.keys(manifest.dependencies ?? {}).filter(name => name.startsWith('@go-admin-plus/'))
  ]))
  for (const [name, dependencies] of graph) {
    for (const dependency of dependencies) {
      assert.ok(graph.has(dependency), `${name} references unknown workspace package ${dependency}`)
    }
  }
  const visited = new Set()
  const active = new Set()

  const visit = name => {
    if (active.has(name)) throw new Error(`workspace dependency cycle at ${name}`)
    if (visited.has(name)) return
    active.add(name)
    for (const dependency of graph.get(name) ?? []) visit(dependency)
    active.delete(name)
    visited.add(name)
  }
  for (const name of graph.keys()) visit(name)
})

test('workspace consumers use package exports rather than source paths', async () => {
  const rootManifest = await readJson(join(workspaceRoot, 'package.json'))
  for (const [dependency, version] of Object.entries({
    ...rootManifest.dependencies,
    ...rootManifest.devDependencies
  })) {
    if (dependency.startsWith('@go-admin-plus/')) assert.equal(version, 'workspace:*')
  }

  for (const file of await sourceFiles(workspaceRoot)) {
    const source = await readFile(file, 'utf8')
    assert.doesNotMatch(
      source,
      /from\s+['"]@go-admin-plus\/[^'"]+\/src(?:\/|['"])/,
      relative(workspaceRoot, file)
    )
  }

  const activeExports = [
    'packages/adapters/browser/src/index.ts',
    'packages/app-shell/src/core/index.ts',
    'packages/app-shell/src/product/index.ts',
    'packages/platform/src/index.ts',
    'packages/ui/src/index.ts',
    'apps/admin-web/src/main.ts'
  ]
  for (const target of activeExports) assert.ok(await readFile(join(workspaceRoot, target), 'utf8'))
})

test('Admin Web development keeps API requests same-origin through the Server proxy', async () => {
  const loaded = await loadConfigFromFile(
    { command: 'serve', mode: 'development' },
    join(workspaceRoot, 'apps/admin-web/vite.config.ts')
  )

  assert.ok(loaded)
  assert.equal(loaded.config.server?.proxy?.['/api']?.target, 'http://127.0.0.1:8080')
  assert.equal(loaded.config.server?.proxy?.['/api']?.rewrite, undefined)
})

test('apps select adapters without owning runtime transport', async () => {
  const adminWeb = await readJson(join(workspaceRoot, 'apps/admin-web/package.json'))
  assert.equal(adminWeb.dependencies['@go-admin-plus/adapter-browser'], 'workspace:*')
  assert.equal(adminWeb.dependencies['@go-admin-plus/platform'], undefined)
  assert.deepEqual(
    Object.keys(adminWeb.dependencies).filter(name => name.startsWith('@go-admin-plus/')).sort(),
    ['@go-admin-plus/adapter-browser', '@go-admin-plus/app-shell', '@go-admin-plus/ui']
  )

  const browserAdapter = await readJson(join(workspaceRoot, 'packages/adapters/browser/package.json'))
  assertBrowserAdapterDependencies(browserAdapter.dependencies)

  const appShell = await readJson(join(workspaceRoot, 'packages/app-shell/package.json'))
  assert.equal(appShell.dependencies['@go-admin-plus/adapter-browser'], undefined)
  assert.equal(appShell.dependencies['@go-admin-plus/adapter-desktop'], undefined)

  const adminDesktop = await readJson(join(workspaceRoot, 'apps/admin-desktop/package.json'))
  assert.equal(adminDesktop.dependencies['@go-admin-plus/adapter-desktop'], 'workspace:*')
  assert.deepEqual(
    Object.keys(adminDesktop.dependencies).filter(name => name.startsWith('@go-admin-plus/')).sort(),
    ['@go-admin-plus/adapter-desktop', '@go-admin-plus/app-shell', '@go-admin-plus/ui']
  )
  const desktopAdapter = await readJson(join(workspaceRoot, 'packages/adapters/desktop/package.json'))
  assertDesktopAdapterDependencies(desktopAdapter.dependencies)

  const productShell = await readFile(join(workspaceRoot, 'packages/app-shell/src/product/ProductWorkspace.vue'), 'utf8')
  assert.doesNotMatch(productShell, /@go-admin-plus\/adapter-(?:browser|desktop)/)
  for (const module of ['iam', 'audit', 'scheduler', 'files']) {
    assert.match(productShell, new RegExp(`@go-admin-plus/web-domain-${module}`), `product shell must compose ${module}`)
  }
  assert.match(productShell, /productRoutesFor\(props\.host\)/)
  assert.match(productShell, /dataScope\.value\s*=\s*identity\.dataScope/)
  assert.doesNotMatch(productShell, /dataScope[^\n]*(?:\?|\|\|)\s*['"]all['"]/)

  const platformSource = await readFile(join(workspaceRoot, 'packages/platform/src/index.ts'), 'utf8')
  assert.match(platformSource, /readonly dataScope:\s*DataScope/)
  const browserRuntime = await readFile(join(workspaceRoot, 'packages/adapters/browser/src/index.ts'), 'utf8')
  assert.match(browserRuntime, /hasExactKeys\(record, \['dataScope', 'kind', 'permissions', 'subjectId'\]\)/)
  assert.match(browserRuntime, /record\.dataScope !== 'self' && record\.dataScope !== 'all'/)

  const appSource = (await sourceFiles(join(workspaceRoot, 'apps/admin-web/src')))
  for (const file of appSource) {
    const source = await readFile(file, 'utf8')
    assert.doesNotMatch(source, /\bfetch\s*\(|implements\s+ShellRuntimePort/)
  }
})

test('dual Apps inject one Platform Port and Desktop host commands remain explicitly allowlisted', async () => {
  const webApp = await readFile(join(workspaceRoot, 'apps/admin-web/src/App.vue'), 'utf8')
  assert.match(webApp, /createWebPlatform\(\)/)
  assert.match(webApp, /:platform="platform"/)
  const desktopApp = await readFile(join(workspaceRoot, 'apps/admin-desktop/src/App.vue'), 'utf8')
  assert.match(desktopApp, /createDesktopPlatform\(\)/)
  assert.match(desktopApp, /:platform="platform"/)

  const productShell = await readFile(join(workspaceRoot, 'packages/app-shell/src/product/ProductWorkspace.vue'), 'utf8')
  assert.match(productShell, /platform:\s*PlatformPort/)
  assert.match(productShell, /case 'files': return \{ controller: controllers\.value\.files, platform \}/)
  assert.match(productShell, /<RouterView/)
  assert.doesNotMatch(productShell, /<FilesPage/)
  const manifest = await readFile(join(workspaceRoot, 'packages/app-shell/src/product/manifest.ts'), 'utf8')
  assert.match(manifest, /component: async \(\) => .*\.FilesPage/)

  const host = await readFile(join(workspaceRoot, 'apps/admin-desktop/src-tauri/src/main.rs'), 'utf8')
  const capability = await readJson(join(workspaceRoot, 'apps/admin-desktop/src-tauri/capabilities/main.json'))
  for (const command of ['desktop_pick_file', 'desktop_save_file', 'desktop_notify', 'desktop_write_clipboard']) {
    assert.match(host, new RegExp(`async fn ${command}\\b`))
    assert.match(host, new RegExp(`invoke_handler\\([\\s\\S]*${command}`))
    assert.ok(capability.permissions.includes(`allow-${command.replaceAll('_', '-')}`))
  }
})

test('browser adapter dependency allowlist rejects additional workspace packages', () => {
  assert.throws(
    () => assertBrowserAdapterDependencies({
      '@go-admin-plus/platform': 'workspace:*',
      '@go-admin-plus/app-shell': 'workspace:*'
    }),
    /browser adapter may only depend/
  )
})

test('desktop adapter dependency allowlist rejects business Web Domain duplicates', () => {
  assert.throws(
    () => assertDesktopAdapterDependencies({
      '@go-admin-plus/domain-iam': 'workspace:*',
      '@go-admin-plus/platform': 'workspace:*',
      '@go-admin-plus/domain-files': 'workspace:*'
    }),
    /desktop adapter may only depend/
  )
})

test('headless packages do not depend on Vue, DOM globals, deep imports, or credential values', async () => {
  const headlessRoots = [
    join(workspaceRoot, 'packages/app-shell/src/core'),
    join(workspaceRoot, 'packages/platform/src')
  ]
  for (const root of headlessRoots) {
    for (const entry of await readdir(root, { withFileTypes: true })) {
      if (!entry.isFile()) continue
      const source = await readFile(join(root, entry.name), 'utf8')
      assert.doesNotMatch(source, /from\s+['"](?:vue|vue-router)['"]|\b(?:window|document|localStorage)\b/)
      assert.doesNotMatch(source, /@go-admin-plus\/[^'"]+\/src\//)
    }
  }
  const platformSource = await readFile(join(workspaceRoot, 'packages/platform/src/index.ts'), 'utf8')
  assert.doesNotMatch(platformSource, /\b(?:secret|password|sessionToken|authorization)\b/i)
})

test('mobile shell uses an off-canvas navigation and gives content the full viewport width', async () => {
  const shell = [
    await readFile(join(workspaceRoot, 'packages/app-shell/src/product/ProductWorkspace.vue'), 'utf8'),
    await readFile(join(workspaceRoot, 'packages/app-shell/src/product/ProductWorkspace.scss'), 'utf8')
  ].join('\n')
  assert.match(shell, /@media \(max-width: 760px\) \{[\s\S]*grid-template-columns:\s*1fr/)
  assert.match(shell, /@media \(max-width: 760px\) \{[\s\S]*\.product-shell__sidebar[^}]*transform:\s*translateX\(calc\(-100% - 16px\)\)/)
  assert.match(shell, /@media \(max-width: 760px\) \{[\s\S]*\.product-shell__sidebar\.is-mobile-open[^}]*transform:\s*translateX\(0\)/)
})
