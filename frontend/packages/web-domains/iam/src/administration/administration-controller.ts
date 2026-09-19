import {
  AdministrationRequestError,
  canCancelAccountDeletion,
  validStartAccountDeletionRequest,
  type AccountDeletion,
  type AdministrationClient,
  type Menu,
  type MenuInput,
  type Permission,
  type Role,
  type StartAccountDeletionRequest,
  type User,
} from '@go-admin-plus/domain-iam/administration'
import { createListController, type FormController, type FormRunResult, type ListController } from '@go-admin-plus/ui'

export interface UserFilters { search: string }
export interface CreateUserModel { username: string; displayName: string; email: string; password: string }
export type CreateRoleModel = Parameters<AdministrationClient['createRole']>[0]

export interface AdministrationController {
  users: ListController<UserFilters, User, string>
  createUser: FormController<CreateUserModel>
  roles(): ReadonlyArray<Role>
  menus(): ReadonlyArray<Menu>
  permissions(): ReadonlyArray<Permission>
  can(permissionCode: string): boolean
  failure(): AdministrationFailure | null
  clearFailure(): void
  hasPendingRepair(): boolean
  repairProjection(): Promise<CommandResult>
  refreshAuthorizationData(): Promise<void>
  createRole: FormController<CreateRoleModel>
  createMenu: FormController<MenuInput>
  readonly busy: boolean
  updateUser(user: User, enabled: boolean): Promise<CommandResult>
  deleteRole(id: string): Promise<CommandResult>
  updateRole(role: Role): Promise<CommandResult>
  deleteMenu(id: string): Promise<CommandResult>
  updateMenu(menu: Menu): Promise<CommandResult>
  setUserRoles(id: string, roleIds: ReadonlyArray<string>): Promise<CommandResult>
  setRoleGrants(id: string, permissionCodes: ReadonlyArray<string>, menuIds: ReadonlyArray<string>): Promise<CommandResult>
  resetPassword(id: string, password: string): Promise<CommandResult>
  deletion(): AccountDeletion | null
  deletionLoading(): boolean
  clearDeletion(): void
  startUserDeletion(id: string, input: StartAccountDeletionRequest): Promise<CommandResult>
  refreshUserDeletion(id: string): Promise<CommandResult>
  cancelUserDeletion(id: string): Promise<CommandResult>
}

export type CommandResult = 'completed' | 'cancelled' | 'busy' | 'invalid' | 'failed' | 'refresh-failed'
export type AdministrationFailure = 'relogin' | 'forbidden' | 'validation' | 'not-found' | 'conflict' | 'unavailable'

export const createAdministrationController = (client: AdministrationClient, confirm: (count: number) => Promise<boolean>): AdministrationController => {
  let roles: ReadonlyArray<Role> = []
  let menus: ReadonlyArray<Menu> = []
  let permissions: ReadonlyArray<Permission> = []
  let capabilityCodes = new Set<string>()
  let capabilityScope: Role['dataScope'] = 'self'
  let failure: AdministrationFailure | null = null
  let deletion: AccountDeletion | null = null
  let deletionLoading = false
  let userProjectionVisible = false
  const pendingRepairs = new Map<string, () => Promise<void>>()
  let mutationBusy = false
  let repairBusy = false
  const clearAuthorizationProjection = () => {
    capabilityCodes = new Set(); capabilityScope = 'self'; roles = []; menus = []; permissions = []; userProjectionVisible = false; deletion = null
  }
  const recordFailure = (error: unknown) => {
    failure = error instanceof AdministrationRequestError && isAdministrationFailure(error.category)
      ? error.category
      : 'unavailable'
    if (failure === 'relogin') clearAuthorizationProjection()
    else if (failure === 'forbidden') deletion = null
  }
  const clearFailure = () => { failure = null }
  const requireAccess = (permissionCode: string): boolean => {
    if (canAccess(permissionCode)) return true
    failure = 'forbidden'
    return false
  }
  const rawUsers = createListController<UserFilters, User, string>({
    initialFilters: () => ({ search: '' }),
    rowKey: (row) => row.id,
    load: async ({ filters, page, pageSize }) => client.listUsers(filters.search, page, pageSize),
  })
  const observeUserList = async (operation: () => Promise<void>) => {
    clearFailure()
    try {
      await operation()
      if (!userProjectionVisible) rawUsers.clearSelection()
      userProjectionVisible = true
    } catch (error) {
      recordFailure(error)
      clearAuthorizationProjection()
      throw error
    }
  }
  const users: ListController<UserFilters, User, string> = {
    snapshot: () => {
      const snapshot = rawUsers.snapshot()
      return userProjectionVisible ? snapshot : { ...snapshot, rows: [], total: 0, selectedKeys: [] }
    },
    refresh: () => observeUserList(() => rawUsers.refresh()),
    search: (filters) => observeUserList(() => rawUsers.search(filters)),
    reset: () => observeUserList(() => rawUsers.reset()),
    setPage: (page) => observeUserList(() => rawUsers.setPage(page)),
    setPageSize: (pageSize) => observeUserList(() => rawUsers.setPageSize(pageSize)),
    setSort: (sort) => observeUserList(() => rawUsers.setSort(sort)),
    select: (rows) => rawUsers.select(rows),
    clearSelection: () => rawUsers.clearSelection(),
  }
  const canAccess = (permissionCode: string) => capabilityCodes.has(permissionCode) && (capabilityScope === 'all' || permissionCode === 'iam.users.read')
  const refreshAuthorizationData = async () => {
    clearFailure()
    try {
      const previousUsersRead = canAccess('iam.users.read')
      const previousScope = capabilityScope
      const manifest = await client.manifest()
      capabilityCodes = new Set(manifest.permissionCodes)
      capabilityScope = manifest.dataScope
      if (!canAccess('iam.users.read') || previousUsersRead !== canAccess('iam.users.read') || previousScope !== capabilityScope) {
        userProjectionVisible = false
        rawUsers.clearSelection()
      }
      const [nextRoles, nextMenus, nextPermissions] = await Promise.all([
        canAccess('iam.roles.read') ? client.listRoles() : Promise.resolve([]),
        canAccess('iam.menus.read') ? client.listMenus() : Promise.resolve([]),
        canAccess('iam.permissions.read') ? client.listPermissions() : Promise.resolve([]),
      ])
      roles = [...nextRoles]; menus = [...nextMenus]; permissions = [...nextPermissions]
    } catch (error) {
      clearAuthorizationProjection()
      recordFailure(error)
      throw error
    }
  }
  const refreshUsersProjection = async () => {
    await refreshAuthorizationData()
    if (canAccess('iam.users.read')) await users.refresh()
  }
  const form = <TModel>(key: string, options: {
    validate(model: Readonly<TModel>): Promise<boolean>
    submit(model: Readonly<TModel>): Promise<void>
    refresh(): Promise<void>
  }): FormController<TModel> => {
    const refresh = async (): Promise<FormRunResult> => {
      clearFailure()
      try { await options.refresh(); pendingRepairs.delete(key); return 'submitted' }
      catch (error) { recordFailure(error); return 'refresh-failed' }
    }
    return {
      get busy() { return mutationBusy || repairBusy || deletionLoading },
      async run(model) {
        if (mutationBusy || repairBusy || deletionLoading) return 'busy'
        if (pendingRepairs.size > 0) return 'refresh-failed'
        mutationBusy = true
        try {
          if (!await options.validate(model)) return 'invalid'
          clearFailure()
          try { await options.submit(model) } catch (error) { recordFailure(error); return 'failed' }
          pendingRepairs.set(key, options.refresh)
          return await refresh()
        } finally { mutationBusy = false }
      },
    }
  }
  const createUser = form<CreateUserModel>('create-user', {
    validate: async (model) => validName(model.username, 64) && validName(model.displayName, 80) && model.email.includes('@') && model.password.length >= 10,
    submit: async (model) => { await client.createUser(model) },
    refresh: refreshUsersProjection,
  })
  const createRole = form<CreateRoleModel>('create-role', {
    validate: async (model) => validStableKey(model.key) && validName(model.name, 100),
    submit: async (model) => { await client.createRole(model) },
    refresh: refreshAuthorizationData,
  })
  const createMenu = form<MenuInput>('create-menu', {
    validate: async (model) => validStableKey(model.key) && validName(model.label, 80) && (model.kind === 'directory' || model.kind === 'button' && model.permissionCode.length >= 3 || model.path.startsWith('/')),
    submit: async (model) => { await client.createMenu(model) },
    refresh: refreshAuthorizationData,
  })
  const command = async (key: string, operation: () => Promise<void>, refreshed: () => Promise<void>, destructive = false): Promise<CommandResult> => {
    if (mutationBusy || repairBusy || deletionLoading) return 'busy'
    if (pendingRepairs.size > 0) return 'refresh-failed'
    mutationBusy = true
    clearFailure()
    try {
      if (destructive && !await confirm(1)) return 'cancelled'
      try { await operation() } catch (error) { recordFailure(error); return 'failed' }
      pendingRepairs.set(key, refreshed)
      try { await refreshed(); pendingRepairs.delete(key); return 'completed' } catch (error) { recordFailure(error); return 'refresh-failed' }
    } finally { mutationBusy = false }
  }
  const repairProjection = async (): Promise<CommandResult> => {
    if (repairBusy || mutationBusy || deletionLoading) return 'busy'
    const next = pendingRepairs.entries().next().value as [string, () => Promise<void>] | undefined
    if (!next) return 'completed'
    repairBusy = true
    clearFailure()
    try {
      try { await next[1](); pendingRepairs.delete(next[0]); return 'completed' }
      catch (error) { recordFailure(error); return 'refresh-failed' }
    } finally { repairBusy = false }
  }
  return {
    users, createUser, createRole, createMenu,
    roles: () => [...roles], menus: () => [...menus], permissions: () => [...permissions], refreshAuthorizationData,
    can: canAccess,
    failure: () => failure,
    clearFailure,
    hasPendingRepair: () => pendingRepairs.size > 0,
    repairProjection,
    get busy() { return mutationBusy || repairBusy || deletionLoading },
    updateUser(user, enabled) { return command(`update-user:${user.id}`, async () => { await client.updateUser(user.id, { displayName: user.displayName, email: user.email, enabled,revision:user.revision }) }, refreshUsersProjection, !enabled) },
    deleteRole(id) { return command(`delete-role:${id}`, () => client.deleteRole(id), refreshAuthorizationData, true) },
    updateRole(role) { if (!validStableKey(role.key) || !validName(role.name, 100)) return Promise.resolve('invalid'); return command(`update-role:${role.id}`, () => client.updateRole(role.id, { key: role.key, name: role.name, dataScope: role.dataScope === 'all' ? 'all' : 'self', enabled: role.enabled,revision:role.revision }), refreshAuthorizationData, !role.enabled) },
    deleteMenu(id) { return command(`delete-menu:${id}`, () => client.deleteMenu(id), refreshAuthorizationData, true) },
    updateMenu(menu) { if (!validStableKey(menu.key)) return Promise.resolve('invalid'); return command(`update-menu:${menu.id}`, () => client.updateMenu(menu.id, { key: menu.key, label: menu.label, path: menu.path, permissionCode: menu.permissionCode, sortOrder: menu.sortOrder, parentId:menu.parentId,kind:menu.kind,icon:menu.icon,enabled:menu.enabled,hidden:menu.hidden,revision:menu.revision }), refreshAuthorizationData) },
    setUserRoles(id, roleIds) { return command(`set-user-roles:${id}`, () => client.setUserRoles(id, roleIds), refreshUsersProjection) },
    setRoleGrants(id, permissionCodes, menuIds) { return command(`set-role-grants:${id}`, () => client.setRoleGrants(id, permissionCodes, menuIds), refreshAuthorizationData) },
    resetPassword(id, password) { if (password.length < 10) return Promise.resolve('invalid'); return command(`reset-password:${id}`, () => client.resetPassword(id, password), refreshUsersProjection, true) },
    deletion: () => deletion,
    deletionLoading: () => deletionLoading,
    clearDeletion() { deletion = null },
    startUserDeletion(id, input) {
      if (!validStartAccountDeletionRequest(id, input)) return Promise.resolve('invalid')
      if (!requireAccess('iam.users.delete')) return Promise.resolve('failed')
      return command(`start-user-deletion:${id}`, async () => { deletion = await client.startUserDeletion(id, input) }, refreshUsersProjection, true)
    },
    async refreshUserDeletion(id) {
      if (!requireAccess('iam.users.delete')) return 'failed'
      if (mutationBusy || repairBusy || deletionLoading) return 'busy'
      deletionLoading = true
      clearFailure()
      try {
        try { deletion = await client.getUserDeletion(id); return 'completed' }
        catch (error) { recordFailure(error); return 'failed' }
      } finally { deletionLoading = false }
    },
    cancelUserDeletion(id) {
      if (!deletion || deletion.accountId !== id || !canCancelAccountDeletion(deletion)) return Promise.resolve('invalid')
      if (!requireAccess('iam.users.delete')) return Promise.resolve('failed')
      return command(`cancel-user-deletion:${id}`, async () => { await client.cancelUserDeletion(id); deletion = null }, refreshUsersProjection)
    },
  }
}

export const resetPasswordAndClear = async (controller: AdministrationController, id: string, password: string, clear: () => void) => {
  try { return await controller.resetPassword(id, password) }
  finally { clear() }
}

export const createUserAndClearPassword = async (controller: AdministrationController, model: CreateUserModel, clear: () => void) => {
  try { return await controller.createUser.run(model) }
  finally { clear() }
}

export const settleAdministrationPageOperation = async <T>(operation: () => Promise<T>, settled: () => void): Promise<T | undefined> => {
  try { return await operation() }
  catch { /* The controller owns stable failure classification; page operations must settle. */ }
  finally { settled() }
}

const isAdministrationFailure = (value: string): value is AdministrationFailure =>
  ['relogin', 'forbidden', 'validation', 'not-found', 'conflict', 'unavailable'].includes(value)

const validName = (value: string, maximum: number) => value.trim().length >= 3 && value.length <= maximum
const validStableKey = (value: string) => value.length >= 3 && value.length <= 64 && /^[a-z0-9][a-z0-9_-]*$/.test(value)
