import { createContractClient, type components } from './client'

describe('generated contract client', () => {
  it('roundtrips the declared probe request and response', async() => {
    let request: Request | undefined
    const client = createContractClient({
      baseUrl: 'https://api.example.test',
      fetch: async input => {
        request = input
        return Response.json({ value: 'roundtrip' })
      }
    })

    const result = await client.POST('/contract/probe', {
      params: { query: { page: 2, pageSize: 40 } },
      body: { value: 'roundtrip' }
    })

    expect(result.data).toEqual({ value: 'roundtrip' })
    expect(result.error).toBeUndefined()
    expect(request?.method).toBe('POST')
    expect(new URL(request?.url ?? '').search).toBe('?page=2&pageSize=40')
    await expect(request?.clone().json()).resolves.toEqual({ value: 'roundtrip' })
  })

  it.each([
    ['validation', 400, 'REQUEST_INVALID'],
    ['authentication', 401, 'SESSION_REQUIRED'],
    ['authorization', 403, 'PERMISSION_DENIED'],
    ['not_found', 404, 'RESOURCE_NOT_FOUND'],
    ['conflict', 409, 'RESOURCE_CONFLICT'],
    ['internal', 500, 'INTERNAL_ERROR']
  ] as const)('returns the declared stable %s problem', async(category, status, code) => {
    const problem: components['schemas']['Problem'] = {
      type: `urn:go-admin-plus:problem:${category.replace('_', '-')}`,
      title: 'Public request failed',
      status,
      category,
      code,
      traceId: '0123456789abcdef'
    }
    const client = createContractClient({
      baseUrl: 'https://api.example.test',
      fetch: async() => Response.json(problem, {
        status,
        headers: { 'Content-Type': 'application/problem+json' }
      })
    })

    const result = await client.POST('/contract/probe', { body: { value: category } })

    expect(result.data).toBeUndefined()
    expect(result.error).toEqual(problem)
    expect(JSON.stringify(result.error).toLowerCase()).not.toMatch(/select password from|stack trace|\/var\/|c:\\|secret=|session=raw/)
  })
})
