// The user's own addresses, lowercased and de-duplicated: every mail
// account's address plus each calendar source's organizer identities.
// Account aliases are not included.
export function collectSelfEmails(
  accountEmails: Array<string | undefined>,
  sources: Array<{ organizerIdentities?: string[] }>,
): string[] {
  const seen = new Set<string>()
  const out: string[] = []
  const push = (raw: string | undefined) => {
    const v = (raw || '').toLowerCase().trim()
    if (v === '' || seen.has(v)) return
    seen.add(v)
    out.push(v)
  }
  for (const email of accountEmails) {
    push(email)
  }
  for (const src of sources) {
    for (const email of src.organizerIdentities ?? []) {
      push(email)
    }
  }
  return out
}
