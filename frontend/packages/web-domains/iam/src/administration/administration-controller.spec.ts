import { describe, expect, it, vi } from 'vitest'
import { AdministrationRequestError, type AdministrationClient } from '@go-admin-plus/domain-iam/administration'
import { createAdministrationController, createUserAndClearPassword, resetPasswordAndClear, settleAdministrationPageOperation } from './administration-controller'

const client = (): AdministrationClient => ({
  manifest: vi.fn(async (): ReturnType<AdministrationClient['manifest']> => ({ dataScope: 'all', permissionCodes: ['iam.users.read', 'iam.users.write', 'iam.users.delete', 'iam.users.reset-password', 'iam.roles.read', 'iam.roles.write', 'iam.roles.delete', 'iam.roles.assign', 'iam.menus.read', 'iam.menus.write', 'iam.menus.delete', 'iam.permissions.read', 'iam.manifest.read'], menus: [] })), listUsers: vi.fn(async (search, page, pageSize) => ({ rows: [{ id: 'account-00000001', username: search || 'admin', displayName: 'Admin', email: 'admin@example.test', disabled: false, roleIds: [] }], total: page * pageSize })),
  createUser: vi.fn(async (value) => ({ id: 'account-00000002', disabled: false, roleIds: [], ...value })), updateUser: vi.fn(),
  startUserDeletion: vi.fn(async (id, value) => ({ id: '11111111-1111-1111-1111-111111111111', accountId: id, strategy: value.strategy, status: 'queued' as const, auditReference: 'audit-reference', createdAt: '2026-09-01T00:00:00Z', updatedAt: '2026-09-01T00:00:00Z' })),
  getUserDeletion: vi.fn(), cancelUserDeletion: vi.fn(), setUserRoles: vi.fn(), resetPassword: vi.fn(),
  listRoles: vi.fn(async () => []), createRole: vi.fn(async (value) => ({ id: 'role-000000000001', enabled: true, protected: false, permissionCodes: [], menuIds: [], ...value })), updateRole: vi.fn(), deleteRole: vi.fn(), setRoleGrants: vi.fn(),
  listMenus: vi.fn(async () => []), createMenu: vi.fn(async (value) => ({ id: 'menu-000000000001', protected: false, ...value })), updateMenu: vi.fn(), deleteMenu: vi.fn(), listPermissions: vi.fn(async () => []),
})

describe('administration controller', () => {
  it('reuses deterministic search, reset, pagination and selection', async () => {
    const api = client(); const controller = createAdministrationController(api, async () => true)
    await controller.users.search({ search: 'reader' }); await controller.users.setPage(2)
    controller.users.select(controller.users.snapshot().rows)
    expect(controller.users.snapshot().selectedKeys).toEqual(['account-00000001'])
    await controller.users.reset(); expect(controller.users.snapshot().filters).toEqual({ search: '' }); expect(controller.users.snapshot().page).toBe(1)
  })

  it('validates before writes and prevents repeated submissions', async () => {
    const api = client(); let release!: () => void
    vi.mocked(api.createUser).mockImplementation(() => new Promise((resolve) => { release = () => resolve({ id: 'account-00000002', username: 'reader', displayName: 'Reader', email: 'reader@example.test', disabled: false, roleIds: [] }) }))
    const controller = createAdministrationController(api, async () => true)
    expect(await controller.createUser.run({ username: 'x', displayName: '', email: 'bad', password: 'short' })).toBe('invalid')
    const first = controller.createUser.run({ username: 'reader', displayName: 'Reader', email: 'reader@example.test', password: 'reader password' })
    expect(await controller.createUser.run({ username: 'reader', displayName: 'Reader', email: 'reader@example.test', password: 'reader password' })).toBe('busy')
    release(); expect(await first).toBe('submitted'); expect(api.createUser).toHaveBeenCalledTimes(1)
    expect(await controller.createRole.run({ key: 'Bad Key', name: 'Invalid role', dataScope: 'all' })).toBe('invalid')
    expect(await controller.createMenu.run({ key: '-invalid', label: 'Invalid menu', path: '/iam/invalid', permissionCode: 'iam.users.read', sortOrder: 1 })).toBe('invalid')
    expect(await controller.updateRole({ id: 'role-000000000001', key: 'bad key', name: 'Invalid', dataScope: 'all', enabled: true, protected: false, permissionCodes: [], menuIds: [] })).toBe('invalid')
    expect(api.createRole).not.toHaveBeenCalled(); expect(api.createMenu).not.toHaveBeenCalled(); expect(api.updateRole).not.toHaveBeenCalled()
  })

  it('guards direct commands and clears replacement passwords on every result', async () => {
    const api = client(); let release!: () => void
    vi.mocked(api.updateUser).mockImplementation(() => new Promise((resolve) => { release = () => resolve({ id: 'account-00000001', username: 'admin', displayName: 'Admin', email: 'admin@example.test', disabled: false, roleIds: [] }) }))
    const controller = createAdministrationController(api, async () => true)
    const user = { id: 'account-00000001', username: 'admin', displayName: 'Admin', email: 'admin@example.test', disabled: false, roleIds: [] }
    const first = controller.updateUser(user, true)
    expect(await controller.updateUser(user, true)).toBe('busy')
    release(); expect(await first).toBe('completed')
    vi.mocked(api.resetPassword).mockRejectedValueOnce(new Error('unavailable'))
    let password = 'replacement password'
    expect(await resetPasswordAndClear(controller, user.id, password, () => { password = '' })).toBe('failed')
    expect(password).toBe('')
  })

  it('clears create passwords and preserves stable failures across shared mutation controllers', async () => {
    const api = client(); const controller = createAdministrationController(api, async () => true)
    vi.mocked(api.createUser).mockRejectedValueOnce(new AdministrationRequestError('relogin'))
    let password = 'reader password'
    expect(await createUserAndClearPassword(controller, { username: 'reader', displayName: 'Reader', email: 'reader@example.test', password }, () => { password = '' })).toBe('failed')
    expect(password).toBe('')
    expect(controller.failure()).toBe('relogin')

    vi.mocked(api.updateUser).mockRejectedValueOnce(new AdministrationRequestError('unavailable'))
    const user = { id: 'account-00000001', username: 'admin', displayName: 'Admin', email: 'admin@example.test', disabled: false, roleIds: [] }
    expect(await controller.updateUser(user, false)).toBe('failed')
    expect(controller.failure()).toBe('unavailable')
  })

  it('confirms destructive status changes and exposes page-owned deletion and secret cleanup controls', async () => {
    const api = client(); const confirm = vi.fn(async () => true); const controller = createAdministrationController(api, confirm)
    const user = { id: 'account-00000001', username: 'admin', displayName: 'Admin', email: 'admin@example.test', disabled: false, roleIds: [] }
    expect(await controller.updateUser(user, false)).toBe('completed')
    expect(await controller.updateRole({ id: 'role-000000000001', key: 'reader', name: 'Reader', dataScope: 'self', enabled: false, protected: false, permissionCodes: [], menuIds: [] })).toBe('completed')
    expect(confirm).toHaveBeenCalledTimes(2)
  })

  it('tracks deletion refreshes and refuses cancellation after a worker claim', async () => {
    const api = client(); const confirm = vi.fn(async () => true); const controller = createAdministrationController(api, confirm)
    await controller.refreshAuthorizationData()
    expect(await controller.startUserDeletion('account-00000001', { strategy: 'purge', purgeConfirmed: false })).toBe('invalid')
    expect(await controller.startUserDeletion('account-00000001', { strategy: 'transfer', transferTargetId: 'account-00000002', purgeConfirmed: false })).toBe('completed')
    expect(controller.deletion()?.status).toBe('queued')
    expect(confirm).toHaveBeenCalledTimes(1)

    vi.mocked(api.getUserDeletion).mockResolvedValueOnce({ ...controller.deletion()!, status: 'claimed', updatedAt: '2026-09-01T00:00:01Z' })
    expect(await controller.refreshUserDeletion('account-00000001')).toBe('completed')
    expect(controller.deletion()?.status).toBe('claimed')
    expect(await controller.cancelUserDeletion('account-00000001')).toBe('invalid')
    expect(api.cancelUserDeletion).not.toHaveBeenCalled()

    vi.mocked(api.getUserDeletion).mockRejectedValueOnce(new AdministrationRequestError('forbidden'))
    expect(await controller.refreshUserDeletion('account-00000001')).toBe('failed')
    expect(controller.deletion()).toBe(null)

    controller.clearDeletion()
    expect(controller.deletion()).toBe(null)
  })

  it('cancels only a queued deletion and refreshes the restored user projection', async () => {
    const api = client(); const controller = createAdministrationController(api, async () => true)
    await controller.refreshAuthorizationData()
    expect(await controller.startUserDeletion('account-00000001', { strategy: 'purge', purgeConfirmed: true })).toBe('completed')
    vi.mocked(api.listUsers).mockClear()
    expect(await controller.cancelUserDeletion('account-00000001')).toBe('completed')
    expect(api.cancelUserDeletion).toHaveBeenCalledWith('account-00000001')
    expect(api.listUsers).toHaveBeenCalledTimes(1)
    expect(controller.deletion()).toBe(null)
  })

  it('serializes deletion refresh with mutation commands', async () => {
    const api = client(); const controller = createAdministrationController(api, async () => true)
    await controller.refreshAuthorizationData()
    let release!: () => void
    vi.mocked(api.getUserDeletion).mockImplementationOnce(() => new Promise((resolve) => {
      release = () => resolve({ id: '11111111-1111-1111-1111-111111111111', accountId: 'account-00000001', strategy: 'purge', status: 'queued', auditReference: 'audit-reference', createdAt: '2026-09-01T00:00:00Z', updatedAt: '2026-09-01T00:00:00Z' })
    }))
    const refreshing = controller.refreshUserDeletion('account-00000001')
    expect(controller.busy).toBe(true)
    release()
    expect(await refreshing).toBe('completed')
  })

  it('loads only administration projections granted by the manifest', async () => {
    const api = client()
    vi.mocked(api.manifest).mockResolvedValueOnce({ dataScope: 'self', permissionCodes: ['iam.users.read', 'iam.users.write', 'iam.roles.read', 'iam.menus.read', 'iam.permissions.read', 'iam.roles.assign', 'iam.manifest.read'], menus: [] })
    const controller = createAdministrationController(api, async () => true)
    await controller.refreshAuthorizationData()
    expect(controller.can('iam.users.read')).toBe(true)
    expect(controller.can('iam.users.write')).toBe(false)
    expect(controller.can('iam.roles.read')).toBe(false)
    expect(api.listRoles).not.toHaveBeenCalled()
    expect(api.listMenus).not.toHaveBeenCalled()
    expect(api.listPermissions).not.toHaveBeenCalled()
  })

  it('exposes global actions when an account-level all scope accompanies the permission', async () => {
    const api = client()
    vi.mocked(api.manifest).mockResolvedValueOnce({ dataScope: 'all', permissionCodes: ['iam.users.read', 'iam.users.write', 'iam.manifest.read'], menus: [] })
    const controller = createAdministrationController(api, async () => true)
    await controller.refreshAuthorizationData()
    expect(controller.can('iam.users.write')).toBe(true)
  })

  it('records initial manifest failures and clears stale capabilities fail closed', async () => {
    const api = client(); const controller = createAdministrationController(api, async () => true)
    await controller.refreshAuthorizationData()
    expect(controller.can('iam.roles.read')).toBe(true)
    vi.mocked(api.manifest).mockRejectedValueOnce(new AdministrationRequestError('relogin'))
    await expect(controller.refreshAuthorizationData()).rejects.toBeInstanceOf(AdministrationRequestError)
    expect(controller.failure()).toBe('relogin')
    expect(controller.can('iam.users.read')).toBe(false)
    expect(controller.can('iam.roles.read')).toBe(false)
  })

  it('settles initial page failures after surfacing the stable relogin state', async () => {
    const api = client(); const controller = createAdministrationController(api, async () => true)
    vi.mocked(api.manifest).mockRejectedValueOnce(new AdministrationRequestError('relogin'))
    const settled = vi.fn()
    await expect(settleAdministrationPageOperation(() => controller.refreshAuthorizationData(), settled)).resolves.toBeUndefined()
    expect(controller.failure()).toBe('relogin')
    expect(controller.can('iam.users.read')).toBe(false)
    expect(settled).toHaveBeenCalledTimes(1)
  })

  it.each([
    ['relogin', new AdministrationRequestError('relogin')],
    ['unavailable', new Error('list unavailable')],
  ] as const)('surfaces an initial user list %s failure and clears capabilities fail closed', async (expected, error) => {
    const api = client(); const controller = createAdministrationController(api, async () => true)
    await controller.refreshAuthorizationData()
    vi.mocked(api.listUsers).mockRejectedValueOnce(error)
    const sessionRequired = vi.fn()
    await settleAdministrationPageOperation(() => controller.users.refresh(), () => {
      if (controller.failure() === 'relogin') sessionRequired()
    })
    expect(controller.failure()).toBe(expected)
    expect(controller.users.snapshot().rows).toEqual([])
    expect(controller.can('iam.users.read')).toBe(false)
    expect(controller.can('iam.roles.read')).toBe(false)
    expect(sessionRequired).toHaveBeenCalledTimes(expected === 'relogin' ? 1 : 0)
  })

  it.each([
    ['forbidden', 'search', new AdministrationRequestError('forbidden')],
    ['relogin', 'page', new AdministrationRequestError('relogin')],
    ['unavailable', 'reset', new Error('list unavailable')],
  ] as const)('hides a successful user projection after a current %s failure and recovers after refresh', async (expected, operation, error) => {
    const api = client(); const controller = createAdministrationController(api, async () => true)
    await controller.refreshAuthorizationData(); await controller.users.refresh()
    controller.users.select(controller.users.snapshot().rows)
    expect(controller.users.snapshot().rows).toHaveLength(1)
    expect(controller.users.snapshot().selectedKeys).toHaveLength(1)

    vi.mocked(api.listUsers).mockRejectedValueOnce(error)
    const request = operation === 'search'
      ? controller.users.search({ search: 'reader' })
      : operation === 'page' ? controller.users.setPage(2) : controller.users.reset()
    await expect(request).rejects.toBe(error)
    expect(controller.failure()).toBe(expected)
    expect(controller.can('iam.users.read')).toBe(false)
    expect(controller.can('iam.users.write')).toBe(false)
    expect(controller.users.snapshot().rows).toEqual([])
    expect(controller.users.snapshot().total).toBe(0)
    expect(controller.users.snapshot().selectedKeys).toEqual([])

    await controller.refreshAuthorizationData(); await controller.users.refresh()
    expect(controller.failure()).toBe(null)
    expect(controller.can('iam.users.read')).toBe(true)
    expect(controller.users.snapshot().rows).toHaveLength(1)
    expect(controller.users.snapshot().selectedKeys).toEqual([])
  })

  it('hides a successful user projection when a refreshed manifest revokes users read', async () => {
    const api = client(); const controller = createAdministrationController(api, async () => true)
    await controller.refreshAuthorizationData(); await controller.users.refresh()
    controller.users.select(controller.users.snapshot().rows)
    vi.mocked(api.manifest).mockResolvedValueOnce({ dataScope: 'all', permissionCodes: ['iam.manifest.read'], menus: [] })

    await controller.refreshAuthorizationData()
    expect(controller.can('iam.users.read')).toBe(false)
    expect(controller.users.snapshot().rows).toEqual([])
    expect(controller.users.snapshot().total).toBe(0)
    expect(controller.users.snapshot().selectedKeys).toEqual([])

    await controller.refreshAuthorizationData(); await controller.users.refresh()
    expect(controller.can('iam.users.read')).toBe(true)
    expect(controller.users.snapshot().rows).toHaveLength(1)
  })

  it('repairs create and command projections without repeating successful writes', async () => {
    const createAPI = client(); const createController = createAdministrationController(createAPI, async () => true)
    vi.mocked(createAPI.listUsers)
      .mockRejectedValueOnce(new Error('refresh unavailable'))
      .mockRejectedValueOnce(new Error('refresh still unavailable'))
    const model = { username: 'reader', displayName: 'Reader', email: 'reader@example.test', password: 'reader password' }
    expect(await createController.createUser.run(model)).toBe('refresh-failed')
    expect(createController.hasPendingRepair()).toBe(true)
    expect(await createController.createUser.run({ ...model, username: 'writer' })).toBe('refresh-failed')
    expect(createAPI.createUser).toHaveBeenCalledTimes(1)
    expect(createController.hasPendingRepair()).toBe(true)
    expect(await createController.repairProjection()).toBe('refresh-failed')
    expect(createAPI.createUser).toHaveBeenCalledTimes(1)
    expect(createController.hasPendingRepair()).toBe(true)
    expect(await createController.repairProjection()).toBe('completed')
    expect(createController.hasPendingRepair()).toBe(false)
    expect(await createController.createUser.run({ ...model, username: 'writer' })).toBe('submitted')
    expect(createAPI.createUser).toHaveBeenCalledTimes(2)

    const commandAPI = client(); const commandConfirm = vi.fn(async () => true); const commandController = createAdministrationController(commandAPI, commandConfirm)
    vi.mocked(commandAPI.listUsers)
      .mockRejectedValueOnce(new Error('refresh unavailable'))
      .mockRejectedValueOnce(new Error('refresh still unavailable'))
    const user = { id: 'account-00000001', username: 'admin', displayName: 'Admin', email: 'admin@example.test', disabled: false, roleIds: [] }
    expect(await commandController.updateUser(user, false)).toBe('refresh-failed')
    expect(commandController.hasPendingRepair()).toBe(true)
    expect(await commandController.updateUser({ ...user, id: 'account-00000002' }, true)).toBe('refresh-failed')
    expect(commandAPI.updateUser).toHaveBeenCalledTimes(1)
    expect(commandConfirm).toHaveBeenCalledTimes(1)
    expect(commandController.hasPendingRepair()).toBe(true)
    expect(await commandController.repairProjection()).toBe('refresh-failed')
    expect(commandAPI.updateUser).toHaveBeenCalledTimes(1)
    expect(commandController.hasPendingRepair()).toBe(true)
    expect(await commandController.repairProjection()).toBe('completed')
    expect(commandController.hasPendingRepair()).toBe(false)
    expect(await commandController.updateUser({ ...user, id: 'account-00000002' }, true)).toBe('completed')
    expect(commandAPI.updateUser).toHaveBeenCalledTimes(2)
    expect(commandConfirm).toHaveBeenCalledTimes(1)
  })

  it('serializes all mutation controller classes behind one busy boundary', async () => {
    const api = client(); const confirm = vi.fn(async () => true); const controller = createAdministrationController(api, confirm)
    let release!: () => void
    vi.mocked(api.createUser).mockImplementation(() => new Promise((resolve) => {
      release = () => resolve({ id: 'account-00000002', username: 'reader', displayName: 'Reader', email: 'reader@example.test', disabled: false, roleIds: [] })
    }))
    const user = { id: 'account-00000001', username: 'admin', displayName: 'Admin', email: 'admin@example.test', disabled: false, roleIds: [] }
    const creating = controller.createUser.run({ username: 'reader', displayName: 'Reader', email: 'reader@example.test', password: 'reader password' })
    expect(await controller.updateUser(user, false)).toBe('busy')
    expect(api.updateUser).not.toHaveBeenCalled()
    expect(confirm).not.toHaveBeenCalled()
    release()
    expect(await creating).toBe('submitted')
    expect(controller.hasPendingRepair()).toBe(false)

    vi.mocked(api.createUser).mockResolvedValueOnce({ id: 'account-00000003', username: 'writer', displayName: 'Writer', email: 'writer@example.test', disabled: false, roleIds: [] })
    vi.mocked(api.listUsers).mockRejectedValueOnce(new Error('refresh unavailable'))
    expect(await controller.createUser.run({ username: 'writer', displayName: 'Writer', email: 'writer@example.test', password: 'writer password' })).toBe('refresh-failed')
    let releaseRepair!: () => void
    vi.mocked(api.listUsers).mockImplementationOnce(() => new Promise((resolve) => {
      releaseRepair = () => resolve({ rows: [], total: 0 })
    }))
    const repairing = controller.repairProjection()
    expect(await controller.updateUser(user, false)).toBe('busy')
    await vi.waitFor(() => expect(releaseRepair).toBeTypeOf('function'))
    releaseRepair()
    expect(await repairing).toBe('completed')
    expect(controller.hasPendingRepair()).toBe(false)
  })
})
