import { describe, expect, it } from 'vitest'
import { resourcePresetVerbOptions, trimPreset, verbOptionsOf, type RolePreset } from '../grant'

describe('resource permission presets', () => {
  it('filters pod-only verbs by resource', () => {
    expect(verbOptionsOf(['deployments'])).not.toContain('exec')
    expect(verbOptionsOf(['pods'])).toContain('exec')
  })

  it('keeps explicit wildcard for admin resource presets', () => {
    const admin: RolePreset = { verbs: ['*'], actions: ['*'] }
    expect(resourcePresetVerbOptions('deployments', admin)[0]).toBe('*')
  })

  it('trims global fallbacks before seeding a resource override', () => {
    const scoped = trimPreset({ verbs: ['get', 'list', 'logs', 'exec'], actions: ['*'] }, ['deployments'])
    expect(scoped.verbs).toEqual(['get', 'list'])
    expect(scoped.actions).toEqual(['*'])
  })
})
