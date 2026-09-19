const nativeFailurePattern = /^(desktop native (?:startup|login|logout|request|identity) failed: [A-Za-z ]{1,96})$/gm
const accessibilityFailurePattern = /native (submit button|process|button|field) unavailable/
const accessibilityFieldActionPattern = /native field action unavailable: ([0-9])/
const accessibilityNavigationPattern = /native navigation (start|traversal|action) unavailable/

export const nativeFailureDiagnostic = output =>
  [...output.matchAll(nativeFailurePattern)].at(-1)?.[1]

export const nativeAccessibilityFailure = output => {
  const fieldAction = output.match(accessibilityFieldActionPattern)?.[1]
  if (fieldAction !== undefined) return `desktop native accessibility field-action-${fieldAction} unavailable`
  const navigation = output.match(accessibilityNavigationPattern)?.[1]
  if (navigation !== undefined) return `desktop native accessibility navigation-${navigation} unavailable`
  const category = output.match(accessibilityFailurePattern)?.[1]
  return category ? `desktop native accessibility ${category.replace(' ', '-')} unavailable` : undefined
}

export const nativePhaseFailure = (phase, output) => {
  const diagnostic = nativeFailureDiagnostic(output)
  return new Error(`desktop native ${phase} failed${diagnostic ? `: ${diagnostic}` : ''}`)
}
