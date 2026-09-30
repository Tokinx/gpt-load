package scheduler

import (
	"gpt-load/internal/channel"
	"gpt-load/internal/state"
)

// routingCandidate contains only the fields used to classify a candidate into
// the current primary/standby priority layer. Runtime health and fairness are
// intentionally excluded so the scheduler and inspector share a pure policy.
type routingCandidate struct {
	groupPriority int
}

type routingTier struct {
	groupPriority int
}

// routeModeTiersForStrategy is the single source of truth for the native vs
// converted route layers. The scheduler and route inspector must explain the
// same layer ordering.
func routeModeTiersForStrategy(strategy state.RouteStrategy) [][]channel.RouteMode {
	if strategy == state.RouteStrategyWeightedMix {
		return [][]channel.RouteMode{{channel.RouteNative, channel.RouteConverted}}
	}
	return [][]channel.RouteMode{{channel.RouteNative}, {channel.RouteConverted}}
}

func highestRoutingTier(candidates []routingCandidate) (routingTier, bool) {
	if len(candidates) == 0 {
		return routingTier{}, false
	}
	tier := routingTier{groupPriority: candidates[0].groupPriority}
	for _, candidate := range candidates[1:] {
		if candidate.groupPriority > tier.groupPriority {
			tier.groupPriority = candidate.groupPriority
		}
	}
	return tier, true
}

func inRoutingTier(candidate routingCandidate, tier routingTier) bool {
	return candidate.groupPriority == tier.groupPriority
}

func containsRouteMode(modes []channel.RouteMode, mode channel.RouteMode) bool {
	for _, candidate := range modes {
		if candidate == mode {
			return true
		}
	}
	return false
}

func routeLayerIndex(storeDowngraded bool, mode channel.RouteMode, modeTiers [][]channel.RouteMode) int {
	modeLayer := 0
	for index, modes := range modeTiers {
		if containsRouteMode(modes, mode) {
			modeLayer = index
			break
		}
	}
	if storeDowngraded {
		return len(modeTiers) + modeLayer
	}
	return modeLayer
}

// filterHighestRoutingTier keeps the highest available group priority.
// Credentials within that tier retain upstream effective-weight fairness.
func filterHighestRoutingTier(candidates []weightedCredential) []weightedCredential {
	policyCandidates := make([]routingCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		policyCandidates = append(policyCandidates, routingCandidate{
			groupPriority: candidate.groupPriority,
		})
	}
	tier, ok := highestRoutingTier(policyCandidates)
	if !ok {
		return nil
	}
	result := make([]weightedCredential, 0, len(candidates))
	for _, candidate := range candidates {
		if inRoutingTier(routingCandidate{
			groupPriority: candidate.groupPriority,
		}, tier) {
			result = append(result, candidate)
		}
	}
	return result
}
