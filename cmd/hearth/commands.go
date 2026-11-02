package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
)

type command func(ctx context.Context, c *client, asJSON bool, w io.Writer) error

var commands = map[string]command{
	"status": status, "ports": ports, "alerts": alertsCmd, "units": units, "containers": containers,
}

// raw prints an answer as it came, with a newline.
func raw(w io.Writer, body []byte) error {
	_, err := w.Write(append(bytes.TrimSpace(body), '\n'))
	return err
}

// table prints rows in columns, every cell cleaned.
func table(w io.Writer, header []string, rows [][]string) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, strings.Join(header, "\t"))
	for _, r := range rows {
		cells := make([]string, len(r))
		for i, cell := range r {
			cells[i] = strings.ReplaceAll(clean(cell), "\t", " ")
		}
		_, _ = fmt.Fprintln(tw, strings.Join(cells, "\t"))
	}
	return tw.Flush()
}

type alertEvent struct {
	RuleName  string `json:"rule_name"`
	Instance  string `json:"instance"`
	Severity  string `json:"severity"`
	StartedAt int64  `json:"started_at"`
	EndedAt   *int64 `json:"ended_at"`
}

func readAlerts(ctx context.Context, c *client) ([]byte, []alertEvent, error) {
	body, err := c.get(ctx, "/api/v1/alerts/events?limit=100")
	if err != nil {
		return nil, nil, err
	}
	var a struct {
		Events []alertEvent `json:"events"`
	}
	if err := json.Unmarshal(body, &a); err != nil {
		return nil, nil, fmt.Errorf("unexpected answer: %w", err)
	}
	return body, a.Events, nil
}

func when(ms int64) string { return time.UnixMilli(ms).Format("2006-01-02 15:04") }

func alertsCmd(ctx context.Context, c *client, asJSON bool, w io.Writer) error {
	body, events, err := readAlerts(ctx, c)
	if err != nil {
		return err
	}
	if asJSON {
		return raw(w, body)
	}
	rows := make([][]string, 0, len(events))
	for _, e := range events {
		state := "open"
		if e.EndedAt != nil {
			state = "ended " + when(*e.EndedAt)
		}
		rows = append(rows, []string{when(e.StartedAt), e.Severity, e.RuleName, e.Instance, state})
	}
	return table(w, []string{"STARTED", "SEVERITY", "ALERT", "ON", "STATE"}, rows)
}

func ports(ctx context.Context, c *client, asJSON bool, w io.Writer) error {
	body, err := c.get(ctx, "/api/v1/net/sockets")
	if err != nil {
		return err
	}
	if asJSON {
		return raw(w, body)
	}
	var s struct {
		Listeners []struct {
			Proto   string `json:"proto"`
			Address string `json:"address"`
			Port    int    `json:"port"`
			Scope   string `json:"scope"`
			User    string `json:"user"`
			Owner   *struct {
				Name      string `json:"name"`
				GroupName string `json:"group_name"`
			} `json:"owner"`
		} `json:"listeners"`
	}
	if err := json.Unmarshal(body, &s); err != nil {
		return fmt.Errorf("unexpected answer: %w", err)
	}
	rows := make([][]string, 0, len(s.Listeners))
	for _, l := range s.Listeners {
		owner := ""
		if l.Owner != nil {
			owner = l.Owner.GroupName
			if owner == "" {
				owner = l.Owner.Name
			}
		}
		rows = append(rows, []string{l.Proto, l.Address, strconv.Itoa(l.Port), l.Scope, l.User, owner})
	}
	return table(w, []string{"PROTO", "ADDRESS", "PORT", "SCOPE", "USER", "OWNER"}, rows)
}

func units(ctx context.Context, c *client, asJSON bool, w io.Writer) error {
	body, err := c.get(ctx, "/api/v1/services")
	if err != nil {
		return err
	}
	if asJSON {
		return raw(w, body)
	}
	var l struct {
		Units []struct {
			Name   string `json:"name"`
			Scope  string `json:"scope"`
			Active string `json:"active"`
			Sub    string `json:"sub"`
		} `json:"units"`
		Errors map[string]string `json:"errors"`
	}
	if err := json.Unmarshal(body, &l); err != nil {
		return fmt.Errorf("unexpected answer: %w", err)
	}
	rows := make([][]string, 0, len(l.Units))
	for _, u := range l.Units {
		rows = append(rows, []string{u.Name, u.Scope, u.Active, u.Sub})
	}
	if err := table(w, []string{"UNIT", "SCOPE", "ACTIVE", "SUB"}, rows); err != nil {
		return err
	}
	if len(l.Errors) > 0 {
		var why []string
		for _, e := range l.Errors {
			why = append(why, e)
		}
		slices.Sort(why)
		return fmt.Errorf("incomplete: %s", strings.Join(why, "; "))
	}
	return nil
}

func containers(ctx context.Context, c *client, asJSON bool, w io.Writer) error {
	body, err := c.get(ctx, "/api/v1/containers")
	if err != nil {
		return err
	}
	if asJSON {
		return raw(w, body)
	}
	var l struct {
		State      string `json:"state"`
		Containers []struct {
			Name   string `json:"name"`
			Image  string `json:"image"`
			State  string `json:"state"`
			Status string `json:"status"`
		} `json:"containers"`
	}
	if err := json.Unmarshal(body, &l); err != nil {
		return fmt.Errorf("unexpected answer: %w", err)
	}
	rows := make([][]string, 0, len(l.Containers))
	for _, ct := range l.Containers {
		rows = append(rows, []string{ct.Name, ct.Image, ct.State, ct.Status})
	}
	if err := table(w, []string{"NAME", "IMAGE", "STATE", "STATUS"}, rows); err != nil {
		return err
	}
	if len(rows) == 0 && l.State != "" && l.State != "ok" {
		return fmt.Errorf("docker: %s", l.State)
	}
	return nil
}

// statusFacts is `status --json`: the two answers it is built from.
type statusFacts struct {
	Metrics json.RawMessage `json:"metrics"`
	Alerts  json.RawMessage `json:"alerts"`
}

func status(ctx context.Context, c *client, asJSON bool, w io.Writer) error {
	metrics, err := c.get(ctx, "/api/v1/metrics/live?m=cpu.usage&m=load1&m=mem.used&m=swap.used&m=disk.used:")
	if err != nil {
		return err
	}
	alertBody, events, err := readAlerts(ctx, c)
	if err != nil {
		return err
	}
	if asJSON {
		out, err := json.Marshal(statusFacts{Metrics: metrics, Alerts: alertBody})
		if err != nil {
			return err
		}
		return raw(w, out)
	}
	var live struct {
		Series map[string][]*float64 `json:"series"`
	}
	if err := json.Unmarshal(metrics, &live); err != nil {
		return fmt.Errorf("unexpected answer: %w", err)
	}
	latest := func(name string) (float64, bool) {
		col := live.Series[name]
		for i := len(col) - 1; i >= 0; i-- {
			if col[i] != nil {
				return *col[i], true
			}
		}
		return 0, false
	}
	var rows [][]string
	if v, ok := latest("cpu.usage"); ok {
		rows = append(rows, []string{"CPU", fmt.Sprintf("%.1f%%", v)})
	}
	if v, ok := latest("load1"); ok {
		rows = append(rows, []string{"Load", fmt.Sprintf("%.2f", v)})
	}
	if v, ok := latest("mem.used"); ok {
		rows = append(rows, []string{"Memory used", size(v)})
	}
	if v, ok := latest("swap.used"); ok {
		rows = append(rows, []string{"Swap used", size(v)})
	}
	var mounts []string
	for name := range live.Series {
		if m, ok := strings.CutPrefix(name, "disk.used:"); ok {
			mounts = append(mounts, m)
		}
	}
	slices.Sort(mounts)
	for _, m := range mounts {
		if v, ok := latest("disk.used:" + m); ok {
			rows = append(rows, []string{"Used on " + m, size(v)})
		}
	}
	open := 0
	for _, e := range events {
		if e.EndedAt == nil {
			open++
		}
	}
	rows = append(rows, []string{"Open alerts", strconv.Itoa(open)})
	return table(w, []string{"WHAT", "NOW"}, rows)
}

// size prints bytes in binary units.
func size(v float64) string {
	const unit = 1024.0
	names := []string{"B", "KiB", "MiB", "GiB", "TiB", "PiB"}
	i := 0
	for v >= unit && i < len(names)-1 {
		v /= unit
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%.0f B", v)
	}
	return fmt.Sprintf("%.1f %s", v, names[i])
}
