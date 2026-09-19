// 只记录 HTTP 状态和固定错误码；验收失败时不输出请求体、身份或凭据。
export const installRequestDiagnostic = () => {
  const original = globalThis.fetch.bind(globalThis)
  let last = 'none'
  let mutations = 0
  globalThis.fetch = async (input, init) => {
    const method = init?.method ?? (input instanceof Request ? input.method : 'GET')
    if (!['GET', 'HEAD', 'OPTIONS'].includes(method)) mutations += 1
    const response = await original(input, init)
    if (!response.ok) {
      const problem = await response.clone().json().catch(() => null) as { code?: unknown } | null
      const code = typeof problem?.code === 'string' && /^[A-Za-z0-9_-]{1,64}$/.test(problem.code) ? problem.code : 'unknown'
      last = `${response.status}:${code}`
    }
    return response
  }
  return () => `${last}:writes-${mutations}`
}
