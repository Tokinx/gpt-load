package scheduler

import (
	"errors"
	"testing"
	"time"

	"gpt-load/internal/execution"
	"gpt-load/internal/protocol"
	"gpt-load/internal/state"
)

func TestFilterHighestRoutingTierUsesOnlyGroupPriority(t *testing.T) {
	t.Parallel()
	got := filterHighestRoutingTier([]weightedCredential{
		{meta: state.CredentialMeta{ID: 1}, groupPriority: 50, weight: 10000},
		{meta: state.CredentialMeta{ID: 2}, groupPriority: 90, weight: 1},
		{meta: state.CredentialMeta{ID: 3}, groupPriority: 90, weight: 50},
	})
	if len(got) != 2 || got[0].meta.ID != 2 || got[1].meta.ID != 3 {
		t.Fatalf("highest group tier=%#v", got)
	}
}

func TestIteratorPrefersHigherGroupPriority(t *testing.T) {
	t.Parallel()
	snapshot := schedulerSnapshot()
	high, low := 80, 20
	group := snapshot.Groups[1]
	group.PriorityManual = &low
	snapshot.Groups[1] = group
	group = snapshot.Groups[2]
	group.PriorityManual = &high
	snapshot.Groups[2] = group

	source := fakeCredentialSource{keys: []state.CredentialMeta{
		{ID: 11, GroupID: 1, WeightManual: new(100)},
		{ID: 21, GroupID: 2, WeightManual: new(1)},
	}}
	source.progress = state.NewSchedulingState()
	for range 50 {
		selection, err := New(snapshot, source, Query{
			ClientProtocol: protocol.OpenAICompletions,
			Operation:      execution.OperationChatCompletion,
			ExternalModel:  modelPointer("gpt-4o"),
		}).Next()
		if err != nil {
			t.Fatalf("Next() error = %v", err)
		}
		if selection.CredentialID != 21 || selection.GroupID != 2 {
			t.Fatalf("selection = %#v, want higher-priority group credential 21", selection)
		}
	}
}

func TestIteratorFallsBackToLowerPriorityAfterTried(t *testing.T) {
	t.Parallel()
	snapshot := schedulerSnapshot()
	high, low := 80, 20
	group := snapshot.Groups[1]
	group.PriorityManual = &high
	snapshot.Groups[1] = group
	group = snapshot.Groups[2]
	group.PriorityManual = &low
	snapshot.Groups[2] = group

	source := fakeCredentialSource{keys: []state.CredentialMeta{
		{ID: 11, GroupID: 1},
		{ID: 21, GroupID: 2},
	}}
	source.progress = state.NewSchedulingState()
	iterator := New(snapshot, source, Query{
		ClientProtocol: protocol.OpenAICompletions,
		Operation:      execution.OperationChatCompletion,
		ExternalModel:  modelPointer("gpt-4o"),
	})
	first, err := iterator.Next()
	if err != nil || first.CredentialID != 11 {
		t.Fatalf("first Next() = (%#v, %v), want credential 11", first, err)
	}
	second, err := iterator.Next()
	if err != nil || second.CredentialID != 21 {
		t.Fatalf("second Next() = (%#v, %v), want fallback credential 21", second, err)
	}
	if _, err := iterator.Next(); !errors.Is(err, ErrExhausted) {
		t.Fatalf("third Next() error = %v, want ErrExhausted", err)
	}
}

func TestIteratorPreferredCredentialDoesNotCrossPriorityTier(t *testing.T) {
	t.Parallel()
	snapshot := schedulerSnapshot()
	high, low := 80, 20
	group := snapshot.Groups[1]
	group.PriorityManual = &high
	snapshot.Groups[1] = group
	group = snapshot.Groups[2]
	group.PriorityManual = &low
	snapshot.Groups[2] = group

	source := fakeCredentialSource{keys: []state.CredentialMeta{
		{ID: 11, GroupID: 1},
		{ID: 21, GroupID: 2},
	}}
	source.progress = state.NewSchedulingState()
	selection, err := New(snapshot, source, Query{
		ClientProtocol:        protocol.OpenAICompletions,
		Operation:             execution.OperationChatCompletion,
		ExternalModel:         modelPointer("gpt-4o"),
		PreferredCredentialID: 21,
	}).Next()
	if err != nil || selection.CredentialID != 11 {
		t.Fatalf("Next() = (%#v, %v), want high-priority credential 11 over preferred 21", selection, err)
	}
}

func TestIteratorSamePriorityStillUsesWeights(t *testing.T) {
	t.Parallel()
	priority := 70
	snapshot := schedulerSnapshot()
	for _, groupID := range []uint{1, 2} {
		group := snapshot.Groups[groupID]
		group.PriorityManual = &priority
		snapshot.Groups[groupID] = group
	}
	groupWeight := 50
	for _, groupID := range []uint{1, 2} {
		group := snapshot.Groups[groupID]
		group.WeightManual = &groupWeight
		snapshot.Groups[groupID] = group
	}

	source := fakeCredentialSource{keys: []state.CredentialMeta{
		{ID: 11, GroupID: 1, WeightManual: new(100)},
		{ID: 21, GroupID: 2, WeightManual: new(50)},
	}}
	source.progress = state.NewSchedulingState()
	counts := map[uint]int{}
	for range 12000 {
		selection, err := New(snapshot, source, Query{
			ClientProtocol: protocol.OpenAICompletions,
			Operation:      execution.OperationChatCompletion,
			ExternalModel:  modelPointer("gpt-4o"),
		}).Next()
		if err != nil {
			t.Fatalf("Next() error = %v", err)
		}
		counts[selection.CredentialID]++
	}
	ratio := float64(counts[11]) / float64(counts[21])
	if ratio < 1.85 || ratio > 2.15 {
		t.Fatalf("same-priority weighted counts = %#v, ratio = %.3f, want about 2:1", counts, ratio)
	}
}

// Recovery re-enters the primary tier immediately; fairness debt and affinity
// must not keep new requests on a standby credential.
func TestPriorityFailoverAndRecovery(t *testing.T) {
	t.Parallel()
	now := inspectNow()
	snapshot := schedulerSnapshot()
	group := snapshot.Groups[1]
	group.PriorityManual = new(90)
	snapshot.Groups[1] = group
	registry := state.NewCredentialRegistry()
	if err := registry.ReplaceCredentials([]state.CredentialEntry{
		{ID: 11, GroupID: 1, Version: 1, IdentityGeneration: 1, Status: state.CredentialStatusActive, Fingerprint: "primary", EncryptedValue: "primary"},
		{ID: 21, GroupID: 2, Version: 1, IdentityGeneration: 1, Status: state.CredentialStatusActive, Fingerprint: "backup", EncryptedValue: "backup"},
	}); err != nil {
		t.Fatal(err)
	}
	query := fairnessQuery(21)
	assertPick := func(want uint) {
		t.Helper()
		got, err := newWithClock(snapshot, registry, query, func() time.Time { return now }).Next()
		if err != nil || got.CredentialID != want {
			t.Fatalf("selection=%#v err=%v want=%d", got, err, want)
		}
		inspection, err := Inspect(snapshot, registry.Snapshot(), query, now)
		if err != nil {
			t.Fatal(err)
		}
		for _, g := range inspection.Groups {
			for _, c := range g.Credentials {
				if c.Active != (c.CredentialID == want) {
					t.Fatalf("inspector disagrees: %#v", inspection)
				}
			}
		}
	}
	assertPick(11)
	ref, _ := registry.CredentialRef(11)
	registry.SetModelCooldown(ref, "gpt-4o", now.Add(time.Hour), now)
	assertPick(21)
	registry.ClearModelCooldowns(11)
	assertPick(11)
}
