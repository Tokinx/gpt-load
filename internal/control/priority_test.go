package control

import (
	"errors"
	"reflect"
	"testing"

	app_errors "gpt-load/internal/platform/errors"
	"gpt-load/internal/storage/models"
)

func TestGroupPriorityUpdatesPersistPublishAndPreserveOmittedFields(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	groupID := createGroupForCredentialImport(t, fixture, "sk-priority")
	for _, value := range []int{0, -1, 101} {
		field := optionalField[int]{Set: true, Value: value}
		if _, err := fixture.service.UpdateGroupSettings(t.Context(), groupID, GroupSettingsUpdateRequest{PriorityManual: field}); !errors.Is(err, app_errors.ErrValidation) {
			t.Fatalf("group priority %d: %v", value, err)
		}
	}
	for _, want := range []*int{new(1), new(100), nil} {
		field := optionalField[int]{Set: true, Null: want == nil}
		if want != nil {
			field.Value = *want
		}
		if _, err := fixture.service.UpdateGroupSettings(t.Context(), groupID, GroupSettingsUpdateRequest{PriorityManual: field}); err != nil {
			t.Fatal(err)
		}
		// A later weight-only edit must not reset priority.
		if _, err := fixture.service.UpdateGroupSettings(t.Context(), groupID, GroupSettingsUpdateRequest{WeightManual: optionalField[int]{Set: true, Value: 25}}); err != nil {
			t.Fatal(err)
		}
		var group models.Group
		if err := fixture.db.Take(&group, groupID).Error; err != nil {
			t.Fatal(err)
		}
		response, err := fixture.service.GetGroupSettings(t.Context(), groupID)
		if err != nil {
			t.Fatal(err)
		}
		for _, got := range []*int{group.PriorityManual, fixture.manager.Current().Groups[groupID].PriorityManual, response.PriorityManual} {
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("priority=%v want=%v", got, want)
			}
		}
	}
}
