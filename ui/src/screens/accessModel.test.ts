import { describe, expect, it } from 'vitest'
import { principalStateLabel } from './accessModel'
import type { PrincipalObject } from '../api'

function principal(overrides: Partial<PrincipalObject> = {}): PrincipalObject {
  return {
    id: 'p1',
    name: 'erbartos',
    kind: 'human',
    role: 'operator',
    disabled: false,
    hasPassword: true,
    reserved: false,
    createdAt: '2026-08-01T00:00:00Z',
    ...overrides,
  } as unknown as PrincipalObject
}

describe('principalStateLabel', () => {
  it('is Active for an enabled, recently-used, non-viewer principal', () => {
    expect(principalStateLabel(principal(), false)).toEqual({ label: 'Active', tone: 'good' })
  })

  it('is Read only for a viewer, ahead of Active', () => {
    expect(principalStateLabel(principal({ role: 'viewer' }), false)).toEqual({ label: 'Read only', tone: 'unknown' })
  })

  it('is Consider revoking ahead of Read only for a long-unused viewer', () => {
    expect(principalStateLabel(principal({ role: 'viewer' }), true)).toEqual({ label: 'Consider revoking', tone: 'warn' })
  })

  it('is Disabled ahead of Consider revoking and Read only', () => {
    expect(principalStateLabel(principal({ role: 'viewer', disabled: true }), true)).toEqual({ label: 'Disabled', tone: 'bad' })
  })
})
