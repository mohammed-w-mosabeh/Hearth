package docker

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
	"github.com/mohammed-w-mosabeh/hearth/internal/broker"
)

// Networks and volumes (ROADMAP 5.5): sizes for the page, and removing one
// orphan. An orphan is what Cleanup calls unused, from the same preview, so
// the two pages cannot disagree. Anything else is blocked here, and the
// Engine itself refuses what became used after the preview; force is never
// sent, so that refusal stands.

// ReasonInUse is a network or volume that is not an orphan.
const ReasonInUse = "in-use"

var anonymousVolume = regexp.MustCompile(`^[a-f0-9]{64}$`)

// HandleVolumeSizes serves GET /api/v1/docker/volumes/sizes. It is apart
// from the snapshot because the Engine walks each volume to answer.
func HandleVolumeSizes(b Broker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
		defer cancel()
		sizes := []broker.VolumeSize{}
		if err := b.Call(ctx, "docker.volumes.sizes", nil, &sizes); err != nil {
			writeError(w, brokerStatus(err), errorText(err))
			return
		}
		writeJSON(w, map[string]any{"sizes": sizes})
	}
}

// orphanKind is what differs between the two actions.
type orphanKind struct {
	kind   string // the action's kind
	prune  string // its docker.prune.preview kind
	method string
	valid  func(string) bool
	params func(target string) any
}

var orphanKinds = []orphanKind{
	{"network", "networks", "docker.networks.remove", broker.ValidNetwork,
		func(t string) any { return map[string]string{"id": t} }},
	{"volume", "volumes", "docker.volumes.remove", broker.ValidVolume,
		func(t string) any { return map[string]string{"name": t} }},
}

// planOrphan finds target among the unused. A volume's data goes for good,
// so its name is typed; an anonymous one's 64 digits are cut to 12.
func planOrphan(ctx context.Context, b Broker, k orphanKind, target string) (actions.Plan, error) {
	p := actions.Plan{Summary: "remove " + k.kind + " " + target, Items: []actions.Item{}, Blocked: []actions.Blocked{}}
	pv, err := preview(ctx, b, k.prune)
	if err != nil {
		return p, brokerError(err)
	}
	for _, it := range pv.Items {
		if it.ID != target {
			continue
		}
		item := actions.Item{ID: it.ID, Name: it.Name}
		if item.Name == "" {
			item.Name = it.ID
		}
		if k.kind == "volume" {
			if it.Size >= 0 {
				item.Detail = humanBytes(it.Size)
			}
			p.Confirm = it.ID
			if anonymousVolume.MatchString(it.ID) {
				p.Confirm = it.ID[:12]
			}
		}
		p.Items = append(p.Items, item)
		return p, nil
	}
	p.Blocked = append(p.Blocked, actions.Blocked{Item: actions.Item{ID: target, Name: target}, Reason: ReasonInUse})
	return p, nil
}

func runOrphan(ctx context.Context, b Broker, k orphanKind, p actions.Plan) (actions.Result, error) {
	res := actions.Result{Failed: []actions.Blocked{}, Blocked: p.Blocked}
	if len(p.Items) == 0 {
		return res, nil
	}
	it := p.Items[0]
	if err := b.Call(ctx, k.method, k.params(it.ID), nil); err != nil {
		if bl, ok := failure(it, err); ok {
			res.Failed = append(res.Failed, bl)
			return res, nil
		}
		return res, brokerError(err)
	}
	res.Done = 1
	return res, nil
}

// RegisterOrphans adds network/remove and volume/remove (SECURITY S15).
func RegisterOrphans(reg *actions.Registry, b Broker) {
	for _, k := range orphanKinds {
		reg.Register(actions.Action{Kind: k.kind, Name: "remove", Risk: actions.Destructive,
			Validate: func(r actions.Request) error {
				if !k.valid(r.Target) || len(r.Params) != 0 {
					return errors.New("target must be a " + k.kind + " and params empty")
				}
				return nil
			},
			Plan: func(ctx context.Context, r actions.Request) (actions.Plan, error) {
				return planOrphan(ctx, b, k, r.Target)
			},
			Run: func(ctx context.Context, _ actions.Request, p actions.Plan) (actions.Result, error) {
				return runOrphan(ctx, b, k, p)
			}})
	}
}
