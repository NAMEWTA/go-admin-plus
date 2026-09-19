#!/usr/bin/env node

import { spawnSync } from 'node:child_process'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

export const requiredPostgresSuites = Object.freeze([
  ['product-migrations', './internal/app/product', 'TestPostgresExplicitMigrationAndExactRuntimeSchema', 'self'],
  ['files-lifecycle', './internal/modules/files', 'TestPostgresAccountLifecycleTransferAndPurgeContract'],
  ['iam-deletion', './internal/modules/iam/administration', 'TestPostgresAccountDeletionLifecycleContract'],
  ['capability-registry', './internal/modules/iam/authorization', 'TestCapabilityRegistryPostgresContract'],
  ['bootstrap-recovery', './internal/modules/iam/bootstrap', 'TestPostgresConcurrentBootstrapAndRecoveryFence'],
  ['session-fencing', './internal/modules/iam/session', 'TestPostgresGenerationFencesConcurrentRenewalAndRevoke'],
  ['migration-convergence', './internal/platform/migrations', 'TestPostgresConcurrentProvidersConverge'],
  ['audit', './test/audit', 'TestAuditPostgresMigrationProjectionQueryAndCleanup'],
  ['files-contract', './test/files', 'TestFilesPostgresContract'],
  ['files-capacity', './test/files', 'TestFilesCapacityPostgresDialectContract'],
  ['authorization-fence', './test/iam/authorization', 'TestPostgresRevocationWaitsForFinalAuthorizationFence'],
  ['runtime-takeover', './test/reliable-runtime', 'TestPostgresExecutorExclusionAndTakeover'],
  ['runtime-fault', './test/reliable-runtime', 'TestPostgresDispatcherRecoversAcrossBackendTerminationAndProcesses'],
  ['scheduler', './test/scheduler', 'TestSchedulerPostgresRuntime'],
].map(([name, packagePath, test, isolation = 'schema']) => Object.freeze({ name, packagePath, test, isolation })))

export const parseGoTestEvents = (source, target) => {
  const counts = { run: 0, pass: 0, fail: 0, skip: 0 }
  for (const line of source.split(/\r?\n/)) {
    if (!line.trim()) continue
    let event
    try { event = JSON.parse(line) } catch { continue }
    if (event.Test !== target || !(event.Action in counts)) continue
    counts[event.Action] += 1
  }
  return counts
}

export const validateRequiredEnvironment = environment => {
  if (environment.GO_ADMIN_CI_REQUIRE_POSTGRES !== '1') throw new Error('GO_ADMIN_CI_REQUIRE_POSTGRES=1 is required')
  const dsn = environment.GO_ADMIN_TEST_POSTGRES_DISPOSABLE_DSN
  if (!dsn) throw new Error('GO_ADMIN_TEST_POSTGRES_DISPOSABLE_DSN is required')
  let parsed
  try { parsed = new URL(dsn) } catch { throw new Error('PostgreSQL DSN must be a URL') }
  if (!['postgres:', 'postgresql:'].includes(parsed.protocol) || !parsed.hostname || !parsed.pathname.slice(1)) {
    throw new Error('PostgreSQL DSN must identify a database')
  }
  return dsn
}

const requiredEnvironment = (environment, dsn) => ({
  ...environment,
  GO_ADMIN_TEST_POSTGRES_DISPOSABLE_DSN: dsn,
  GO_ADMIN_TEST_POSTGRES_FILES_LIFECYCLE_DSN: dsn,
  GO_ADMIN_TEST_POSTGRES_IAM_DELETION_DSN: dsn,
  GO_ADMIN_SCHEDULER_POSTGRES_DSN: dsn
})

const suiteDSN = (dsn, schema) => {
  const value = new URL(dsn)
  value.searchParams.set('search_path', schema)
  return value.toString()
}

const prepareSchema = ({ spawn, goRoot, environment, schema }) => {
  const result = spawn('go', ['run', './test/postgres/prepare', '--schema', schema], { cwd: goRoot, env: environment, encoding: 'utf8', maxBuffer: 1024 * 1024, timeout: 2 * 60 * 1000 })
  if (result.error || result.status !== 0) throw new Error(`required PostgreSQL schema preparation failed with status ${result.status ?? 'spawn'}`)
}

export const runRequiredPostgres = ({ root, environment = process.env, spawn = spawnSync, prepare = prepareSchema, suites = requiredPostgresSuites }) => {
  const dsn = validateRequiredEnvironment(environment)
  if (suites.length === 0) throw new Error('required PostgreSQL suite contains zero targets')
  const goRoot = join(root, 'backend')
  const runToken = `${environment.GITHUB_RUN_ID ?? process.pid}_${environment.GITHUB_RUN_ATTEMPT ?? '0'}`.replaceAll(/[^a-zA-Z0-9_]/g, '_').toLowerCase()
  const report = []
  for (const [index, suite] of suites.entries()) {
    const schema = `ci_${String(index + 1).padStart(2, '0')}_${suite.name.replaceAll('-', '_')}_${runToken}`
    if (suite.isolation !== 'self') prepare({ spawn, goRoot, environment, schema })
    const childEnvironment = requiredEnvironment(environment, suite.isolation === 'self' ? dsn : suiteDSN(dsn, schema))
    const result = spawn('go', ['test', '-json', '-count=1', '-timeout=20m', '-run', `^${suite.test}$`, suite.packagePath], {
      cwd: goRoot,
      env: childEnvironment,
      encoding: 'utf8',
      maxBuffer: 64 * 1024 * 1024,
      timeout: 25 * 60 * 1000
    })
    const output = `${result.stdout ?? ''}\n${result.stderr ?? ''}`
    const counts = parseGoTestEvents(output, suite.test)
    if (result.error || result.status !== 0 || counts.run !== 1 || counts.pass !== 1 || counts.fail !== 0 || counts.skip !== 0) {
      throw new Error(`required PostgreSQL suite ${suite.name} failed (status=${result.status ?? 'spawn'}, run=${counts.run}, pass=${counts.pass}, fail=${counts.fail}, skip=${counts.skip})`)
    }
    report.push({ name: suite.name, test: suite.test, schema, executed: counts.run, passed: counts.pass, skipped: counts.skip })
  }
  return report
}

const repositoryRoot = resolve(dirname(fileURLToPath(import.meta.url)), '../..')
if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const report = runRequiredPostgres({ root: repositoryRoot })
    console.log(`REQUIRED_POSTGRES_PASS executed=${report.length} skipped=0`)
  } catch (error) {
    console.error(`REQUIRED_POSTGRES_FAIL ${error instanceof Error ? error.message : 'unknown failure'}`)
    process.exit(1)
  }
}
