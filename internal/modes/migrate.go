package modes

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

// MigrateDB is what Migrate reads and writes (store.DB).
type MigrateDB interface {
	Profiles(ctx context.Context) ([]store.Profile, error)
	ModeNumbers(ctx context.Context, ids []string) (map[string]int64, error)
	AdoptProfile(ctx context.Context, a store.AdoptedProfile) error
	Audit(ctx context.Context, e store.AuditEntry) error
}

// Migrate turns every profile left in the database into a mode file
// (ROADMAP 10.2), once: each profile's row goes in the same transaction that
// gives its mode the profile's number, its restore list and its game
// trigger, so the audit log's profile IDs still name it and an active profile
// can still be restored. A file written before a crash is found again by
// its content. It returns how many profiles moved; one that cannot move stays
// in the database and is reported in the error, and the others go on.
func Migrate(ctx context.Context, db MigrateDB, set *Set, uid int) (int, error) {
	list, err := db.Profiles(ctx)
	if err != nil || len(list) == 0 {
		return 0, err
	}
	root, err := openDir(set.cfg.Dir)
	if err != nil {
		return 0, err
	}
	defer func() { _ = root.Close() }()
	moved := 0
	var failed []string
	for _, p := range list {
		id, err := migrateOne(ctx, db, root, p)
		result := "ok"
		if err != nil {
			result = "error: " + err.Error()
			failed = append(failed, fmt.Sprintf("profile %d: %v", p.ID, err))
		} else {
			moved++
		}
		if aerr := db.Audit(ctx, store.AuditEntry{Origin: store.OriginLocal, UID: uid, Action: "profile.migrate",
			Target: strconv.FormatInt(p.ID, 10), Params: map[string]string{"mode": id}, Result: result}); aerr != nil {
			failed = append(failed, "audit: "+aerr.Error())
		}
	}
	set.Load(ctx)
	if len(failed) > 0 {
		return moved, fmt.Errorf("modes: migration: %s", strings.Join(failed, "; "))
	}
	return moved, nil
}

func migrateOne(ctx context.Context, db MigrateDB, root *os.Root, p store.Profile) (string, error) {
	name := migrateName(p.Name, p.ID)
	m, err := apply(Mode{Stacks: []StackAction{}, Triggers: []Trigger{}}, store.Profile{Name: name, Items: p.Items, AutoGame: p.AutoGame})
	if err != nil {
		return "", err
	}
	raw, err := Encode(m)
	if err != nil {
		return "", err
	}
	if _, err := Parse("migrated", raw); err != nil {
		return "", err
	}
	nums, err := db.ModeNumbers(ctx, nil)
	if err != nil {
		return "", err
	}
	same := func(id string) bool {
		cur, err := readFile(root, id+".toml")
		return err == nil && bytes.Equal(cur, raw)
	}
	id, err := freeID(Slug(name), func(id string) bool {
		_, used := nums[id]
		return used || (exists(root, id) && !same(id))
	})
	if err != nil {
		return "", err
	}
	if !exists(root, id) {
		if err := writeFile(root, id, raw, ""); err != nil {
			return id, err
		}
	}
	hash := Hash(raw)
	a := store.AdoptedProfile{ProfileID: p.ID, ModeID: id}
	if !p.ActivatedAt.IsZero() {
		undo := json.RawMessage(p.Undo)
		if !json.Valid(undo) {
			undo = json.RawMessage("[]")
		}
		snap, err := json.Marshal(snapshot{Undo: undo})
		if err != nil {
			return id, err
		}
		a.Active = &store.ActiveMode{FileHash: hash, Since: p.ActivatedAt, By: p.ActivatedBy, Snapshot: string(snap)}
	}
	if p.AutoGame {
		// The user turned the game trigger on in Hearth; it stays armed.
		for i, t := range m.Triggers {
			if t.Kind == TriggerGame {
				a.Arm = &store.ModeArming{Trigger: i, FileHash: hash, ArmedAt: time.Now()}
			}
		}
	}
	return id, db.AdoptProfile(ctx, a)
}

// migrateName is a profile's name as a mode name: profiles allowed some
// invisible characters and spaces at the ends that modes refuse.
func migrateName(name string, id int64) string {
	var b strings.Builder
	n := 0
	for _, r := range name {
		if unicode.IsGraphic(r) && n < maxName {
			b.WriteRune(r)
			n++
		}
	}
	if s := strings.TrimSpace(b.String()); s != "" {
		return s
	}
	return "Profile " + strconv.FormatInt(id, 10)
}
