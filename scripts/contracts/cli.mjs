import { spawnSync } from 'node:child_process'
import {
  copyFileSync,
  existsSync,
  lstatSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  readdirSync,
  realpathSync,
  renameSync,
  rmSync,
  writeFileSync
} from 'node:fs'
import { tmpdir } from 'node:os'
import { basename, dirname, isAbsolute, join, relative, resolve, sep } from 'node:path'
import { fileURLToPath } from 'node:url'
import {
  discoverModuleContracts,
  parseManagedModuleOutput,
  resolveModuleMetadata
} from './modules.mjs'
import { validatePolicy } from './policy.mjs'

const repositoryRoot = join(dirname(fileURLToPath(import.meta.url)), '..', '..')
const uiRoot = join(repositoryRoot, 'frontend')
const canonicalContract = join(repositoryRoot, 'contracts', 'openapi', 'openapi.yaml')
const config = join(repositoryRoot, 'scripts', 'contracts', 'redocly.yaml')
const goConfigPath = join(repositoryRoot, 'scripts', 'contracts', 'oapi-codegen.yaml')
const manifestPath = join('scripts', 'contracts', 'generated', 'manifest.json')
const pnpm = process.platform === 'win32' ? 'corepack.cmd' : 'corepack'
const canonicalOutputs = {
  bundle: join('scripts', 'contracts', 'generated', 'openapi.json'),
  go: join('backend', 'internal', 'contracts', 'openapi.gen.go'),
  runtimeSpec: join('backend', 'internal', 'contracts', 'openapi.json'),
  typescript: join('frontend', 'packages', 'api-client', 'src', 'generated', 'schema.ts'),
  client: join('frontend', 'packages', 'api-client', 'src', 'generated', 'client.ts')
}
const canonicalGeneratedFiles = Object.values(canonicalOutputs)

const lstatIfPresent = path => {
  try {
    return lstatSync(path)
  } catch (error) {
    if (error && typeof error === 'object' && error.code === 'ENOENT') return undefined
    throw error
  }
}

const assertSafeOutputLocation = (outputRoot, path, { directory = false } = {}) => {
  const root = resolve(outputRoot)
  const rootStat = lstatIfPresent(root)
  if (!rootStat?.isDirectory() || rootStat.isSymbolicLink()) fail('generated output root must be a real directory')
  const realRoot = realpathSync(root)
  const destination = resolve(root, path)
  const location = relative(root, destination)
  if (location === '' || location === '..' || location.startsWith(`..${sep}`) || isAbsolute(location)) {
    fail(`generated output resolves outside its root: ${path}`)
  }

  const segments = location.split(sep)
  let current = root
  let nearestExisting = root
  for (const [index, segment] of segments.entries()) {
    current = join(current, segment)
    const stat = lstatIfPresent(current)
    if (!stat) break
    nearestExisting = current
    if (stat.isSymbolicLink()) fail(`generated output path contains a symbolic link: ${path}`)
    const final = index === segments.length - 1
    if (!final && !stat.isDirectory()) fail(`generated output parent is not a directory: ${path}`)
    if (final && directory && !stat.isDirectory()) fail(`generated output directory is not a directory: ${path}`)
    if (final && !directory && !stat.isFile()) fail(`generated output is not a regular file: ${path}`)
  }
  const physicalLocation = relative(realRoot, realpathSync(nearestExisting))
  if (physicalLocation === '..' || physicalLocation.startsWith(`..${sep}`) || isAbsolute(physicalLocation)) {
    fail(`generated output resolves outside its physical root: ${path}`)
  }
  return destination
}

class ContractError extends Error {
  constructor(message, exitCode = 1) {
    super(message)
    this.exitCode = exitCode
  }
}

const fail = (message, exitCode = 1) => {
  throw new ContractError(`contract: ${message}`, exitCode)
}

const run = (command, args, options = {}) => {
  const commandShim = process.platform === 'win32' && /\.(?:cmd|bat)$/i.test(command)
  if (commandShim && args.some(argument => /[\r\n"&|<>^%!]/.test(argument))) {
    fail(`${command} received an unsafe Windows command argument`)
  }
  const result = spawnSync(command, args, {
    cwd: options.cwd ?? repositoryRoot,
    encoding: 'utf8',
    stdio: options.capture ? 'pipe' : 'inherit',
    shell: commandShim
  })
  if (result.error) fail(`${command} is required: ${result.error.message}`, 127)
  if (result.status !== 0) {
    const detail = options.capture
      ? `\n${[result.stdout, result.stderr].filter(Boolean).join('\n').trim()}`
      : ''
    fail(`${command} exited with status ${result.status ?? 1}${detail}`, result.status ?? 1)
  }
  return result.stdout ?? ''
}

const redocly = (...args) => run(pnpm, [
  'pnpm',
  '--dir', uiRoot,
  '--filter', '@go-admin-plus/api-client',
  'exec', 'redocly',
  ...args
])

const optionValues = name => {
  const values = []
  for (let index = 0; index < process.argv.length; index += 1) {
    if (process.argv[index] !== name) continue
    if (!process.argv[index + 1]) fail(`${name} requires a value`)
    values.push(process.argv[index + 1])
    index += 1
  }
  return values
}

const bundle = (input, output, { dereferenced = false } = {}) => {
  const args = ['bundle', input]
  if (dereferenced) args.push('--dereferenced')
  redocly(...args, '--ext', 'json', '--output', output, '--config', config)
}

const lintOne = (input, operationIds) => {
  redocly('lint', input, '--config', config)
  const temporaryDirectory = mkdtempSync(join(tmpdir(), 'go-admin-contract-'))
  const output = join(temporaryDirectory, 'bundle.json')
  try {
    bundle(input, output, { dereferenced: true })
    const document = JSON.parse(readFileSync(output, 'utf8'))
    validatePolicy(document, {
      canonical: resolve(input) === resolve(canonicalContract),
      operationIds,
      source: relative(repositoryRoot, input)
    })
    if (document['x-go-admin-module'] !== undefined || document['x-go-admin-codegen'] !== undefined) {
      return resolveModuleMetadata(repositoryRoot, document, relative(repositoryRoot, input))
    }
    return undefined
  } finally {
    rmSync(temporaryDirectory, { recursive: true, force: true })
  }
}

export const lintContracts = inputs => {
  const operationIds = new Map()
  const fragments = new Map()
  for (const input of inputs) {
    const metadata = lintOne(resolve(input), operationIds)
    if (!metadata) continue
    const first = fragments.get(metadata.id)
    if (first) fail(`module fragment id ${metadata.id} is declared by both ${first} and ${metadata.source}`)
    fragments.set(metadata.id, metadata.source)
  }
}

const allContracts = () => [canonicalContract, ...discoverModuleContracts(repositoryRoot)]
const lintAll = () => lintContracts(allContracts())

const canonicalClient = `// Code generated by scripts/contracts/cli.mjs. DO NOT EDIT.\nimport createOpenAPIClient, { type ClientOptions } from 'openapi-fetch'\nimport type { paths } from './schema'\n\nexport { createOpenAPIClient }\nexport type { ClientOptions }\nexport const createContractClient = (options: ClientOptions) => createOpenAPIClient<paths>(options)\nexport type ContractClient = ReturnType<typeof createContractClient>\nexport type { components, operations, paths } from './schema'\n`

const moduleClient = `// Code generated by scripts/contracts/cli.mjs. DO NOT EDIT.\nimport { createOpenAPIClient, type ClientOptions } from '@go-admin-plus/api-client/contract'\nimport type { paths } from './schema'\n\nexport const createContractClient = (options: ClientOptions) => createOpenAPIClient<paths>(options)\nexport type ContractClient = ReturnType<typeof createContractClient>\nexport type { components, operations, paths } from './schema'\n`

const generateGo = (input, output, packageName) => {
  const goConfig = JSON.parse(readFileSync(goConfigPath, 'utf8'))
  goConfig.package = packageName
  goConfig.output = output
  const configDirectory = mkdtempSync(join(tmpdir(), 'go-admin-oapi-config-'))
  try {
    const temporaryConfig = join(configDirectory, 'oapi-codegen.json')
    writeFileSync(temporaryConfig, `${JSON.stringify(goConfig, null, 2)}\n`)
    run('go', ['tool', 'oapi-codegen', '--config', temporaryConfig, input], {
      cwd: join(repositoryRoot, 'backend')
    })
  } finally {
    rmSync(configDirectory, { recursive: true, force: true })
  }
}

const generateTypescript = (input, schemaOutput, clientOutput, clientSource) => {
  run(pnpm, [
  'pnpm',
    '--dir', uiRoot,
    '--filter', '@go-admin-plus/api-client',
    'exec', 'openapi-typescript', input,
    '--output', schemaOutput,
    '--alphabetize'
  ])
  writeFileSync(clientOutput, clientSource)
}

const outputAt = (outputRoot, path) => {
  const output = assertSafeOutputLocation(outputRoot, path)
  mkdirSync(dirname(output), { recursive: true })
  return output
}

export const generate = (outputRoot, moduleContracts = discoverModuleContracts(repositoryRoot)) => {
  const generatedFiles = [...canonicalGeneratedFiles]
  const outputs = Object.fromEntries(
    Object.entries(canonicalOutputs).map(([name, path]) => [name, outputAt(outputRoot, path)])
  )
  bundle(canonicalContract, outputs.bundle)
  copyFileSync(outputs.bundle, outputs.runtimeSpec)
  generateGo(outputs.bundle, outputs.go, 'contracts')
  generateTypescript(outputs.bundle, outputs.typescript, outputs.client, canonicalClient)

  const stagingDirectory = mkdtempSync(join(tmpdir(), 'go-admin-module-contracts-'))
  const moduleIds = new Set()
  const moduleOutputs = new Set()
  try {
    for (const [index, moduleContract] of moduleContracts.entries()) {
      const stem = basename(moduleContract).replace(/\.[^.]+$/, '')
      const moduleBundle = join(stagingDirectory, `${String(index).padStart(3, '0')}-${stem}.json`)
      bundle(moduleContract, moduleBundle)
      const metadata = resolveModuleMetadata(
        repositoryRoot,
        JSON.parse(readFileSync(moduleBundle, 'utf8')),
        relative(repositoryRoot, moduleContract)
      )
      if (moduleIds.has(metadata.id)) fail(`duplicate module fragment id ${metadata.id}`)
      moduleIds.add(metadata.id)

      const relativeGoOutput = relative(repositoryRoot, metadata.goOutput)
      const relativeTypescriptDirectory = relative(repositoryRoot, metadata.typescriptOutput)
      const moduleFiles = [
        relativeGoOutput,
        join(dirname(relativeGoOutput), 'openapi.json'),
        join(relativeTypescriptDirectory, 'schema.ts'),
        join(relativeTypescriptDirectory, 'client.ts')
      ]
      const moduleManifest = join(dirname(relativeGoOutput), 'openapi.manifest.json')
      moduleFiles.push(moduleManifest)
      for (const output of moduleFiles) {
        if (moduleOutputs.has(output) || canonicalGeneratedFiles.includes(output)) {
          fail(`multiple contracts generate ${output}`)
        }
        moduleOutputs.add(output)
        generatedFiles.push(output)
      }

      const goOutput = outputAt(outputRoot, moduleFiles[0])
      const runtimeSpec = outputAt(outputRoot, moduleFiles[1])
      const schemaOutput = outputAt(outputRoot, moduleFiles[2])
      const clientOutput = outputAt(outputRoot, moduleFiles[3])
      copyFileSync(moduleBundle, runtimeSpec)
      generateGo(moduleBundle, goOutput, metadata.goPackage)
      generateTypescript(moduleBundle, schemaOutput, clientOutput, moduleClient)
      writeFileSync(outputAt(outputRoot, moduleManifest), manifestContent(moduleFiles))
    }
  } finally {
    rmSync(stagingDirectory, { recursive: true, force: true })
  }
  return generatedFiles.sort((left, right) => left.localeCompare(right))
}

const manifestContent = generatedFiles => `${JSON.stringify({
  schemaVersion: 1,
  outputs: generatedFiles.map(path => path.split(sep).join('/')).sort()
}, null, 2)}\n`

const isCanonicalGeneratedOutput = path => {
  if (typeof path !== 'string' || path.length === 0 || isAbsolute(path) || path.includes('\\')) return false
  if (path.split('/').includes('..')) return false
  return canonicalGeneratedFiles.map(item => item.split(sep).join('/')).includes(path)
}

const readManifest = outputRoot => {
  const path = assertSafeOutputLocation(outputRoot, manifestPath)
  if (!existsSync(path)) return []
  let manifest
  try {
    manifest = JSON.parse(readFileSync(path, 'utf8'))
  } catch (error) {
    fail(`generated manifest is invalid: ${error instanceof Error ? error.message : String(error)}`)
  }
  if (manifest?.schemaVersion !== 1 || !Array.isArray(manifest.outputs)) {
    fail('generated manifest must use schemaVersion 1 and an outputs array')
  }
  const outputs = [...new Set(manifest.outputs)]
  const canonical = canonicalGeneratedFiles.map(path => path.split(sep).join('/')).sort()
  if (outputs.length !== manifest.outputs.length ||
      JSON.stringify([...outputs].sort()) !== JSON.stringify(canonical) ||
      outputs.some(path => !isCanonicalGeneratedOutput(path))) {
    fail('generated manifest must contain exactly the canonical output paths')
  }
  return outputs
}

const moduleManifestName = 'openapi.manifest.json'

const collectManagedModuleOutputs = (outputRoot, relativeRoot) => {
  const root = join(outputRoot, ...relativeRoot.split('/'))
  if (!lstatIfPresent(root)) return []
  assertSafeOutputLocation(outputRoot, relativeRoot, { directory: true })
  const outputs = []
  const visit = directory => {
    for (const entry of readdirSync(directory, { withFileTypes: true })) {
      const path = join(directory, entry.name)
      const relativePath = relative(outputRoot, path).split(sep).join('/')
      if (entry.isSymbolicLink()) fail(`module generated output tree contains a symbolic link: ${relativePath}`)
      if (entry.isDirectory()) {
        visit(path)
        continue
      }
      if (!entry.isFile()) continue
      const kind = parseManagedModuleOutput(relativePath)?.kind
      if (kind === 'go-code' || kind === 'go-spec' || kind === 'go-manifest' || kind === 'typescript-file') {
        outputs.push(relativePath)
      }
    }
  }
  visit(root)
  return outputs
}

const discoverManagedModuleOutputs = outputRoot => {
  const outputs = collectManagedModuleOutputs(outputRoot, 'backend/internal/modules')
  const domainsRoot = join(outputRoot, 'frontend', 'packages', 'domains')
  if (!lstatIfPresent(domainsRoot)) return outputs.sort((left, right) => left.localeCompare(right))
  assertSafeOutputLocation(outputRoot, 'frontend/packages/domains', { directory: true })
  for (const entry of readdirSync(domainsRoot, { withFileTypes: true })) {
    const ownerPath = join(domainsRoot, entry.name)
    if (entry.isSymbolicLink()) {
      fail(`module generated output tree contains a symbolic link: ${relative(outputRoot, ownerPath)}`)
    }
    if (!entry.isDirectory()) continue
    outputs.push(...collectManagedModuleOutputs(
      outputRoot,
      `frontend/packages/domains/${entry.name}/src`
    ))
  }
  return [...new Set(outputs)].sort((left, right) => left.localeCompare(right))
}

const discoverModuleManifests = outputRoot => {
  const modulesRoot = join(outputRoot, 'backend', 'internal', 'modules')
  if (!existsSync(modulesRoot)) return []
  assertSafeOutputLocation(outputRoot, join('backend', 'internal', 'modules'), { directory: true })
  const manifests = []
  const visit = directory => {
    for (const entry of readdirSync(directory, { withFileTypes: true })) {
      const path = join(directory, entry.name)
      if (entry.isSymbolicLink()) fail(`module generated output tree contains a symbolic link: ${relative(outputRoot, path)}`)
      if (entry.isDirectory()) {
        visit(path)
        continue
      }
      if (entry.isFile() && entry.name === moduleManifestName) manifests.push(path)
    }
  }
  visit(modulesRoot)
  return manifests.sort((left, right) => left.localeCompare(right))
}

const readModuleManifestOutputs = outputRoot => {
  const outputs = []
  for (const absoluteManifest of discoverModuleManifests(outputRoot)) {
    if (!lstatSync(absoluteManifest).isFile()) fail('module generated manifest must be a regular file')
    const manifestPath = relative(outputRoot, absoluteManifest).split(sep).join('/')
    const parsedManifest = parseManagedModuleOutput(manifestPath)
    if (parsedManifest?.kind !== 'go-manifest') fail(`unmanaged module generated manifest ${manifestPath}`)
    let manifest
    try {
      manifest = JSON.parse(readFileSync(absoluteManifest, 'utf8'))
    } catch (error) {
      fail(`module generated manifest is invalid: ${error instanceof Error ? error.message : String(error)}`)
    }
    if (manifest?.schemaVersion !== 1 || !Array.isArray(manifest.outputs)) {
      fail(`module generated manifest ${manifestPath} must use schemaVersion 1 and an outputs array`)
    }
    const typescriptDirectory = [
      'frontend', 'packages', 'domains', parsedManifest.owner, 'src',
      ...parsedManifest.nested, 'generated'
    ].join('/')
    const goDirectory = dirname(manifestPath).split(sep).join('/')
    const expected = [
      `${goDirectory}/openapi.gen.go`,
      `${goDirectory}/openapi.json`,
      manifestPath,
      `${typescriptDirectory}/client.ts`,
      `${typescriptDirectory}/schema.ts`
    ].sort()
    const unique = [...new Set(manifest.outputs)].sort()
    if (unique.length !== manifest.outputs.length || JSON.stringify(unique) !== JSON.stringify(expected)) {
      fail(`module generated manifest ${manifestPath} must list exactly its own generated outputs`)
    }
    outputs.push(...unique)
  }
  return [...new Set(outputs)]
}

const materializeGeneration = (stagingRoot, outputRoot, generatedFiles) => {
  const previousOutputs = [
    ...readManifest(outputRoot),
    ...readModuleManifestOutputs(outputRoot),
    ...discoverManagedModuleOutputs(outputRoot)
  ]
  const pending = []
  let temporaryManifest
  try {
    generatedFiles.forEach((path, index) => {
      const destination = assertSafeOutputLocation(outputRoot, path)
      mkdirSync(dirname(destination), { recursive: true })
      const temporary = join(dirname(destination), `.${basename(destination)}.contract-${process.pid}-${index}.tmp`)
      copyFileSync(join(stagingRoot, path), temporary)
      pending.push({ destination, temporary })
    })
    for (const { destination, temporary } of pending) renameSync(temporary, destination)

    const expected = new Set(generatedFiles.map(path => path.split(sep).join('/')))
    for (const stale of previousOutputs.filter(path => !expected.has(path))) {
      rmSync(assertSafeOutputLocation(outputRoot, stale), { force: true })
    }

    const manifest = assertSafeOutputLocation(outputRoot, manifestPath)
    mkdirSync(dirname(manifest), { recursive: true })
    temporaryManifest = `${manifest}.contract-${process.pid}.tmp`
    writeFileSync(temporaryManifest, manifestContent(canonicalGeneratedFiles))
    renameSync(temporaryManifest, manifest)
  } finally {
    for (const { temporary } of pending) rmSync(temporary, { force: true })
    if (temporaryManifest) rmSync(temporaryManifest, { force: true })
  }
}

export const synchronizeGeneration = (
  outputRoot = repositoryRoot,
  moduleContracts = discoverModuleContracts(repositoryRoot)
) => {
  const stagingRoot = mkdtempSync(join(tmpdir(), 'go-admin-generate-write-'))
  try {
    const generatedFiles = generate(stagingRoot, moduleContracts)
    materializeGeneration(stagingRoot, outputRoot, generatedFiles)
    return generatedFiles
  } finally {
    rmSync(stagingRoot, { recursive: true, force: true })
  }
}

export const checkGeneration = (
  outputRoot = repositoryRoot,
  moduleContracts = discoverModuleContracts(repositoryRoot)
) => {
  const stagingRoot = mkdtempSync(join(tmpdir(), 'go-admin-generate-check-'))
  try {
    const generatedFiles = generate(stagingRoot, moduleContracts)
    const drift = generatedFiles.filter(path => {
      const expected = assertSafeOutputLocation(outputRoot, path)
      const actual = join(stagingRoot, path)
      return !existsSync(expected) || readFileSync(expected).compare(readFileSync(actual)) !== 0
    })
    const expectedOutputs = new Set(generatedFiles.map(path => path.split(sep).join('/')))
    const managedOutputs = [
      ...readModuleManifestOutputs(outputRoot),
      ...discoverManagedModuleOutputs(outputRoot)
    ]
    for (const stale of [...new Set(managedOutputs)].filter(path => !expectedOutputs.has(path))) {
      drift.push(stale)
    }
    const manifest = assertSafeOutputLocation(outputRoot, manifestPath)
    if (!existsSync(manifest) || readFileSync(manifest, 'utf8') !== manifestContent(canonicalGeneratedFiles)) {
      drift.push(manifestPath)
    }
    if (drift.length) fail(`generated transport drift:\n- ${drift.join('\n- ')}`)
    return generatedFiles
  } finally {
    rmSync(stagingRoot, { recursive: true, force: true })
  }
}

const main = () => {
  const command = process.argv[2]
  switch (command) {
    case 'lint': {
      const inputs = optionValues('--contract')
      lintContracts(inputs.length ? inputs.map(input => resolve(repositoryRoot, input)) : allContracts())
      console.log('CONTRACT_LINT_PASS')
      break
    }
    case 'generate':
      lintAll()
      if (process.argv.includes('--check')) {
        checkGeneration()
        console.log('CONTRACT_GENERATE_CHECK_PASS')
      } else {
        synchronizeGeneration()
        console.log('CONTRACT_GENERATE_PASS')
      }
      break
    default:
      fail(`unknown command ${JSON.stringify(command)}`)
  }
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    main()
  } catch (error) {
    console.error(error instanceof Error ? error.message : String(error))
    process.exitCode = error instanceof ContractError ? error.exitCode : 1
  }
}
