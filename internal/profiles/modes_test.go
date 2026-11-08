package profiles

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/modes"
	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

// modeStore is profiles kept as mode files, with the database's audit log.
type modeStore struct {
	*modes.ProfileStore
	db *store.DB
}

func (s modeStore) AuditBetween(ctx context.Context, from, to time.Time, limit int) ([]store.AuditRow, error) {
	return s.db.AuditBetween(ctx, from, to, limit)
}

// TestProfilesOnModes runs every profile test again with profiles kept as
// mode files (ROADMAP 10.2, Gate 10: migrated profiles behave as before).
// The set has no load-time check, as the database had none, so rows planted
// behind the API reach the run-time checks; modes tests cover that check.
func TestProfilesOnModes(t *testing.T) {
	old := newStore
	t.Cleanup(func() { newStore = old })
	newStore = func(t *testing.T, db *store.DB) testStore {
		set := modes.NewSet(modes.Config{Dir: filepath.Join(t.TempDir(), "modes"), Store: db})
		set.Load(context.Background())
		return modeStore{ProfileStore: &modes.ProfileStore{Set: set, DB: db}, db: db}
	}
	for _, tc := range []struct {
		name string
		fn   func(*testing.T)
	}{
		{"SaveValidation", TestSaveValidation},
		{"ActivateAndRestoreOnlyWhatChanged", TestActivateAndRestoreOnlyWhatChanged},
		{"StartsAloneNeedNoToken", TestStartsAloneNeedNoToken},
		{"StopWithoutPreviewIsRefused", TestStopWithoutPreviewIsRefused},
		{"RunTimeBlock", TestRunTimeBlock},
		{"PartialFailures", TestPartialFailures},
		{"GameAutoMode", TestGameAutoMode},
		{"GameLeavesManualActivationAlone", TestGameLeavesManualActivationAlone},
		{"RestoresAfterRestart", TestRestoresAfterRestart},
		{"AutoRestoreFailureStaysForTheUser", TestAutoRestoreFailureStaysForTheUser},
		{"AutoRefusesPasswordItems", TestAutoRefusesPasswordItems},
		{"Handlers", TestHandlers},
		{"StacksRunInOneBatch", TestStacksRunInOneBatch},
		{"StackBatchPartial", TestStackBatchPartial},
		{"GameRefusesStacks", TestGameRefusesStacks},
	} {
		t.Run(tc.name, tc.fn)
	}
	// The suite above really ran on files.
	t.Run("WritesModeFiles", func(t *testing.T) {
		f := setup(t)
		id := f.add(t, "Dev Box", true, userUnit, app)
		ms, ok := f.db.(modeStore)
		if !ok {
			t.Fatalf("%T", f.db)
		}
		list := ms.Set.Modes()
		if len(list) != 1 || list[0].ID != "dev-box" || !list[0].HasGame() || len(list[0].Items) != 1 || len(list[0].Session.Open) != 1 {
			t.Fatalf("%+v", list)
		}
		if p := f.profile(t, id); !p.AutoGame || p.Name != "Dev Box" {
			t.Fatalf("%+v", p)
		}
	})
}
