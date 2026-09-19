import { describe, expect, it } from 'vitest'
import {
  AdministrationRequestError,
  canCancelAccountDeletion,
  createCapabilityController,
  validStartAccountDeletionRequest,
  type Manifest,
} from './administration-controller'

const manifest: Manifest = { dataScope: 'self', permissionCodes: ['iam.users.read'], menus: [{ key: 'iam-users', label: 'Users', path: '/iam/users', permissionCode: 'iam.users.read', sortOrder: 10 }] }

describe('capability controller', () => {
  it('projects only server-issued permission codes and routes', async () => {
    const controller = createCapabilityController({ manifest: async () => manifest })
    await controller.refresh()
    expect(controller.can('iam.users.read')).toBe(true)
    expect(controller.can('iam.users.write')).toBe(false)
    expect(controller.route('/iam/users')).toBe('allowed')
    expect(controller.route('/iam/roles')).toBe('unauthorized')
    expect(controller.route('/unknown')).toBe('not-found')
  })

  it('discards stale manifest results and fails closed', async () => {
    let resolveFirst!: (value: Manifest) => void
    let call = 0
    const controller = createCapabilityController({ manifest: () => ++call === 1 ? new Promise((resolve) => { resolveFirst = resolve }) : Promise.reject(new AdministrationRequestError('authorization')) })
    const first = controller.refresh(); await controller.refresh(); resolveFirst(manifest); await first
    expect(controller.state()).toEqual({ status: 'unauthorized', manifest: null, error: 'authorization' })
    expect(controller.can('iam.users.read')).toBe(false)
  })
})

describe('administration mutation contracts', () => {

  it('enforces transfer, purge confirmation, and the claim cancellation boundary', () => {
    expect(validStartAccountDeletionRequest('account-1', { strategy: 'transfer', transferTargetId: 'account-2', purgeConfirmed: false })).toBe(true)
    expect(validStartAccountDeletionRequest('account-1', { strategy: 'transfer', transferTargetId: 'account-1', purgeConfirmed: false })).toBe(false)
    expect(validStartAccountDeletionRequest('account-1', { strategy: 'purge', purgeConfirmed: false })).toBe(false)
    expect(validStartAccountDeletionRequest('account-1', { strategy: 'purge', purgeConfirmed: true })).toBe(true)
    expect(canCancelAccountDeletion({ id: 'deletion-1', accountId: 'account-1', strategy: 'purge', status: 'queued', auditReference: 'audit-1', createdAt: '2026-09-01T00:00:00Z', updatedAt: '2026-09-01T00:00:00Z' })).toBe(true)
    expect(canCancelAccountDeletion({ id: 'deletion-1', accountId: 'account-1', strategy: 'purge', status: 'claimed', auditReference: 'audit-1', createdAt: '2026-09-01T00:00:00Z', updatedAt: '2026-09-01T00:00:01Z' })).toBe(false)
  })
})
