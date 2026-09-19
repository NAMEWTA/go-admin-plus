import { AdministrationRequestError, createContractClient, type AdministrationClient } from '@go-admin-plus/domain-iam/administration'
import { createSessionAwareFetch } from '@go-admin-plus/ui'

interface Problem { category?: string; code?: string }

export const createWebAdministrationClient = (fetcher: typeof fetch = fetch, baseUrl = '/api'): AdministrationClient => {
  const sessionFetch = createSessionAwareFetch(fetcher)
  let requestTail: Promise<void> = Promise.resolve()
  const serialized = <T>(operation: () => Promise<T>): Promise<T> => {
    const result = requestTail.then(operation, operation)
    requestTail = result.then(() => undefined, () => undefined)
    return result
  }
  // 表单携带其读取版本；无正文的分配/删除命令使用最近一次投影版本。
  const revisions = new Map<string, number>()
  const remember = (value: unknown): void => {
    if (Array.isArray(value)) { value.forEach(remember); return }
    if (!value || typeof value !== 'object') return
    const record = value as Record<string, unknown>
    if (typeof record.id === 'string' && Number.isSafeInteger(record.revision)) {
      revisions.set(record.id, Number(record.revision))
    }
    if (Array.isArray(record.rows)) record.rows.forEach(remember)
  }
  const versionedFetch: typeof fetch = async (input, init) => {
    const request = new Request(input, init)
    const path = new URL(request.url).pathname
    const match = path.match(/\/iam\/administration\/(?:users|roles|menus)\/([^/]+)/)
    if (match && request.method !== 'GET' && request.method !== 'HEAD') {
      let version = revisions.get(decodeURIComponent(match[1]!))
      const body = await request.clone().json().catch(() => null) as { revision?: number } | null
      if (body?.revision) version = body.revision
      if (version) request.headers.set('If-Match', String(version))
    }
    const response = await sessionFetch(request)
    if (response.ok) remember(await response.clone().json().catch(() => null))
    return response
  }
  const contract = createContractClient({
    baseUrl,
    fetch: versionedFetch,
  })
  const unwrap = <T>(data: T | undefined, error: unknown): T => {
    if (error !== undefined) throw new AdministrationRequestError(problemCategory(error))
    if (data === undefined) throw new AdministrationRequestError('unavailable')
    return data
  }
  const complete = (error: unknown) => { if (error !== undefined) throw new AdministrationRequestError(problemCategory(error)) }
  return {
    manifest: () => serialized(async () => { const result = await contract.GET('/iam/administration/manifest'); return unwrap(result.data, result.error) }),
    listUsers: (search, page, pageSize) => serialized(async () => { const result = await contract.GET('/iam/administration/users', { params: { query: { search, page, pageSize } } }); return unwrap(result.data, result.error) }),
    createUser: (body) => serialized(async () => { const result = await contract.POST('/iam/administration/users', { body }); return unwrap(result.data, result.error) }),
    updateUser: (userId, body) => serialized(async () => { const result = await contract.PATCH('/iam/administration/users/{userId}', { params: { path: { userId }, header: { 'If-Match': String(revisions.get(userId) ?? 0) } }, body }); return unwrap(result.data, result.error) }),
    startUserDeletion: (userId, body) => serialized(async () => { const result = await contract.POST('/iam/administration/users/{userId}/deletion', { params: { path: { userId }, header: { 'If-Match': String(revisions.get(userId) ?? 0) } }, body }); return unwrap(result.data, result.error) }),
    getUserDeletion: (userId) => serialized(async () => { const result = await contract.GET('/iam/administration/users/{userId}/deletion', { params: { path: { userId } } }); return unwrap(result.data, result.error) }),
    cancelUserDeletion: (userId) => serialized(async () => { const result = await contract.POST('/iam/administration/users/{userId}/deletion/cancel', { params: { path: { userId }, header: { 'If-Match': String(revisions.get(userId) ?? 0) } } }); complete(result.error) }),
    setUserRoles: (userId, roleIds) => serialized(async () => { const result = await contract.PUT('/iam/administration/users/{userId}/roles', { params: { path: { userId }, header: { 'If-Match': String(revisions.get(userId) ?? 0) } }, body: { roleIds: [...roleIds] } }); complete(result.error) }),
    resetPassword: (userId, password) => serialized(async () => { const result = await contract.PUT('/iam/administration/users/{userId}/password', { params: { path: { userId }, header: { 'If-Match': String(revisions.get(userId) ?? 0) } }, body: { password } }); complete(result.error) }),
    listRoles: () => serialized(async () => { const result = await contract.GET('/iam/administration/roles'); return unwrap(result.data, result.error) }),
    createRole: (body) => serialized(async () => { const result = await contract.POST('/iam/administration/roles', { body }); return unwrap(result.data, result.error) }),
    updateRole: (roleId, body) => serialized(async () => { const result = await contract.PATCH('/iam/administration/roles/{roleId}', { params: { path: { roleId }, header: { 'If-Match': String(revisions.get(roleId) ?? 0) } }, body }); complete(result.error) }),
    deleteRole: (roleId) => serialized(async () => { const result = await contract.DELETE('/iam/administration/roles/{roleId}', { params: { path: { roleId }, header: { 'If-Match': String(revisions.get(roleId) ?? 0) } } }); complete(result.error) }),
    setRoleGrants: (roleId, permissionCodes, menuIds) => serialized(async () => { const result = await contract.PUT('/iam/administration/roles/{roleId}/grants', { params: { path: { roleId }, header: { 'If-Match': String(revisions.get(roleId) ?? 0) } }, body: { permissionCodes: [...permissionCodes], menuIds: [...menuIds] } }); complete(result.error) }),
    listMenus: () => serialized(async () => { const result = await contract.GET('/iam/administration/menus'); return unwrap(result.data, result.error) }),
    createMenu: (body) => serialized(async () => { const result = await contract.POST('/iam/administration/menus', { body }); return unwrap(result.data, result.error) }),
    updateMenu: (menuId, body) => serialized(async () => { const result = await contract.PATCH('/iam/administration/menus/{menuId}', { params: { path: { menuId }, header: { 'If-Match': String(revisions.get(menuId) ?? 0) } }, body }); complete(result.error) }),
    deleteMenu: (menuId) => serialized(async () => { const result = await contract.DELETE('/iam/administration/menus/{menuId}', { params: { path: { menuId }, header: { 'If-Match': String(revisions.get(menuId) ?? 0) } } }); complete(result.error) }),
    listPermissions: () => serialized(async () => { const result = await contract.GET('/iam/administration/permissions'); return unwrap(result.data, result.error) }),
  }
}

const problemCategory = (value: unknown) => {
  if (typeof value !== 'object' || value === null) return 'unavailable'
  const problem = value as Problem
  if (problem.code === 'CSRF_REJECTED' || problem.category === 'authentication') return 'relogin'
  if (problem.category === 'authorization') return 'forbidden'
  if (problem.category === 'not_found') return 'not-found'
  if (problem.category === 'validation' || problem.category === 'conflict') return problem.category
  return 'unavailable'
}
