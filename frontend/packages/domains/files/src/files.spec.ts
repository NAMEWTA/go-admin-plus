import { describe, expect, it } from 'vitest'
import { codePointLength, maximumFileBytes, validFileName, validFileSearch, validUploadCandidate } from './files'

describe('files domain', () => {
  it('uses Unicode code-point limits for names and search', () => {
    expect(codePointLength('😀')).toBe(1)
    expect(validFileSearch('😀'.repeat(100))).toBe(true)
    expect(validFileSearch('😀'.repeat(101))).toBe(false)
    expect(validUploadCandidate({ name: `${'😀'.repeat(255)}`, type: 'text/plain', size: 1, body: new Blob(['x'], { type: 'text/plain' }) })).toBe(true)
    expect(validUploadCandidate({ name: `${'😀'.repeat(256)}`, type: 'text/plain', size: 1, body: new Blob(['x'], { type: 'text/plain' }) })).toBe(false)
  })

  it('rejects traversal, unsupported media and oversized candidates', () => {
    expect(validUploadCandidate({ name: '../outside.txt', type: 'text/plain', size: 1, body: new Blob(['x']) })).toBe(false)
    expect(validUploadCandidate({ name: 'file.exe', type: 'application/octet-stream', size: 1, body: new Blob(['x']) })).toBe(false)
    expect(validUploadCandidate({ name: 'large.txt', type: 'text/plain', size: maximumFileBytes + 1, body: new Blob([]) })).toBe(false)
  })

  it('rejects every Unicode control character accepted by JavaScript strings', () => {
    expect(validFileName('line\nfeed.txt')).toBe(false)
    expect(validFileName('next\u0085line.txt')).toBe(false)
    expect(validFileName('printable\u2028separator.txt')).toBe(true)
  })
})
