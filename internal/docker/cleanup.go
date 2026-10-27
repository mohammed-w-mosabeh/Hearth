package docker

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
	"github.com/mohammed-w-mosabeh/hearth/internal/broker"
)

// Cleanup (SPEC 5, ROADMAP 2.8): what Docker could reclaim, by kind, and
// prune/<kind> actions. Every prune is previewed (SECURITY S15): the
// preview's time and, for volumes, its exact list go to the broker, so
// nothing created after the preview is removed.

// PruneKinds are the kinds of leftovers, in the order the page shows them.
var PruneKinds = []string{"containers", "images", "networks", "volumes", "build"}

// KindSummary is one kind in GET /api/v1/docker/cleanup.
type KindSummary struct {
	Kind        string             `json:"kind"`
	Count       int                `json:"count"`
	Reclaimable int64              `json:"reclaimable"` // bytes
	Items       []broker.PruneItem `json:"items"`       // the largest first, at most maxSummaryItems
	Error       string             `json:"error,omitempty"`
}

const maxSummaryItems = 20

// maxPlanItems bounds the items a prune plan shows.
const maxPlanItems = 200

func preview(ctx context.Context, b Broker, kind string) (broker.PrunePreview, error) {
	var pv broker.PrunePreview
	err := b.Call(ctx, "docker.prune.preview", map[string]string{"kind": kind}, &pv)
	return pv, err
}

// HandleCleanup serves GET /api/v1/docker/cleanup.
func HandleCleanup(b Broker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
		defer cancel()
		out := make([]KindSummary, 0, len(PruneKinds))
		for _, k := range PruneKinds {
			s := KindSummary{Kind: k, Items: []broker.PruneItem{}}
			pv, err := preview(ctx, b, k)
			if err != nil {
				var be *broker.Error
				if !errors.As(err, &be) || be.Code == broker.CodeNotMember || be.Code == broker.CodeNoDocker {
					writeError(w, brokerStatus(err), errorText(err))
					return
				}
				s.Error = errorText(err)
			} else {
				s.Count, s.Reclaimable = len(pv.Items), pv.Reclaimable
				items := append([]broker.PruneItem(nil), pv.Items...)
				sort.SliceStable(items, func(i, j int) bool { return items[i].Size > items[j].Size })
				s.Items = items[:min(len(items), maxSummaryItems)]
			}
			out = append(out, s)
		}
		writeJSON(w, map[string]any{"kinds": out})
	}
}

// pruneState is what a prune plan saw.
type pruneState struct {
	Until   int64
	Volumes []string
}

// RegisterPrune adds prune/<kind> for each of PruneKinds, target
// "docker". Removing volumes deletes data: their names must be typed.
func RegisterPrune(reg *actions.Registry, b Broker) {
	for _, kind := range PruneKinds {
		reg.Register(actions.Action{Kind: "prune", Name: kind, Risk: actions.Destructive,
			Validate: func(r actions.Request) error {
				if r.Target != "docker" || len(r.Params) != 0 {
					return errors.New(`target must be "docker" and params empty`)
				}
				return nil
			},
			Plan: func(ctx context.Context, _ actions.Request) (actions.Plan, error) { return planPrune(ctx, b, kind) },
			Run: func(ctx context.Context, _ actions.Request, p actions.Plan) (actions.Result, error) {
				return runPrune(ctx, b, kind, p)
			}})
	}
}

func planPrune(ctx context.Context, b Broker, kind string) (actions.Plan, error) {
	p := actions.Plan{Summary: "remove unused " + kind, Items: []actions.Item{}, Blocked: []actions.Blocked{}}
	pv, err := preview(ctx, b, kind)
	if err != nil {
		return p, brokerError(err)
	}
	st := pruneState{Until: pv.Until}
	for i, it := range pv.Items {
		if kind == "volumes" {
			st.Volumes = append(st.Volumes, it.ID)
		}
		if i >= maxPlanItems {
			continue
		}
		name := it.Name
		if name == "" {
			name = shortHex(it.ID)
		}
		detail := ""
		if it.Size > 0 || (it.Size == 0 && kind != "networks") {
			detail = humanBytes(it.Size)
		}
		p.Items = append(p.Items, actions.Item{ID: it.ID, Name: name, Detail: detail})
	}
	if kind == "volumes" && len(st.Volumes) > 0 {
		p.Confirm = "delete " + strconv.Itoa(len(st.Volumes)) + " volumes"
	}
	p.State = st
	return p, nil
}

func shortHex(id string) string {
	if len(id) > 7 && id[:7] == "sha256:" {
		id = id[7:]
	}
	return id[:min(len(id), 12)]
}

func runPrune(ctx context.Context, b Broker, kind string, p actions.Plan) (actions.Result, error) {
	res := actions.Result{Failed: []actions.Blocked{}, Blocked: []actions.Blocked{}}
	st, ok := p.State.(pruneState)
	if !ok || len(p.Items) == 0 {
		return res, nil
	}
	params := map[string]any{"kind": kind, "until": st.Until}
	if kind == "volumes" {
		params["volumes"] = st.Volumes
	}
	var out broker.PruneResult
	if err := b.Call(ctx, "docker.prune", params, &out); err != nil {
		if bl, ok := failure(actions.Item{ID: kind, Name: kind}, err); ok {
			res.Failed = append(res.Failed, bl)
			return res, nil
		}
		return res, brokerError(err)
	}
	res.Done = len(out.Deleted)
	// Volumes a container started using since the preview are kept.
	for _, n := range out.Skipped {
		res.Failed = append(res.Failed, actions.Blocked{Item: actions.Item{ID: n, Name: n}, Reason: "in-use"})
	}
	return res, nil
}

// humanBytes formats n like the UI does (binary units).
func humanBytes(n int64) string {
	units := []string{"B", "KiB", "MiB", "GiB", "TiB"}
	f, i := float64(n), 0
	for f >= 1024 && i < len(units)-1 {
		f /= 1024
		i++
	}
	if i == 0 || f >= 100 {
		return strconv.FormatFloat(f, 'f', 0, 64) + " " + units[i]
	}
	return strconv.FormatFloat(f, 'f', 1, 64) + " " + units[i]
}
