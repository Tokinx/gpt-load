package scheduler

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"gpt-load/internal/channel"
	"gpt-load/internal/execution"
	"gpt-load/internal/protocol"
	"gpt-load/internal/state"
)

var ErrInconsistentSnapshot = errors.New("inconsistent scheduler snapshot")

type ReasonCode string

const (
	ReasonAccessKeyDisabled         ReasonCode = "access_key_disabled"
	ReasonAccessKeyExpired          ReasonCode = "access_key_expired"
	ReasonProtocolFiltered          ReasonCode = "protocol_filtered"
	ReasonModelFiltered             ReasonCode = "model_filtered"
	ReasonModelRequiredByFilter     ReasonCode = "model_required_by_filter"
	ReasonOperationUnsupported      ReasonCode = "operation_unsupported"
	ReasonNativeRouteRequired       ReasonCode = "native_route_required"
	ReasonNoRouteTarget             ReasonCode = "no_route_target"
	ReasonGroupDisabled             ReasonCode = "group_disabled"
	ReasonCodexLiveDisabled         ReasonCode = "codex_live_disabled"
	ReasonWebsocketDisabled         ReasonCode = "websocket_disabled"
	ReasonGroupFiltered             ReasonCode = "group_filtered"
	ReasonNoAvailableGroup          ReasonCode = "no_available_group"
	ReasonNoCredentials             ReasonCode = "no_credentials"
	ReasonGroupWeightZero           ReasonCode = "group_weight_zero"
	ReasonCredentialDisabled        ReasonCode = "credential_disabled"
	ReasonCredentialAuthUnavailable ReasonCode = "credential_auth_unavailable"
	ReasonCredentialBlacklisted     ReasonCode = "credential_blacklisted"
	ReasonCredentialCooldown        ReasonCode = "credential_cooldown"
	ReasonModelCooldown             ReasonCode = "model_cooldown"
	ReasonCredentialWeightZero      ReasonCode = "credential_weight_zero"
	ReasonCredentialNotAllowed      ReasonCode = "credential_not_allowed"
	ReasonNoAvailableCredential     ReasonCode = "no_available_credential"
	ReasonRouteModeStandby          ReasonCode = "route_mode_standby"
	ReasonStoreDowngradedStandby    ReasonCode = "store_downgraded_standby"
	ReasonGroupPriorityStandby      ReasonCode = "lower_group_priority"
)

type Inspection struct {
	ClientProtocol   protocol.Protocol
	Operation        execution.Operation
	RouteRequirement execution.RouteRequirement
	ExternalModel    *string
	Routable         bool
	Reason           ReasonCode
	Groups           []GroupInspection
}

type GroupInspection struct {
	GroupID                   uint
	GroupName                 string
	ChannelID                 channel.ID
	RouteMode                 channel.RouteMode
	RouteRequirementSatisfied bool
	UpstreamModelID           *string
	PriorityManual            *int
	WeightManual              *int
	Included                  bool
	Routable                  bool
	Active                    bool
	Reason                    ReasonCode
	Credentials               []CredentialInspection
}

type CredentialInspection struct {
	CredentialID    uint
	Available       bool
	Active          bool
	StandbyReason   ReasonCode
	Reason          ReasonCode
	WeightManual    *int
	EffectiveWeight int64
	CooldownUntil   time.Time
}

// CredentialRuntimeView is the scheduler's neutral view of runtime health.
type CredentialRuntimeView = state.CredentialRuntimeView

type targetDecision struct {
	target                   state.RouteTarget
	group                    state.GroupCatalogView
	requirementOK            bool
	responsesStoreDowngraded bool
	included                 bool
	reason                   ReasonCode
}

func cloneWeight(weight *int) *int {
	if weight == nil {
		return nil
	}
	value := *weight
	return &value
}

func evaluateTargets(
	snapshot *state.ConfigSnapshot,
	index state.ExecutionCandidateIndex,
	query normalizedQuery,
) ([]targetDecision, ReasonCode, error) {
	if snapshot == nil {
		return nil, "", fmt.Errorf("%w: nil ConfigSnapshot", ErrInconsistentSnapshot)
	}
	if query.accessKey.Status == state.AccessKeyStatusDisabled {
		return []targetDecision{}, ReasonAccessKeyDisabled, nil
	}
	if len(query.accessKey.Filters.Protocols) > 0 {
		if _, allowed := query.accessKey.Filters.Protocols[query.clientProtocol]; !allowed {
			return []targetDecision{}, ReasonProtocolFiltered, nil
		}
	}
	if len(query.accessKey.Filters.Models) > 0 {
		if query.externalModel == nil {
			return []targetDecision{}, ReasonModelRequiredByFilter, nil
		}
		if _, allowed := query.accessKey.Filters.Models[*query.externalModel]; !allowed {
			return []targetDecision{}, ReasonModelFiltered, nil
		}
	}
	if !query.clientProtocol.Valid() || !query.operation.Valid() ||
		!query.routeRequirement.Valid() || !query.responsesStorePreference.Valid() {
		return []targetDecision{}, ReasonOperationUnsupported, nil
	}
	byOperation := index[query.clientProtocol]
	if len(byOperation) == 0 {
		return []targetDecision{}, ReasonNoRouteTarget, nil
	}
	byModel, operationSupported := byOperation[query.operation]
	if !operationSupported {
		return []targetDecision{}, ReasonOperationUnsupported, nil
	}
	modelKey := state.NoModelRouteKey
	clientSelectedLiveModel := query.clientProtocol == protocol.CodexLive && query.operation == execution.OperationLiveCall
	if query.externalModel != nil && !clientSelectedLiveModel {
		modelKey = *query.externalModel
	}
	routes := byModel[modelKey]
	if len(routes) == 0 {
		return []targetDecision{}, ReasonNoRouteTarget, nil
	}

	decisions := make([]targetDecision, 0, len(routes))
	included := 0
	for _, route := range routes {
		group, exists := snapshot.GroupCatalog[route.GroupID]
		if !exists {
			return nil, "", fmt.Errorf(
				"%w: route target group %d missing from catalog",
				ErrInconsistentSnapshot,
				route.GroupID,
			)
		}
		requirementOK, storeDowngraded, requirementReason := routeRequirementSatisfied(query, route)
		decision := targetDecision{
			target: cloneRouteTarget(route), group: group,
			requirementOK: requirementOK, responsesStoreDowngraded: storeDowngraded,
			included: true,
		}
		if clientSelectedLiveModel && query.externalModel != nil {
			decision.target.UpstreamModelID = *query.externalModel
		}
		groupFiltered := false
		if len(query.accessKey.Filters.Groups) > 0 {
			_, allowed := query.accessKey.Filters.Groups[route.GroupID]
			groupFiltered = !allowed
		}
		switch {
		case !requirementOK:
			decision.included = false
			decision.reason = requirementReason
		case !group.Enabled:
			decision.included = false
			decision.reason = ReasonGroupDisabled
		case clientSelectedLiveModel && snapshot.Groups[route.GroupID].CodexLiveMode == state.CodexLiveOff:
			decision.included = false
			decision.reason = ReasonCodexLiveDisabled
		case groupFiltered:
			decision.included = false
			decision.reason = ReasonGroupFiltered
		case query.responsesWebsocket != nil && !snapshot.Groups[route.GroupID].ResponsesWebsocketEnabled:
			decision.included = false
			decision.reason = ReasonWebsocketDisabled
		}
		if decision.included {
			included++
		}
		decisions = append(decisions, decision)
	}
	if included > 0 {
		return decisions, "", nil
	}
	reason := decisions[0].reason
	for _, decision := range decisions[1:] {
		if decision.reason != reason {
			return decisions, ReasonNoAvailableGroup, nil
		}
	}
	return decisions, reason, nil
}

func routeRequirementSatisfied(
	query normalizedQuery,
	route state.RouteTarget,
) (bool, bool, ReasonCode) {
	if !query.routeRequirement.Allows(execution.RouteMode(route.Mode)) {
		return false, false, ReasonNativeRouteRequired
	}
	if query.responsesWebsocket != nil {
		if query.clientProtocol == protocol.OpenAIResponses && query.operation == execution.OperationResponsesCreate &&
			route.Mode == channel.RouteNative && route.ResolvedTarget.ResponsesWebsocket.Supports(*query.responsesWebsocket) {
			return true, false, ""
		}
		return false, false, ReasonNativeRouteRequired
	}
	if query.operation != execution.OperationResponsesCreate {
		return true, false, ""
	}
	if query.responsesStorePreference == execution.ResponsesStorePreferenceRequireStored {
		if route.Mode == channel.RouteNative && route.ResolvedTarget.ResponsesStoreHandling(
			protocol.OpenAIResponses, execution.OperationResponsesCreate,
		) == channel.ResponsesStoreHandlingUpstreamManaged {
			return true, false, ""
		}
		return false, false, ReasonNativeRouteRequired
	}
	if query.routeRequirement.Normalize() == execution.RouteRequirementNative {
		if route.ResolvedTarget.SupportsResponsesLifecycle() {
			return true, false, ""
		}
		return false, false, ReasonNativeRouteRequired
	}
	if query.responsesStorePreference != execution.ResponsesStorePreferencePreferStored {
		return true, false, ""
	}
	handling := route.ResolvedTarget.ResponsesStoreHandling(
		protocol.OpenAIResponses,
		execution.OperationResponsesCreate,
	)
	switch handling {
	case channel.ResponsesStoreHandlingUpstreamManaged:
		return true, false, ""
	case channel.ResponsesStoreHandlingStateless:
		return true, true, ""
	default:
		return false, false, ReasonOperationUnsupported
	}
}

func accessKeyAllowsGroup(accessKey state.AccessKeyView, groupID uint) bool {
	if len(accessKey.Filters.Groups) == 0 {
		return true
	}
	_, allowed := accessKey.Filters.Groups[groupID]
	return allowed
}

func effectiveWeight(groupManual, credentialManual *int) int64 {
	groupWeight := state.ConfiguredWeight(groupManual)
	credentialWeight := state.ConfiguredWeight(credentialManual)
	if groupWeight <= 0 || credentialWeight <= 0 {
		return 0
	}
	// Priority determines the primary/standby layer. Group and credential
	// weights are only used for fairness among candidates in the same layer.
	return int64(groupWeight) * int64(credentialWeight)
}

func inspectCredential(
	group state.GroupCatalogView,
	credential CredentialRuntimeView,
	allowedCredentialIDs map[uint]struct{},
	allowedCredentialRefs map[uint]credentialIdentity,
	now time.Time,
	model string,
	operation execution.Operation,
) CredentialInspection {
	result := CredentialInspection{
		CredentialID: credential.ID,
		WeightManual: cloneWeight(credential.WeightManual),
	}
	if group.WeightManual != nil && *group.WeightManual == 0 {
		result.Reason = ReasonGroupWeightZero
		return result
	}
	if allowedCredentialIDs != nil {
		if _, allowed := allowedCredentialIDs[credential.ID]; !allowed {
			result.Reason = ReasonCredentialNotAllowed
			return result
		}
	}
	if allowedCredentialRefs != nil {
		ref, allowed := allowedCredentialRefs[credential.ID]
		if !allowed || ref.GroupID != credential.GroupID || ref.IdentityGeneration != credential.IdentityGeneration {
			result.Reason = ReasonCredentialNotAllowed
			return result
		}
	}
	if credential.Status != state.CredentialStatusActive {
		result.Reason = ReasonCredentialDisabled
		return result
	}
	if !credential.AuthReady() {
		result.Reason = ReasonCredentialAuthUnavailable
		return result
	}
	if credential.WeightManual != nil && *credential.WeightManual == 0 {
		result.Reason = ReasonCredentialWeightZero
		return result
	}
	switch credential.RuntimeState(now) {
	case state.CredentialRuntimeBlacklisted:
		result.Reason = ReasonCredentialBlacklisted
	case state.CredentialRuntimeCooldown:
		result.Reason = ReasonCredentialCooldown
		result.CooldownUntil = credential.CooldownUntil
		if until := modelCooldownUntil(credential.ModelCooldowns, model, operation, now); until.After(result.CooldownUntil) {
			result.CooldownUntil = until
		}
	default:
		if until := modelCooldownUntil(credential.ModelCooldowns, model, operation, now); until.After(now) {
			result.Reason, result.CooldownUntil = ReasonModelCooldown, until
			return result
		}
		result.Available = true
		result.EffectiveWeight = effectiveWeight(
			group.WeightManual,
			credential.WeightManual,
		)
	}
	return result
}

func Inspect(
	snapshot *state.ConfigSnapshot,
	credentials []CredentialRuntimeView,
	query Query,
	now time.Time,
) (Inspection, error) {
	normalized := normalizeQuery(query)
	result := Inspection{
		ClientProtocol:   normalized.clientProtocol,
		Operation:        normalized.operation,
		RouteRequirement: normalized.routeRequirement,
		ExternalModel:    cloneString(normalized.externalModel),
		Groups:           []GroupInspection{},
	}
	if snapshot == nil {
		return Inspection{}, fmt.Errorf("%w: nil ConfigSnapshot", ErrInconsistentSnapshot)
	}
	if normalized.accessKey.Status == state.AccessKeyStatusDisabled {
		result.Reason = ReasonAccessKeyDisabled
		return result, nil
	}

	credentialsByGroup := make(map[uint][]CredentialRuntimeView)
	for _, credential := range credentials {
		if _, exists := snapshot.GroupCatalog[credential.GroupID]; !exists {
			return Inspection{}, fmt.Errorf(
				"%w: registry credential %d group %d missing from catalog",
				ErrInconsistentSnapshot,
				credential.ID,
				credential.GroupID,
			)
		}
		cloned := credential
		cloned.WeightManual = cloneWeight(credential.WeightManual)
		credentialsByGroup[credential.GroupID] = append(credentialsByGroup[credential.GroupID], cloned)
	}
	for groupID := range credentialsByGroup {
		sort.Slice(credentialsByGroup[groupID], func(i, j int) bool {
			return credentialsByGroup[groupID][i].ID < credentialsByGroup[groupID][j].ID
		})
	}

	decisions, staticReason, err := evaluateTargets(
		snapshot,
		snapshot.ExecutionRouteCatalog,
		normalized,
	)
	if err != nil {
		return Inspection{}, err
	}
	candidateDecisions, _, err := evaluateTargets(
		snapshot,
		snapshot.ExecutionCandidates,
		normalized,
	)
	if err != nil {
		return Inspection{}, err
	}
	candidateTargets := make(map[inspectionTargetKey]struct{}, len(candidateDecisions))
	for _, decision := range candidateDecisions {
		if decision.included {
			candidateTargets[inspectionTargetKeyOf(decision)] = struct{}{}
		}
	}

	type candidateRef struct{ group, credential int }
	type inspectionCandidate struct {
		ref             candidateRef
		storeDowngraded bool
		mode            channel.RouteMode
		policy          routingCandidate
	}
	candidates := make([]inspectionCandidate, 0)
	groups := make([]GroupInspection, 0, len(decisions))
	for groupIndex, decision := range decisions {
		groupPolicy := decision.group
		if view, exists := snapshot.Groups[decision.group.ID]; exists {
			groupPolicy.PriorityManual = cloneWeight(view.PriorityManual)
			groupPolicy.WeightManual = cloneWeight(view.WeightManual)
		}
		groupResult := GroupInspection{
			GroupID: groupPolicy.ID, GroupName: groupPolicy.Name,
			ChannelID:                 decision.target.ResolvedTarget.ChannelID,
			RouteMode:                 decision.target.Mode,
			RouteRequirementSatisfied: decision.requirementOK,
			UpstreamModelID:           optionalModel(decision.target.UpstreamModelID),
			PriorityManual:            cloneWeight(groupPolicy.PriorityManual),
			WeightManual:              cloneWeight(groupPolicy.WeightManual),
			Included:                  decision.included, Reason: decision.reason,
			Credentials: []CredentialInspection{},
		}
		if !decision.included {
			groups = append(groups, groupResult)
			continue
		}

		groupCredentials := credentialsByGroup[decision.group.ID]
		for credentialIndex, credential := range groupCredentials {
			credentialResult := inspectCredential(
				groupPolicy, credential, normalized.allowedCredentialIDs, normalized.allowedCredentialRefs, now,
				decision.target.UpstreamModelID, normalized.operation,
			)
			groupResult.Credentials = append(groupResult.Credentials, credentialResult)
			if credentialResult.Available && credentialResult.EffectiveWeight > 0 {
				groupResult.Routable = true
			}
			if _, included := candidateTargets[inspectionTargetKeyOf(decision)]; included &&
				credentialResult.Available && credentialResult.EffectiveWeight > 0 {
				candidates = append(candidates, inspectionCandidate{
					ref:             candidateRef{group: groupIndex, credential: credentialIndex},
					storeDowngraded: decision.responsesStoreDowngraded,
					mode:            decision.target.Mode,
					policy: routingCandidate{
						groupPriority: state.ConfiguredPriority(groupPolicy.PriorityManual),
					},
				})
			}
		}
		groupWeightZero := groupPolicy.WeightManual != nil && *groupPolicy.WeightManual == 0
		switch {
		case groupWeightZero:
			groupResult.Reason = ReasonGroupWeightZero
		case len(groupCredentials) == 0:
			groupResult.Reason = ReasonNoCredentials
		case !groupResult.Routable:
			groupResult.Reason = ReasonNoAvailableCredential
		}
		groups = append(groups, groupResult)
	}

	modeTiers := routeModeTiersForStrategy(snapshot.Settings.RouteStrategy)
	activeLayer := -1
	var activeTier routingTier
	for _, pool := range []bool{false, true} {
		if activeLayer >= 0 {
			break
		}
		for layer, modes := range modeTiers {
			layerCandidates := make([]routingCandidate, 0)
			for _, candidate := range candidates {
				if candidate.storeDowngraded != pool || !containsRouteMode(modes, candidate.mode) {
					continue
				}
				layerCandidates = append(layerCandidates, candidate.policy)
			}
			if tier, ok := highestRoutingTier(layerCandidates); ok {
				activeLayer = layer
				if pool {
					activeLayer = len(modeTiers) + layer
				}
				activeTier = tier
				break
			}
		}
	}
	for _, candidate := range candidates {
		if activeLayer < 0 {
			continue
		}
		layer := routeLayerIndex(candidate.storeDowngraded, candidate.mode, modeTiers)
		credential := &groups[candidate.ref.group].Credentials[candidate.ref.credential]
		if layer == activeLayer && inRoutingTier(candidate.policy, activeTier) {
			credential.Active = true
			groups[candidate.ref.group].Active = true
			continue
		}
		credential.StandbyReason = inspectionStandbyReason(candidate.storeDowngraded, layer, activeLayer, len(modeTiers))
	}
	for _, group := range groups {
		if group.Routable {
			result.Routable = true
			break
		}
	}
	result.Groups = groups
	if result.Routable {
		return result, nil
	}
	if staticReason != "" {
		result.Reason = staticReason
	} else {
		result.Reason = ReasonNoAvailableCredential
	}
	return result, nil
}

// inspectionTargetKey keeps route-catalog explanations and executable
// candidates aligned without changing either snapshot index.
type inspectionTargetKey struct {
	groupID         uint
	mode            channel.RouteMode
	model           string
	storeDowngraded bool
}

func inspectionTargetKeyOf(decision targetDecision) inspectionTargetKey {
	return inspectionTargetKey{
		groupID: decision.target.GroupID, mode: decision.target.Mode,
		model:           decision.target.UpstreamModelID,
		storeDowngraded: decision.responsesStoreDowngraded,
	}
}

func inspectionStandbyReason(storeDowngraded bool, layer int, activeLayer int, modeLayerCount int) ReasonCode {
	if layer > activeLayer {
		if storeDowngraded && activeLayer < modeLayerCount {
			return ReasonStoreDowngradedStandby
		}
		return ReasonRouteModeStandby
	}
	return ReasonGroupPriorityStandby
}
