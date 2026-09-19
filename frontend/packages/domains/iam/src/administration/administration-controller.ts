import type { components } from './generated/schema'

export type User = components['schemas']['User']
export type UserPage = components['schemas']['UserPage']
export type Role = components['schemas']['Role']
export type Menu = components['schemas']['Menu']
export type MenuInput = components['schemas']['MenuInput']
export type Permission = components['schemas']['Permission']
export type Manifest = components['schemas']['CapabilityManifest']
export type AccountDeletion = components['schemas']['AccountDeletion']
export type StartAccountDeletionRequest = components['schemas']['StartAccountDeletionRequest']

export const validStartAccountDeletionRequest = (
  accountId: string,
  input: StartAccountDeletionRequest,
): boolean => {
  if (accountId.trim().length === 0) return false
  if (input.strategy === 'transfer') {
    return input.purgeConfirmed === false
      && input.transferTargetId !== undefined
      && input.transferTargetId.trim().length > 0
      && input.transferTargetId !== accountId
  }
  return input.purgeConfirmed === true && input.transferTargetId === undefined
}

export const canCancelAccountDeletion = (deletion: AccountDeletion): boolean => deletion.status === 'queued'

export interface AdministrationClient {
  manifest(): Promise<Manifest>
  listUsers(search: string, page: number, pageSize: number): Promise<UserPage>
  createUser(input: components['schemas']['CreateUserRequest']): Promise<User>
  updateUser(id: string, input: components['schemas']['UpdateUserRequest']): Promise<User>
  startUserDeletion(id: string, input: StartAccountDeletionRequest): Promise<AccountDeletion>
  getUserDeletion(id: string): Promise<AccountDeletion>
  cancelUserDeletion(id: string): Promise<void>
  setUserRoles(id: string, roleIds: ReadonlyArray<string>): Promise<void>
  resetPassword(id: string, password: string): Promise<void>
  listRoles(): Promise<ReadonlyArray<Role>>
  createRole(input: components['schemas']['CreateRoleRequest']): Promise<Role>
  updateRole(id: string, input: components['schemas']['UpdateRoleRequest']): Promise<void>
  deleteRole(id: string): Promise<void>
  setRoleGrants(id: string, permissionCodes: ReadonlyArray<string>, menuIds: ReadonlyArray<string>): Promise<void>
  listMenus(): Promise<ReadonlyArray<Menu>>
  createMenu(input: MenuInput): Promise<Menu>
  updateMenu(id: string, input: MenuInput): Promise<void>
  deleteMenu(id: string): Promise<void>
  listPermissions(): Promise<ReadonlyArray<Permission>>
}

export type CapabilityState =
  | { status: 'idle' | 'loading'; manifest: Manifest | null; error: null }
  | { status: 'ready'; manifest: Manifest; error: null }
  | { status: 'unauthorized'; manifest: null; error: 'authorization' }
  | { status: 'error'; manifest: Manifest | null; error: 'unavailable' }

export interface CapabilityController {
  state(): CapabilityState
  refresh(): Promise<void>
  can(permissionCode: string): boolean
  route(path: string): 'allowed' | 'unauthorized' | 'not-found'
}

export const createCapabilityController = (client: Pick<AdministrationClient, 'manifest'>): CapabilityController => {
  let state: CapabilityState = { status: 'idle', manifest: null, error: null }
  let sequence = 0
  return {
    state: () => state,
    async refresh() {
      const request = ++sequence
      state = { status: 'loading', manifest: state.manifest, error: null }
      try {
        const manifest = await client.manifest()
        if (request === sequence) state = { status: 'ready', manifest, error: null }
      } catch (error) {
        if (request !== sequence) return
        state = error instanceof AdministrationRequestError && ['authorization', 'forbidden', 'relogin'].includes(error.category)
          ? { status: 'unauthorized', manifest: null, error: 'authorization' }
          : { status: 'error', manifest: state.manifest, error: 'unavailable' }
      }
    },
    can(permissionCode) { return state.manifest?.permissionCodes.includes(permissionCode) ?? false },
    route(path) {
      if (state.manifest?.menus.some((menu) => menu.path === path)) return 'allowed'
      return path.startsWith('/iam/') ? 'unauthorized' : 'not-found'
    },
  }
}

export class AdministrationRequestError extends Error {
  readonly category: string
  constructor(category: string) { super('IAM administration request failed'); this.name = 'AdministrationRequestError'; this.category = category }
}
