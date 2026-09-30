import type { Inspection, InspectionCredential, InspectionGroup } from '@modern/api/inspector'

// 后端 `active` 表示“当前参与调度的层”，`available` 仍表示可调度（含备用层）。
export function credentialActive(credential: InspectionCredential): boolean {
  return credential.available && credential.active
}
export function activeCredentialCount(group: InspectionGroup): number {
  return group.credentials.filter(credentialActive).length
}
// 当前层有效权重合计：只有 active 凭据参与份额计算。
export function groupWeight(group: InspectionGroup): number {
  return group.credentials.reduce(
    (total, credential) => total + (credentialActive(credential) ? credential.effectiveWeight : 0),
    0,
  )
}
// 同组多个模型目标共享凭据，每份凭据权重只累计一次。
export function groupsWeight(groups: readonly InspectionGroup[]): number {
  const weights = new Map<number, number>()
  for (const group of groups) {
    for (const credential of group.credentials) {
      if (credentialActive(credential)) weights.set(credential.id, credential.effectiveWeight)
    }
  }
  return [...weights.values()].reduce((total, weight) => total + weight, 0)
}
// 当前层由后端判定（group.active），前端不再用路由模式推测。
export function activeGroups(result: Inspection): InspectionGroup[] {
  if (!result.routable) return []
  return result.groups.filter((group) => group.included && group.active)
}
// 备用分组的理由来自其凭据的 standby_reason（如 lower_group_priority / route_mode_standby）。
export function groupStandbyReason(group: InspectionGroup): string | null {
  if (group.active) return null
  for (const credential of group.credentials) {
    if (credential.available && credential.standbyReason) return credential.standbyReason
  }
  return null
}
export function reasonLabel(reason: string | null, t: (key: string) => string): string {
  const known = [
    'access_key_disabled',
    'access_key_expired',
    'protocol_filtered',
    'model_filtered',
    'model_required_by_filter',
    'operation_unsupported',
    'native_route_required',
    'no_route_target',
    'codex_live_disabled',
    'group_disabled',
    'group_filtered',
    'no_available_group',
    'no_credentials',
    'group_weight_zero',
    'credential_disabled',
    'credential_blacklisted',
    'credential_cooldown',
    'model_cooldown',
    'credential_auth_unavailable',
    'credential_weight_zero',
    'credential_not_allowed',
    'no_available_credential',
  ]
  return reason ? (known.includes(reason) ? t('inspector.reasons.' + reason) : reason) : '—'
}
export function standbyReasonLabel(
  reason: string | null,
  t: (key: string) => string,
): string | null {
  const known = [
    'lower_group_priority',
    'route_mode_standby',
    'store_downgraded_standby',
  ]
  return reason ? (known.includes(reason) ? t('inspector.standbyReasons.' + reason) : reason) : null
}
