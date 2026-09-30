import { describe, expect, test } from 'bun:test'
import {
  activeCredentialCount,
  activeGroups,
  credentialActive,
  groupStandbyReason,
  groupWeight,
  groupsWeight,
  reasonLabel,
  standbyReasonLabel,
} from './inspection-display'

function credential(overrides = {}) {
  return {
    id: 1,
    available: true,
    active: true,
    standbyReason: null,
    reason: null,
    weight: 50,
    effectiveWeight: 50,
    cooldownUntil: null,
    ...overrides,
  }
}

function group(overrides = {}) {
  return {
    id: 1,
    name: 'group',
    channelID: 'openai',
    mode: 'native',
    requirementSatisfied: true,
    model: null,
    priority: 50,
    weight: 50,
    included: true,
    active: true,
    routable: true,
    reason: null,
    credentials: [credential()],
    ...overrides,
  }
}

function inspection(groups, routable = true) {
  return {
    observedAt: 0,
    revision: 1,
    strategy: 'native_first',
    protocol: 'openai-responses',
    operation: 'chat_completion',
    requirement: 'any',
    model: null,
    accessKey: { id: 1, name: 'key', status: 'active' },
    routable,
    reason: null,
    groups,
  }
}

const t = (key) => key

describe('credentialActive', () => {
  test('requires both available and active', () => {
    expect(credentialActive(credential())).toBe(true)
    expect(credentialActive(credential({ active: false }))).toBe(false)
    expect(credentialActive(credential({ available: false }))).toBe(false)
  })
})

describe('current layer weight', () => {
  test('counts only active credentials of a group', () => {
    const subject = group({
      credentials: [
        credential({ id: 1, effectiveWeight: 70 }),
        credential({
          id: 2,
          active: false,
          standbyReason: 'route_mode_standby',
          effectiveWeight: 30,
        }),
        credential({ id: 3, available: false, effectiveWeight: 20 }),
      ],
    })
    expect(activeCredentialCount(subject)).toBe(1)
    expect(groupWeight(subject)).toBe(70)
  })

  test('deduplicates shared credentials across active groups', () => {
    const shared = credential({ id: 9, effectiveWeight: 40 })
    const standby = credential({ id: 10, active: false, effectiveWeight: 40 })
    const total = groupsWeight([
      group({ id: 1, credentials: [shared, standby] }),
      group({ id: 2, credentials: [shared] }),
    ])
    expect(total).toBe(40)
  })
})

describe('activeGroups', () => {
  test('uses backend active/included/routable instead of route mode heuristics', () => {
    const result = inspection([
      group({ id: 1, mode: 'converted', active: true }),
      group({ id: 2, mode: 'native', active: false }),
      group({ id: 3, active: true, included: false }),
    ])
    expect(activeGroups(result).map((item) => item.id)).toEqual([1])
    expect(activeGroups(inspection([group({ id: 1 })], false))).toEqual([])
  })
})

describe('standby reasons', () => {
  test('reads the first standby reason from an available credential', () => {
    const standby = group({
      active: false,
      credentials: [
        credential({ id: 1, active: false, standbyReason: 'lower_group_priority' }),
        credential({ id: 2, active: false, standbyReason: 'route_mode_standby' }),
      ],
    })
    expect(groupStandbyReason(standby)).toBe('lower_group_priority')
    expect(groupStandbyReason(group({ active: true }))).toBeNull()
    expect(groupStandbyReason(group({ active: false, credentials: [] }))).toBeNull()
  })

  test('maps known codes through i18n and passes unknown codes through', () => {
    expect(standbyReasonLabel('lower_group_priority', t)).toBe(
      'inspector.standbyReasons.lower_group_priority',
    )
    expect(standbyReasonLabel('custom_reason', t)).toBe('custom_reason')
    expect(standbyReasonLabel(null, t)).toBeNull()
    expect(reasonLabel(null, t)).toBe('—')
  })
})
