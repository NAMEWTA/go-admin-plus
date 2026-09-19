import { readdirSync, realpathSync, statSync } from 'node:fs'
import { isAbsolute, join, relative, resolve, sep } from 'node:path'

const moduleIdPattern = /^[a-z][a-z0-9-]{1,63}$/
const goPackagePattern = /^[a-z][a-z0-9_]*$/
const modulePathSegmentPattern = /^[a-z][a-z0-9_-]{0,63}$/

const validNestedSegments = segments => segments.every(segment => modulePathSegmentPattern.test(segment))

// This parser is the single authority for module generation targets and manifest deletion entries.
export const parseManagedModuleOutput = value => {
  if (typeof value !== 'string' || value.length === 0 || isAbsolute(value) || value.includes('\\')) return undefined
  const segments = value.split('/')
  if (segments.some(segment => segment.length === 0 || segment === '.' || segment === '..')) return undefined

  if (segments.length >= 6 && segments.slice(0, 3).join('/') === 'backend/internal/modules') {
    const owner = segments[3]
    const nested = segments.slice(4, -2)
    const directory = segments.at(-2)
    const filename = segments.at(-1)
    if (!moduleIdPattern.test(owner) || !validNestedSegments(nested) || directory !== 'transport') return undefined
    if (filename === 'openapi.gen.go') return { kind: 'go-code', nested, owner, value }
    if (filename === 'openapi.json') return { kind: 'go-spec', nested, owner, value }
    if (filename === 'openapi.manifest.json') return { kind: 'go-manifest', nested, owner, value }
    return undefined
  }

  if (segments.slice(0, 3).join('/') === 'frontend/packages/domains') {
    const owner = segments[3]
    if (!moduleIdPattern.test(owner) || segments[4] !== 'src') return undefined
    const filename = segments.at(-1)
    const hasFile = filename === 'schema.ts' || filename === 'client.ts'
    const generatedIndex = hasFile ? segments.length - 2 : segments.length - 1
    if (segments[generatedIndex] !== 'generated') return undefined
    const nested = segments.slice(5, generatedIndex)
    if (!validNestedSegments(nested)) return undefined
    return { kind: hasFile ? 'typescript-file' : 'typescript-directory', nested, owner, value }
  }

  return undefined
}

export const isManagedGeneratedOutput = value => {
  const parsed = parseManagedModuleOutput(value)
  return parsed?.kind === 'go-code' || parsed?.kind === 'go-spec' ||
    parsed?.kind === 'go-manifest' || parsed?.kind === 'typescript-file'
}

export const resolveModuleMetadata = (repositoryRoot, document, source = 'module contract') => {
  const id = document?.['x-go-admin-module']
  if (typeof id !== 'string' || !moduleIdPattern.test(id)) {
    throw new Error(`${source}: module id must match ${moduleIdPattern}`)
  }

  const codegen = document?.['x-go-admin-codegen']
  if (!codegen || typeof codegen !== 'object' || Array.isArray(codegen)) {
    throw new Error(`${source}: module codegen metadata is required`)
  }
  const codegenKeys = Object.keys(codegen).sort()
  const expectedCodegenKeys = ['goOutput', 'goPackage', 'owner', 'typescriptOutput']
  if (JSON.stringify(codegenKeys) !== JSON.stringify(expectedCodegenKeys)) {
    throw new Error(`${source}: module codegen metadata must contain only ${expectedCodegenKeys.join(', ')}`)
  }
  if (typeof codegen.goPackage !== 'string' || !goPackagePattern.test(codegen.goPackage)) {
    throw new Error(`${source}: Go package must match ${goPackagePattern}`)
  }
  if (typeof codegen.owner !== 'string' || !moduleIdPattern.test(codegen.owner)) {
    throw new Error(`${source}: module owner must match ${moduleIdPattern}`)
  }

  const parsedGoOutput = parseManagedModuleOutput(codegen.goOutput)
  if (parsedGoOutput?.kind !== 'go-code' || parsedGoOutput.owner !== codegen.owner) {
    throw new Error(`${source}: Go output must be a managed transport/openapi.gen.go path for owner ${codegen.owner}`)
  }

  const parsedTypescriptOutput = parseManagedModuleOutput(codegen.typescriptOutput)
  if (parsedTypescriptOutput?.kind !== 'typescript-directory' || parsedTypescriptOutput.owner !== codegen.owner) {
    throw new Error(`${source}: TypeScript output must be a managed owner src path ending with generated`)
  }
  if (JSON.stringify(parsedGoOutput.nested) !== JSON.stringify(parsedTypescriptOutput.nested)) {
    throw new Error(`${source}: Go and TypeScript outputs must use the same nested module path`)
  }

  return {
    id,
    owner: codegen.owner,
    goPackage: codegen.goPackage,
    goOutput: resolve(repositoryRoot, parsedGoOutput.value),
    typescriptOutput: resolve(repositoryRoot, parsedTypescriptOutput.value),
    source
  }
}

export const discoverModuleContracts = repositoryRoot => {
  const directory = join(repositoryRoot, 'contracts', 'openapi', 'modules')
  const realDirectory = realpathSync(directory)

  return readdirSync(directory, { withFileTypes: true })
    .filter(entry => !entry.name.startsWith('_') && /\.(?:json|ya?ml)$/i.test(entry.name))
    .map(entry => {
      if (!entry.isFile()) throw new Error(`${entry.name}: module contract must be a regular file`)
      const path = join(directory, entry.name)
      const realPath = realpathSync(path)
      const location = relative(realDirectory, realPath)
      if (location === '..' || location.startsWith(`..${sep}`) || isAbsolute(location)) {
        throw new Error(`${entry.name}: module contract resolves outside the modules directory`)
      }
      if (!statSync(realPath).isFile()) throw new Error(`${entry.name}: module contract must be a regular file`)
      return path
    })
    .sort((left, right) => left.localeCompare(right))
}
