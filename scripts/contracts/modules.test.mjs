import assert from 'node:assert/strict'
import { resolve } from 'node:path'
import test from 'node:test'
import { isManagedGeneratedOutput, parseManagedModuleOutput, resolveModuleMetadata } from './modules.mjs'

const repositoryRoot = resolve('/workspace/product')

test('resolves module transport outputs inside owner roots', () => {
  const metadata = resolveModuleMetadata(repositoryRoot, {
    'x-go-admin-module': 'files',
    'x-go-admin-codegen': {
      owner: 'files',
      goPackage: 'filestransport',
      goOutput: 'backend/internal/modules/files/transport/openapi.gen.go',
      typescriptOutput: 'frontend/packages/domains/files/src/generated'
    }
  }, 'files.yaml')

  assert.equal(metadata.id, 'files')
  assert.equal(metadata.goPackage, 'filestransport')
  assert.equal(metadata.goOutput, resolve(repositoryRoot, 'backend/internal/modules/files/transport/openapi.gen.go'))
  assert.equal(metadata.owner, 'files')
  assert.equal(metadata.typescriptOutput, resolve(repositoryRoot, 'frontend/packages/domains/files/src/generated'))
})

for (const [name, document] of [
  ['invalid module id', {
    'x-go-admin-module': '../files',
    'x-go-admin-codegen': { owner: 'files', goPackage: 'files', goOutput: 'backend/internal/modules/files/transport/openapi.gen.go', typescriptOutput: 'frontend/packages/domains/files/src/generated' }
  }],
  ['Go output traversal', {
    'x-go-admin-module': 'files',
    'x-go-admin-codegen': { owner: 'files', goPackage: 'files', goOutput: '../outside.go', typescriptOutput: 'frontend/packages/domains/files/src/generated' }
  }],
  ['TypeScript output traversal', {
    'x-go-admin-module': 'files',
    'x-go-admin-codegen': { owner: 'files', goPackage: 'files', goOutput: 'backend/internal/modules/files/transport/openapi.gen.go', typescriptOutput: '/tmp/generated' }
  }],
  ['cross-module output', {
    'x-go-admin-module': 'files',
    'x-go-admin-codegen': { owner: 'files', goPackage: 'files', goOutput: 'backend/internal/modules/audit/transport/openapi.gen.go', typescriptOutput: 'frontend/packages/domains/files/src/generated' }
  }],
  ['Go output missing the owner transport directory', {
    'x-go-admin-module': 'transport-fragment',
    'x-go-admin-codegen': { owner: 'transport', goPackage: 'transport', goOutput: 'backend/internal/modules/transport/openapi.gen.go', typescriptOutput: 'frontend/packages/domains/transport/src/generated' }
  }],
  ['unknown codegen metadata', {
    'x-go-admin-module': 'files',
    'x-go-admin-codegen': { owner: 'files', goPackage: 'files', goOutput: 'backend/internal/modules/files/transport/openapi.gen.go', typescriptOutput: 'frontend/packages/domains/files/src/generated', unsupported: true }
  }]
]) {
  test(`rejects ${name}`, () => {
    assert.throws(() => resolveModuleMetadata(repositoryRoot, document, 'fixture.yaml'), /module|output|package/i)
  })
}

test('allows multiple fragments to write inside one explicit module owner', () => {
  const metadata = resolveModuleMetadata(repositoryRoot, {
    'x-go-admin-module': 'iam-session',
    'x-go-admin-codegen': {
      owner: 'iam',
      goPackage: 'sessiontransport',
      goOutput: 'backend/internal/modules/iam/session/transport/openapi.gen.go',
      typescriptOutput: 'frontend/packages/domains/iam/src/session/generated'
    }
  }, 'iam-session.yaml')

  assert.equal(metadata.id, 'iam-session')
  assert.equal(metadata.owner, 'iam')
})

test('uses one path grammar for nested generation targets and manifest entries', () => {
  const metadata = resolveModuleMetadata(repositoryRoot, {
    'x-go-admin-module': 'iam-session',
    'x-go-admin-codegen': {
      owner: 'iam',
      goPackage: 'sessionv2transport',
      goOutput: 'backend/internal/modules/iam/session_v2/transport/openapi.gen.go',
      typescriptOutput: 'frontend/packages/domains/iam/src/session_v2/generated'
    }
  }, 'iam-session.yaml')

  assert.equal(parseManagedModuleOutput('backend/internal/modules/iam/session_v2/transport/openapi.gen.go')?.owner, 'iam')
  assert.equal(parseManagedModuleOutput('frontend/packages/domains/iam/src/session_v2/generated')?.kind, 'typescript-directory')
  assert.equal(isManagedGeneratedOutput('frontend/packages/domains/iam/src/session_v2/generated/client.ts'), true)
  assert.equal(isManagedGeneratedOutput('frontend/packages/domains/iam/manual/generated/client.ts'), false)
  assert.equal(isManagedGeneratedOutput('backend/internal/modules/transport/openapi.gen.go'), false)
  assert.match(metadata.goOutput, /session_v2/)
})

test('rejects mismatched nested Go and TypeScript slice paths', () => {
  assert.throws(() => resolveModuleMetadata(repositoryRoot, {
    'x-go-admin-module': 'iam-session',
    'x-go-admin-codegen': {
      owner: 'iam',
      goPackage: 'iamsessiontransport',
      goOutput: 'backend/internal/modules/iam/session/transport/openapi.gen.go',
      typescriptOutput: 'frontend/packages/domains/iam/src/administration/generated'
    }
  }), /same nested module path/)
})
