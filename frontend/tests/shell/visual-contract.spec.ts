import { readFile } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'

import { describe, expect, it } from 'vitest'

const workspaceRoot = fileURLToPath(new URL('../..', import.meta.url))
const source = (path: string) => readFile(`${workspaceRoot}/${path}`, 'utf8')

describe('admin visual contract', () => {
  it('keeps the modern responsive shell composition and dimensions', async () => {
    const [shell, shellStyles] = await Promise.all([
      source('packages/app-shell/src/product/ProductWorkspace.vue'),
      source('packages/app-shell/src/product/ProductWorkspace.scss')
    ])
    const visualShell = `${shell}\n${shellStyles}`

    for (const region of [
      'product-shell__sidebar',
      'product-shell__header',
      'product-shell__breadcrumb',
      'product-shell__tags',
      'product-shell__content'
    ]) expect(visualShell).toContain(region)

    expect(shell).toContain('ProductWorkspace.scss')
    expect(shell).toContain('navigationIcon(item)')
    expect(shell).toContain('ShieldCheckIcon')
    expect(shellStyles).toMatch(/grid-template-columns:\s*244px/)
    expect(shellStyles).toMatch(/product-shell__header\s*\{[^}]*height:\s*54px/s)
    expect(shellStyles).toMatch(/product-shell__navigation button\s*\{[^}]*min-height:\s*42px/s)
    expect(shellStyles).toMatch(/product-shell__tags\s*\{[^}]*height:\s*36px/s)
    expect(shellStyles).toMatch(/grid-template-rows:\s*54px 36px minmax\(0, 1fr\)/)
    expect(shellStyles).toMatch(/grid-template-columns:\s*76px/)
    expect(shellStyles).toMatch(/product-shell__content\s*\{[^}]*min-height:\s*0/s)
    expect(shellStyles).toContain('@media (max-width: 760px)')
  })

  it('shares the tokenized Element Plus design system across both hosts', async () => {
    const [theme, tokens, components, web, desktop, uiManifest, workspaceManifest] = await Promise.all([
      source('packages/ui/src/theme.scss'),
      source('packages/ui/src/styles/_tokens.scss'),
      source('packages/ui/src/components/runtime.mts'),
      source('apps/admin-web/src/main.ts'),
      source('apps/admin-desktop/src/main.ts'),
      source('packages/ui/package.json'),
      source('package.json')
    ])

    for (const token of ['--ga-brand', '--ga-sidebar-bg', '--ga-bg-container', '--ga-border-light', '--el-color-primary']) {
      expect(`${theme}\n${tokens}`).toContain(token)
    }
    for (const mode of [':root', '[data-theme="dark"]', '[data-density="compact"]', 'prefers-reduced-motion']) {
      expect(theme).toContain(mode)
    }
    for (const legacySelector of ['.product-shell__content button', '.product-shell__content :is(input', '.product-shell__content :is(table)']) {
      expect(theme).not.toContain(legacySelector)
    }
    for (const component of ['AppPage', 'QueryBar', 'TableToolbar', 'DataTable', 'FormDialog', 'StatusTag', 'Pagination', 'EmptyState', 'FormGrid']) {
      expect(components).toContain(component)
    }
    expect(web).toContain("@go-admin-plus/ui/admin-theme.css")
    expect(desktop).toContain("@go-admin-plus/ui/admin-theme.css")
    expect(uiManifest).toContain('"./admin-theme.css"')
    expect(uiManifest).toContain('"./components"')
    for (const dependency of ['element-plus', '@lucide/vue', 'sass']) {
      expect(`${uiManifest}\n${workspaceManifest}`).toContain(`"${dependency}"`)
    }
  })

  it('does not let domain-scoped styles override the shared compact page surface', async () => {
    const pages = await Promise.all([
      source('packages/web-domains/iam/src/administration/AdministrationPage.vue'),
      source('packages/web-domains/audit/src/AuditPage.vue'),
      source('packages/web-domains/scheduler/src/SchedulerPage.vue'),
      source('packages/web-domains/files/src/FilesPage.vue'),
      source('packages/web-domains/iam/src/session/AccountPage.vue')
    ])

    for (const page of pages) {
      expect(page).not.toMatch(/\.(?:administration-page|audit-page|organization-page|generator-wizard|scheduler-page|files-page|account-page)\s*\{[^}]*(?:max-width|padding):/s)
      expect(page).not.toMatch(/(?:input|select|button)[^{]*\{[^}]*min-height:\s*40px/s)
      expect(page).not.toMatch(/table\s*\{[^}]*border-collapse:\s*collapse/s)
    }
  })

  it('keeps the retained administration surface localized and on shared tokens', async () => {
    const [manifest, administration, ...domainPages] = await Promise.all([
      source('packages/app-shell/src/product/manifest.ts'),
      source('packages/web-domains/iam/src/administration/AdministrationPage.vue'),
      source('packages/web-domains/audit/src/AuditPage.vue'),
      source('packages/web-domains/files/src/FilesPage.vue'),
      source('packages/web-domains/scheduler/src/SchedulerPage.vue')
    ])

    for (const label of ['用户管理', '角色管理', '菜单管理', '任务调度', '文件管理']) {
      expect(manifest).toContain(`title: '${label}'`)
    }
    for (const text of ['用户与权限', '新增用户', '角色管理', '菜单管理', '当前账号没有可用的管理视图']) {
      expect(administration).toContain(text)
    }
    expect(administration).not.toMatch(/>\s*(Identity and access|Users|Roles|Menus|Create user|Create role|Create menu)\s*</)

    const obsoleteColors = ['#17202a', '#176b54', '#d7dce2', '#dfe6e9', '#aab2bc', '#4c5968', '#4a5561']
    for (const page of [administration, ...domainPages]) {
      for (const color of obsoleteColors) expect(page).not.toContain(color)
    }
  })

  it('uses a restrained product-preview login composition without restoring removed features', async () => {
    const login = await source('packages/web-domains/iam/src/session/LoginPage.vue')

    expect(login).toContain('login-page__visual')
    expect(login).toContain('login-page__preview')
    expect(login).toContain('login-page__preview-table')
    expect(login).toContain('ShieldCheckIcon')
    expect(login).toContain('UserRoundIcon')
    expect(login).toContain('LockKeyholeIcon')
    expect(login).not.toContain('GopherMark')
    expect(login).not.toContain('http://localhost:')
    expect(login).toContain("passwordVisible ? 'text' : 'password'")
    expect(login).toContain("passwordVisible ? '隐藏密码' : '显示密码'")
    expect(login).toContain('passwordInput.value?.focus()')
    expect(login).toContain('autofocus required')
    expect(login).toMatch(/login-page__preview\s*\{[^}]*aspect-ratio:\s*16 \/ 9/s)
    expect(login).toMatch(/login-page__field input\s*\{[^}]*min-height:\s*44px/s)
    expect(login).toContain('@media (max-width: 760px)')
    expect(login).not.toMatch(/captcha|验证码|uuid/i)
  })

  it('keeps the retained account fields in the established summary and tab composition', async () => {
    const account = await source('packages/web-domains/iam/src/session/AccountPage.vue')

    expect(account).toContain('account-page__workspace')
    expect(account).toContain('account-page__summary')
    expect(account).toContain('role="tablist"')
    expect(account).toContain("activeTab = ref<'profile' | 'password'>('profile')")
    expect(account).toContain("confirmPassword: ''")
    expect(account).toContain('passwordsMatch(password.newPassword, password.confirmPassword)')
    expect(account).toContain('两次输入的密码不一致。')
    expect(account).toContain("@keydown.right.prevent=\"activateTab('password')\"")
    expect(account).toContain("@keydown.left.prevent=\"activateTab('profile')\"")
    expect(account).toMatch(/grid-template-columns:\s*minmax\(260px, 300px\) minmax\(0, 1fr\)/)
    for (const retained of ['用户名称', '用户昵称', '用户邮箱', '头像元数据', '基本资料', '修改密码']) {
      expect(account).toContain(retained)
    }
    expect(account).not.toMatch(/手机号码|所属部门|所属角色|创建日期/)
  })

  it('keeps management creation and editing in explicit dialogs', async () => {
    const pages = await Promise.all([
      source('packages/web-domains/iam/src/administration/AdministrationPage.vue'),
      source('packages/web-domains/scheduler/src/SchedulerPage.vue')
    ])

    const openControls = [
      'open-create-user',
      'open-scheduler-definition-form'
    ]
    pages.forEach((page, index) => {
      expect(page).toContain(`data-testid="${openControls[index]}"`)
      {
        expect(page).toContain('management-dialog-backdrop')
        expect(page).toContain('role="dialog"')
      }
      expect(page).not.toMatch(/class="[^"]*\beditor\b[^"]*"/)
    })
  })

  it('localizes destructive confirmations and keeps read details in the shared modal contract', async () => {
    const [shell, audit] = await Promise.all([
      source('packages/app-shell/src/product/ProductWorkspace.vue'),
      source('packages/web-domains/audit/src/AuditPage.vue')
    ])

    for (const message of ['确定删除该记录吗？', '确定删除所选的', '确定清理所选日期之前且符合保留策略的审计日志吗？', '确定删除该']) {
      expect(shell).toContain(message)
    }
    expect(shell).not.toMatch(/Delete (?:this|\$\{count\})/)
    expect(audit).toContain('data-testid="audit-detail-dialog"')
    expect(audit).toContain('role="dialog"')
    expect(audit).toContain('detailDialog.value?.focus()')
    expect(audit).toContain('detailTrigger.value?.focus()')
    expect(audit).not.toContain('<dialog')
  })

  it('exposes complete navigation for every retained paginated projection', async () => {
    const [audit, administration, scheduler, files] = await Promise.all([
      source('packages/web-domains/audit/src/AuditPage.vue'),
      source('packages/web-domains/iam/src/administration/AdministrationPage.vue'),
      source('packages/web-domains/scheduler/src/SchedulerPage.vue'),
      source('packages/web-domains/files/src/FilesPage.vue')
    ])

    for (const [page, testIds, lists] of [
      [audit, ['audit-pagination'], ['list']],
      [administration, ['iam-users-pagination'], ['users']],
      [scheduler, ['scheduler-definitions-pagination', 'scheduler-executions-pagination'], ['definitions', 'executions']]
    ] as const) {
      for (const testId of testIds) expect(page).toContain(`data-testid="${testId}"`)
      for (const list of lists) {
        expect(page).toContain(`controller.${list}.setPage(`)
        expect(page).toContain(`controller.${list}.setPageSize(`)
      }
    }
    for (const page of [files]) {
      expect(page).toContain('controller.list.setSort(')
      expect(page).toContain(':aria-sort="sortDirection(')
      expect(page).toContain("? 'descending' : 'ascending'")
    }
    expect(files).toContain('formatDate(row.createdAt)')
    expect(files).not.toContain('{{ row.updatedAt }}')
    expect(scheduler).toContain('taskTypeLabel(item.taskType)')
    expect(scheduler).toContain('formatDate(item.scheduledFor)')
  })

  it('keeps routed browser and desktop drivers synchronized with localized UI labels', async () => {
    const [iam, scheduler, audit, desktop] = await Promise.all([
      source('tests/e2e/iam/administration/browser-driver.ts'),
      source('tests/e2e/scheduler/browser-driver.ts'),
      source('tests/e2e/audit/browser-driver.ts'),
      source('tests/e2e/desktop/run.mjs')
    ])

    expect(iam).toContain("await router.push('/iam/users')")
    expect(scheduler).toContain("path: '/scheduler/executions'")
    expect(audit).toContain("includes('会话已失效，请重新登录')")
    for (const label of ['账号', '密码', '登录', '文件管理', '退出登录']) expect(desktop).toContain(`'${label}'`)

    const drivers = [iam, scheduler, audit, desktop].join('\n')
    for (const staleLabel of ["'Users'", "'Departments'", "'Executions'", "'Sign in again'", "'Sign out'"]) {
      expect(drivers).not.toContain(staleLabel)
    }
  })
})
