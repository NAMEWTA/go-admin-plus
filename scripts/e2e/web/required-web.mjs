import { spawnSync } from 'node:child_process'
import { existsSync, statSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const repositoryRoot = resolve(dirname(fileURLToPath(import.meta.url)), '../../..')

export const suites = Object.freeze([
  { name: 'product-shell', path: 'frontend/tests/e2e/web-shell/run.mjs', require: 'GO_ADMIN_REQUIRE_WEB_SHELL_E2E', marker: 'WEB_SHELL_E2E_PASS profiles=sqlite,postgres' },
  { name: 'iam-session', path: 'frontend/tests/e2e/iam/session/run.mjs', require: 'GO_ADMIN_REQUIRE_IAM_E2E', marker: 'IAM_SESSION_E2E_PASS profiles=sqlite,postgres' },
  { name: 'iam-administration', path: 'frontend/tests/e2e/iam/administration/run.mjs', require: 'GO_ADMIN_REQUIRE_IAM_ADMIN_E2E', marker: 'IAM_ADMIN_E2E_PASS profiles=sqlite,postgres' },
  { name: 'audit', path: 'frontend/tests/e2e/audit/run.mjs', require: 'GO_ADMIN_REQUIRE_AUDIT_E2E', marker: 'AUDIT_E2E_PASS' },
  { name: 'scheduler', path: 'frontend/tests/e2e/scheduler/run.mjs', require: 'GO_ADMIN_REQUIRE_SCHEDULER_E2E', marker: 'SCHEDULER_E2E_PASS profiles=sqlite,postgres' },
  { name: 'files', path: 'frontend/tests/e2e/files/run.mjs', require: 'GO_ADMIN_REQUIRE_FILES_E2E', marker: 'FILES_E2E_PASS profiles=sqlite,postgres' },
])

const fail = message => {
  console.error(`REQUIRED_WEB_E2E_FAIL ${message}`)
  return 1
}

const regularFile = path => {
  try { return statSync(path).isFile() } catch { return false }
}

export const verifySuiteResult = (suite, result) => {
  const output = `${result.stdout ?? ''}\n${result.stderr ?? ''}`
  if (result.error || result.signal || result.status !== 0) return `${suite.name}: child failed`
  if (/_E2E_SKIP\b/.test(output)) return `${suite.name}: skip output is forbidden`
  if (!output.includes(suite.marker)) return `${suite.name}: pass marker is missing`
  return null
}

export const validateEnvironment = environment => {
  if (environment.GO_ADMIN_REQUIRE_WEB_E2E !== '1') return 'GO_ADMIN_REQUIRE_WEB_E2E=1 is required'
  if (!environment.GO_ADMIN_TEST_CHROMIUM_EXECUTABLE || !regularFile(environment.GO_ADMIN_TEST_CHROMIUM_EXECUTABLE)) return 'a regular Chromium executable is required'
  if (!environment.GO_ADMIN_TEST_POSTGRES_DISPOSABLE_DSN || !/^postgres(?:ql)?:\/\//.test(environment.GO_ADMIN_TEST_POSTGRES_DISPOSABLE_DSN)) return 'a disposable PostgreSQL URL is required'
  return null
}

export const main = (environment = process.env, run = spawnSync) => {
  const invalid = validateEnvironment(environment)
  if (invalid) return fail(invalid)

  let executed = 0
  for (const suite of suites) {
    const result = run(process.execPath, [resolve(repositoryRoot, suite.path)], {
      cwd: repositoryRoot,
      env: { ...environment, [suite.require]: '1' },
      encoding: 'utf8',
      timeout: 25 * 60_000,
      maxBuffer: 16 * 1024 * 1024,
      windowsHide: true,
    })
    const failure = verifySuiteResult(suite, result)
    if (failure) return fail(failure)
    executed += 1
    console.log(`REQUIRED_WEB_E2E_SUITE_PASS name=${suite.name}`)
  }
  if (executed !== suites.length) return fail(`executed=${executed} expected=${suites.length}`)
  console.log(`REQUIRED_WEB_E2E_PASS suites=${executed} profiles=${executed * 2} skipped=0`)
  return 0
}

const isMain = process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)
if (isMain) process.exitCode = main()
