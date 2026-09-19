const sensitiveAssignment = /((?:password|secret|sessionToken|csrfToken|credential|authorization)["']?\s*[:=]\s*)(?:"[^"\r\n]*"|'[^'\r\n]*'|[^,\s;}]+)/gi
const sensitiveHeader = /((?:cookie|set-cookie|x-csrf-token|authorization):\s*)[^\r\n]+/gi
const sessionCookie = /(__Host-go-admin-session=)[^;\s]+/gi
const URIUserInfo = /((?:postgres(?:ql)?|https?):\/\/)[^@\s/]+@/gi

export const redactDiagnostic = (value, secrets = []) => {
  let result = String(value ?? '')
  for (const secret of secrets) {
    if (secret) result = result.replaceAll(String(secret), '[redacted]')
  }
  return result
    .replace(URIUserInfo, '$1[redacted]@')
    .replace(sessionCookie, '$1[redacted]')
    .replace(sensitiveHeader, '$1[redacted]')
    .replace(sensitiveAssignment, '$1[redacted]')
}

export const createDiagnosticBuffer = (limit = 8_192, secrets = []) => {
  const requestedLimit = Number.isFinite(limit) ? Math.trunc(limit) : 8_192
  const outputLimit = Math.min(65_536, Math.max(1, requestedLimit))
  const rawLineLimit = Math.min(65_536, Math.max(1_024, outputLimit * 2))
  let output = ''
  let pendingLine = ''
  let discardingLine = false

  const appendOutput = (value) => {
    output = (output + value).slice(-outputLimit)
  }
  const finishLine = () => {
    appendOutput(redactDiagnostic(pendingLine, secrets))
    pendingLine = ''
  }
  return {
    append(value) {
      let chunk = String(value ?? '')
      while (chunk) {
        const newline = chunk.indexOf('\n')
        const complete = newline >= 0
        const part = complete ? chunk.slice(0, newline + 1) : chunk
        chunk = complete ? chunk.slice(newline + 1) : ''

        if (discardingLine) {
          if (complete) {
            discardingLine = false
            appendOutput('[truncated output line]\n')
          }
          continue
        }
        if (pendingLine.length + part.length > rawLineLimit) {
          pendingLine = ''
          if (complete) appendOutput('[truncated output line]\n')
          else discardingLine = true
          continue
        }
        pendingLine += part
        if (complete) finishLine()
      }
    },
    text() {
      const tail = discardingLine ? '[truncated output line]' : pendingLine ? '[incomplete output line]' : ''
      return (output + tail).slice(-outputLimit).trim()
    },
  }
}

export const browserExceptionDiagnostic = (details, secrets = []) => {
  const description = details?.exception?.description ?? details?.exception?.value ?? details?.text ?? 'browser exception without public detail'
  return redactDiagnostic(description, secrets).slice(0, 4_096)
}
