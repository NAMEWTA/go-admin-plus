#!/usr/bin/env node

import { existsSync, readFileSync, readdirSync } from 'node:fs'
import { basename, dirname, extname, join, relative, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const commandExtensions = new Set(['.cjs', '.js', '.json', '.md', '.mjs', '.ps1', '.sh', '.ts', '.yaml', '.yml'])
const ignoredCommandDirectories = new Set(['node_modules', 'dist', 'target'])

const commandFiles = directory => {
  if (!existsSync(directory)) return []
  return readdirSync(directory, { withFileTypes: true }).flatMap(entry => {
    if (ignoredCommandDirectories.has(entry.name)) return []
    const path = join(directory, entry.name)
    if (entry.isDirectory()) return commandFiles(path)
    return commandExtensions.has(extname(entry.name)) || /^(?:Containerfile|Taskfile)/.test(entry.name) ? [path] : []
  })
}

const workspacePackages = root => {
  const workspaceRoot = join(root, 'frontend')
  const packages = []
  for (const packageRoot of ['apps', 'packages', 'packages/adapters', 'packages/domains', 'packages/web-domains']) {
    const directory = join(workspaceRoot, packageRoot)
    if (!existsSync(directory)) continue
    for (const entry of readdirSync(directory, { withFileTypes: true })) {
      const packageDirectory = join(directory, entry.name)
      const manifestPath = join(packageDirectory, 'package.json')
      if (entry.isDirectory() && existsSync(manifestPath)) {
        packages.push({ directory: packageDirectory, manifest: JSON.parse(readFileSync(manifestPath, 'utf8')) })
      }
    }
  }
  return packages
}

const workspacePackageNames = root => {
  const workspaceRoot = join(root, 'frontend')
  const names = new Set(workspacePackages(root).map(({ manifest }) => manifest.name))
  const rootManifest = join(workspaceRoot, 'package.json')
  if (existsSync(rootManifest)) names.add(JSON.parse(readFileSync(rootManifest, 'utf8')).name)
  return names
}

const workflowJob = (source, id) => {
  const lines = source.split('\n')
  const start = lines.findIndex(line => line === `  ${id}:`)
  if (start === -1) return ''
  const nextJob = lines.slice(start + 1).findIndex(line => /^  [A-Za-z0-9_-]+:\s*$/.test(line))
  const end = nextJob === -1 ? lines.length : start + nextJob + 1
  return lines.slice(start, end).join('\n')
}

export const checkArchitecture = root => {
  const failures = []
  const canonicalGoModule = 'github.com/NAMEWTA/go-admin-plus/backend'
  const canonicalWorkspaceName = '@go-admin-plus/workspace'
  const canonicalTaskVersion = '3.48.0'
  const required = [
    'Taskfile.yml', '.github/workflows/ci.yml', 'backend/go.mod',
    'backend/cmd/server/main.go', 'backend/cmd/desktop-sidecar/main.go',
    'backend/internal/app/product/registry.go', 'backend/internal/modules',
    'frontend/package.json', 'frontend/pnpm-workspace.yaml',
    'frontend/tests/shell/vitest.config.ts',
    'frontend/tests/shell/node-tests.mjs',
    'frontend/apps/admin-web/package.json',
    'frontend/apps/admin-desktop/src-tauri/tauri.conf.json',
    'scripts/backend/pnpm.sh',
    'scripts/frontend/build.sh',
    'scripts/frontend/package.sh'
  ]
  const forbidden = ['go-admin-ui-plus', 'backend/app', 'backend/common', 'backend/api', 'backend/cmd/go-admin-desktop', 'backend/cmd/config-check', 'backend/cmd/migrate']
  for (const path of required) if (!existsSync(join(root, path))) failures.push(`missing canonical path: ${path}`)
  for (const path of forbidden) if (existsSync(join(root, path))) failures.push(`removed path still exists: ${path}`)

  const goModulePath = join(root, 'backend/go.mod')
  if (existsSync(goModulePath)) {
    const declaration = readFileSync(goModulePath, 'utf8').match(/^module\s+(\S+)$/m)?.[1]
    if (declaration !== canonicalGoModule) failures.push(`Go module path must be ${canonicalGoModule}`)
  }

  const frontendManifestPath = join(root, 'frontend/package.json')
  let frontendManifest
  if (existsSync(frontendManifestPath)) {
    frontendManifest = JSON.parse(readFileSync(frontendManifestPath, 'utf8'))
    if (frontendManifest.name !== canonicalWorkspaceName) failures.push(`frontend workspace name must be ${canonicalWorkspaceName}`)
    const firstTypecheckCommand = frontendManifest.scripts?.typecheck?.split('&&', 1)[0]?.trim()
    if (firstTypecheckCommand !== 'corepack pnpm --recursive --if-present typecheck') {
      failures.push('frontend root typecheck must recursively run every workspace package typecheck script')
    }
    if (!frontendManifest.scripts?.test?.includes('node tests/shell/node-tests.mjs')) {
      failures.push('frontend root test must run Node unit test discovery')
    }
  }

  for (const { directory, manifest } of workspacePackages(root)) {
    if (!manifest.scripts?.typecheck) failures.push(`${manifest.name} must declare a typecheck script`)
    const ownsSpecs = commandFiles(directory).some(path => /\.spec\.[cm]?[jt]sx?$/.test(path))
    if (ownsSpecs && !manifest.scripts?.test) {
      failures.push(`${manifest.name} owns package specs and must declare a test script`)
    }
  }

  const packageNames = workspacePackageNames(root)
  const commandRoots = ['.github', 'scripts', 'release', 'deploy']
  const surfaces = [join(root, 'Taskfile.yml'), ...commandRoots.flatMap(path => commandFiles(join(root, path)))]
  for (const file of surfaces.filter(existsSync)) {
    const source = readFileSync(file, 'utf8')
    for (const match of source.matchAll(/@go-admin-plus\/[A-Za-z0-9._-]+/g)) {
      if (!packageNames.has(match[0])) failures.push(`${relative(root, file)} references unknown workspace package ${match[0]}`)
    }
  }

  const packageScriptPath = join(root, 'scripts/frontend/package.sh')
  if (existsSync(packageScriptPath)) {
    const packageScript = readFileSync(packageScriptPath, 'utf8')
    const requiredPackageContracts = [
      [/GO_ADMIN_BUILD_DIR="\$web_dist" run_pnpm --filter @go-admin-plus\/admin-web build/, 'local package script must build Web into the artifacts staging directory'],
      [/tar -C "\$web_stage" -czf "\$package_tmp" dist/, 'local package script must archive the staged Web dist'],
      [/case \$\(go env GOHOSTOS\) in/, 'local package script must select Desktop bundles from GOHOSTOS'],
      [/darwin\) desktop_bundle=app/, 'local package script must support the macOS app bundle'],
      [/windows\) desktop_bundle=nsis/, 'local package script must support the Windows NSIS bundle'],
      [/tauri build \\\n\s+--features custom-protocol --bundles "\$desktop_bundle"/, 'local package script must enable the Tauri production protocol'],
      [/apps\/admin-desktop\/scripts\/verify-build\.mjs/, 'local Desktop package must verify production WebView, sidecar, and host artifacts']
    ]
    for (const [pattern, message] of requiredPackageContracts) {
      if (!pattern.test(packageScript)) failures.push(message)
    }
    if (/pnpm build:prod/.test(packageScript)) failures.push('local package script must not invoke the aggregate frontend build')
  }

  const buildScriptPath = join(root, 'scripts/frontend/build.sh')
  if (existsSync(buildScriptPath)) {
    const buildScript = readFileSync(buildScriptPath, 'utf8')
    if (!/node "\$repo_root\/release\/shared\/sidecar\/build\.mjs" --host/.test(buildScript)) {
      failures.push('Desktop build must stage the host Go sidecar')
    }
    if (!/run_pnpm --filter @go-admin-plus\/admin-desktop tauri build[\s\\]+--features custom-protocol --no-bundle/.test(buildScript)) {
      failures.push('Desktop build must compile the Tauri host without bundling')
    }
    if (!/apps\/admin-desktop\/scripts\/verify-build\.mjs/.test(buildScript)) {
      failures.push('Desktop build must verify production WebView, sidecar, and host artifacts')
    }
    if (!/all\)\s*\n\s*build_web\s*\n\s*build_desktop/.test(buildScript)) {
      failures.push('aggregate product build must include native Desktop')
    }
    if (!/desktop\)\s*\n\s*build_desktop/.test(buildScript)) {
      failures.push('Desktop target must use the native build')
    }
    if (/pnpm build:prod/.test(buildScript)) failures.push('product build must not stop at aggregate WebView assets')
  }

  const frontendTaskScriptRoot = join(root, 'scripts/frontend')
  if (existsSync(frontendTaskScriptRoot)) {
    for (const entry of readdirSync(frontendTaskScriptRoot, { withFileTypes: true })) {
      if (!entry.isFile() || extname(entry.name) !== '.sh' || entry.name === 'common.sh') continue
      const path = join(frontendTaskScriptRoot, entry.name)
      if (/\b(?:exec\s+)?pnpm\s/.test(readFileSync(path, 'utf8'))) {
        failures.push('frontend task script must use managed pnpm resolution: ' + relative(root, path).replaceAll('\\', '/'))
      }
    }
  }

  const ciWorkflowPath = join(root, '.github/workflows/ci.yml')
  if (existsSync(ciWorkflowPath)) {
    const ciWorkflow = readFileSync(ciWorkflowPath, 'utf8')
    const qualityJob = workflowJob(ciWorkflow, 'quality')
    if (!qualityJob.includes(`go install github.com/go-task/task/v3/cmd/task@v${canonicalTaskVersion}`)) {
      failures.push(`quality CI must install Go Task ${canonicalTaskVersion}`)
    }
    const backendJob = workflowJob(ciWorkflow, 'backend')
    if (!/timeout-minutes: 60/.test(backendJob)) {
      failures.push('backend CI must reserve 60 minutes for the Go test matrix')
    }
    const postgresJob = workflowJob(ciWorkflow, 'postgres-required')
    const postgresContracts = [
      ['postgres:17.11-bookworm@sha256:051f7b7b3abdd564d5d1bd1e8c4b9c1b6e77087d1dd22020ede611c096a272e0', 'required PostgreSQL CI must pin the service version and manifest digest'],
      ['GO_ADMIN_CI_REQUIRE_POSTGRES: "1"', 'required PostgreSQL CI must enable the non-skip contract'],
      ['GO_ADMIN_TEST_POSTGRES_DISPOSABLE_DSN:', 'required PostgreSQL CI must inject a disposable DSN'],
      ['pg_isready -h 127.0.0.1', 'required PostgreSQL CI must assert service health'],
      ['node scripts/ci/required-postgres.mjs', 'required PostgreSQL CI must use the counted non-skip runner']
    ]
    for (const [contract, message] of postgresContracts) if (!postgresJob.includes(contract)) failures.push(message)
    if (/continue-on-error:|\|\|\s*true/.test(postgresJob)) failures.push('required PostgreSQL CI must not allow failures')
    const securityJob = workflowJob(ciWorkflow, 'security-supply-chain')
    const securityContracts = [
      ['GO_ADMIN_CI_REQUIRE_SECURITY: "1"', 'security CI must enable the required gate contract'],
      ['govulncheck@v1.1.4', 'security CI must pin govulncheck'],
      ['cargo-audit --version 0.22.2 --locked', 'security CI must pin cargo-audit'],
      ['cargo-deny --version 0.20.2 --locked', 'security CI must pin cargo-deny'],
      ['node scripts/security/required-security.mjs', 'security CI must run the repository security gate'],
      ['actions/upload-artifact@ea165f8d65b6e75b540449e92b4886f43607fa02', 'security CI must pin evidence upload']
    ]
    for (const [contract, message] of securityContracts) if (!securityJob.includes(contract)) failures.push(message)
    if (/continue-on-error:|\|\|\s*true/.test(securityJob)) failures.push('security CI must not allow failures')
    const desktopJob = workflowJob(ciWorkflow, 'desktop-rust')
    if (!/pnpm --dir frontend install --frozen-lockfile/.test(desktopJob)) {
      failures.push('Desktop CI must install the frozen frontend workspace')
    }
    if (!/node release\/shared\/sidecar\/build\.mjs --host/.test(desktopJob)) {
      failures.push('Desktop CI must stage the host Go sidecar')
    }
    if (!/pnpm --dir frontend --filter @go-admin-plus\/admin-desktop tauri build \\\n+\s+--features custom-protocol --no-bundle/.test(desktopJob)) {
      failures.push('Desktop CI must link the Tauri host without bundling')
    }
    if (!/node frontend\/apps\/admin-desktop\/scripts\/verify-build\.mjs/.test(desktopJob)) {
      failures.push('Desktop CI must verify production WebView, sidecar, and host artifacts')
    }
  }

  const requiredPostgresRunnerPath = join(root, 'scripts/ci/required-postgres.mjs')
  if (existsSync(requiredPostgresRunnerPath)) {
    const runner = readFileSync(requiredPostgresRunnerPath, 'utf8')
    for (const contract of ['counts.skip !== 0', 'counts.run !== 1', 'suites.length === 0', 'GO_ADMIN_CI_REQUIRE_POSTGRES']) {
      if (!runner.includes(contract)) failures.push(`required PostgreSQL runner is missing non-skip contract: ${contract}`)
    }
  }
  const requiredSecurityRunnerPath = join(root, 'scripts/security/required-security.mjs')
  if (existsSync(requiredSecurityRunnerPath)) {
    const runner = readFileSync(requiredSecurityRunnerPath, 'utf8')
    for (const contract of ['govulncheck', 'pnpm-production-audit', 'cargo-audit', 'cargo-deny', 'secret-scan', 'sbom', 'generate-drift', 'GO_ADMIN_CI_REQUIRE_SECURITY']) {
      if (!runner.includes(contract)) failures.push(`required security runner is missing gate: ${contract}`)
    }
    for (const image of runner.matchAll(/['"]([^'"]+@sha256:[0-9a-f]+)['"]/g)) {
      if (!/@sha256:[0-9a-f]{64}$/.test(image[1])) failures.push('security scanner image digest must contain 64 hexadecimal characters')
    }
    const denyPolicyPath = join(root, 'scripts/security/deny.toml')
    const denyPolicy = existsSync(denyPolicyPath) ? readFileSync(denyPolicyPath, 'utf8') : ''
    for (const contract of ['wildcards = "deny"', 'unknown-registry = "deny"', 'unknown-git = "deny"']) {
      if (!denyPolicy.includes(contract)) failures.push(`cargo-deny policy is missing required contract: ${contract}`)
    }
    const gitleaksPolicyPath = join(root, 'scripts/security/gitleaks.toml')
    const gitleaksPolicy = existsSync(gitleaksPolicyPath) ? readFileSync(gitleaksPolicyPath, 'utf8') : ''
    for (const contract of ['useDefault = true', '0391c8816cb97e8c68e61ec7ef56715046f8115f', 'condition = "AND"', 'regexTarget = "line"']) {
      if (!gitleaksPolicy.includes(contract)) failures.push(`gitleaks policy is missing required narrow allowlist contract: ${contract}`)
    }
    if (/\.\*test\\\.go|backend\/internal\/.*\*\*/.test(gitleaksPolicy)) failures.push('gitleaks policy must not allowlist broad test or source paths')
  }

  const pnpmResolverPath = join(root, 'scripts/backend/pnpm.sh')
  if (existsSync(pnpmResolverPath)) {
    const pnpmResolver = readFileSync(pnpmResolverPath, 'utf8')
    const requiredPnpmContracts = [
      'required_pnpm_version=11.1.3',
      'exec corepack pnpm@$required_pnpm_version',
      'test "$installed_pnpm_version" = "$required_pnpm_version"'
    ]
    if (requiredPnpmContracts.some(contract => !pnpmResolver.includes(contract))) {
      failures.push('managed command resolver must require pnpm 11.1.3')
    }
  }

  for (const relativePath of ['scripts/backend/dev.sh', 'scripts/backend/test.sh']) {
    const path = join(root, relativePath)
    if (existsSync(path) && !readFileSync(path, 'utf8').includes('require_pnpm')) {
      failures.push(`${relativePath} must prepare the managed pnpm toolchain`)
    }
  }

  const taskfilePath = join(root, 'Taskfile.yml')
  if (existsSync(taskfilePath)) {
    const taskfile = readFileSync(taskfilePath, 'utf8')
    if (!taskfile.includes('scripts/contracts/generate.sh verify') ||
        !taskfile.includes('scripts/contracts/generate.sh generate --check')) {
      failures.push('contract tasks must use the managed pnpm wrapper')
    }
  }

  const taskContractPath = join(root, 'scripts/backend/task-contract.sh')
  if (existsSync(taskContractPath)) {
    const taskContract = readFileSync(taskContractPath, 'utf8')
    const requiredTaskChecks = [
      `required_task_version=${canonicalTaskVersion}`,
      'task_version=$("$task_command" --version',
      'test "$task_version" = "$required_task_version"'
    ]
    if (requiredTaskChecks.some(contract => !taskContract.includes(contract))) {
      failures.push(`root command contract must require Go Task ${canonicalTaskVersion}`)
    }
  }

  const readDocument = path => existsSync(join(root, path)) ? readFileSync(join(root, path), 'utf8') : ''
  const readme = readDocument('README.md')
  if (readme && !readme.includes(`Go Task ${canonicalTaskVersion}`)) {
    failures.push(`README must declare Go Task ${canonicalTaskVersion}`)
  }
  const developmentGuide = readDocument('docs/development.md')
  if (developmentGuide) {
    if (!developmentGuide.includes(`go install github.com/go-task/task/v3/cmd/task@v${canonicalTaskVersion}`)) {
      failures.push(`development guide must install Go Task ${canonicalTaskVersion} reproducibly`)
    }
    if (!developmentGuide.includes('Node.js 22.22.3')) {
      failures.push('development guide must record the Node.js 22.22.3 CI baseline')
    }
    if (!developmentGuide.includes('Rust 1.96.0')) {
      failures.push('development guide must record the Rust 1.96.0 CI baseline')
    }
    if (!developmentGuide.includes('corepack pnpm@11.1.3')) {
      failures.push('development guide must pin pnpm 11.1.3 in the Corepack command')
    }
  }

  const internalRoot = join(root, 'backend/internal')
  if (existsSync(internalRoot)) {
    const allowed = new Set(['app', 'application', 'contracts', 'host', 'modules', 'platform'])
    for (const entry of readdirSync(internalRoot, { withFileTypes: true })) {
      if (entry.isDirectory() && !allowed.has(entry.name)) failures.push(`backend layer is outside the canonical architecture: internal/${entry.name}`)
    }
  }
  const commandRoot = join(root, 'backend/cmd')
  if (existsSync(commandRoot)) {
    const allowed = new Set(['desktop-sidecar', 'server'])
    for (const entry of readdirSync(commandRoot, { withFileTypes: true })) {
      if (entry.isDirectory() && !allowed.has(entry.name)) failures.push(`backend command is outside the canonical command plane: cmd/${entry.name}`)
    }
  }
  const workspacePath = join(root, 'frontend/pnpm-workspace.yaml')
  if (existsSync(workspacePath)) {
    const workspace = readFileSync(workspacePath, 'utf8')
    for (const pattern of ['apps/*', 'packages/*', 'packages/adapters/*', 'packages/domains/*', 'packages/web-domains/*']) {
      if (!workspace.includes(pattern)) failures.push(`workspace does not declare ${pattern}`)
    }
  }
  const frontendTestConfigPath = join(root, 'frontend/tests/shell/vitest.config.ts')
  if (existsSync(frontendTestConfigPath)) {
    const config = readFileSync(frontendTestConfigPath, 'utf8')
    if (!/['"]packages\/\*\*\/\*\.spec\.ts['"]/.test(config)) {
      failures.push('frontend test discovery must include every workspace package spec')
    }
    if (!/['"]tests\/e2e\/\*\*\/\*\.spec\.ts['"]/.test(config)) {
      failures.push('frontend test discovery must include E2E harness unit specs')
    }
  }
  if (frontendManifest) {
    const frontendRoot = join(root, 'frontend')
    const typecheck = frontendManifest.scripts?.typecheck ?? ''
    const testProjects = commandFiles(join(frontendRoot, 'tests')).filter(path => basename(path) === 'tsconfig.json')
    for (const project of testProjects) {
      const projectPath = relative(frontendRoot, project).replaceAll('\\', '/')
      if (!typecheck.includes(projectPath)) failures.push(`frontend root typecheck omits test project ${projectPath}`)
    }
  }
  return failures
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const root = resolve(process.argv[2] ?? dirname(fileURLToPath(import.meta.url)), process.argv[2] ? '.' : '../..')
  const failures = checkArchitecture(root)
  if (failures.length) {
    console.error(`ARCHITECTURE_CHECK_FAIL\n${failures.join('\n')}`)
    process.exit(1)
  }
  console.log('ARCHITECTURE_CHECK_PASS')
}
