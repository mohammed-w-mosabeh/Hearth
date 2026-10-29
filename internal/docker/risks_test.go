package docker

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/mohammed-w-mosabeh/hearth/internal/broker"
	"github.com/mohammed-w-mosabeh/hearth/internal/posture"
)

func TestRisks(t *testing.T) {
	b := &fakeBroker{answers: map[string]string{
		"docker.containers.list":    `[{"Id":"0123456789abcdef","Names":["/agent"],"State":"running"},{"Id":"fedcba9876543210","Names":[],"State":"exited"}]`,
		"docker.containers.inspect": `{"inspect":{},"risks":["privileged"]}`,
	}}
	got, err := Risks(context.Background(), b)
	if err != nil || len(got) != 2 {
		t.Fatalf("%v %+v", err, got)
	}
	if got[0].Name != "agent" || !got[0].Running || got[0].Risks[0] != "privileged" || got[1].Name != "fedcba987654" || got[1].Running {
		t.Errorf("%+v", got)
	}
	if !strings.Contains(strings.Join(b.calls, "\n"), `docker.containers.inspect {"id":"fedcba9876543210"}`) {
		t.Errorf("calls %v", b.calls)
	}
	none := &fakeBroker{errs: map[string]error{"docker.containers.list": broker.ErrNoDocker}}
	if _, err := Risks(context.Background(), none); !errors.Is(err, posture.ErrNoDocker) {
		t.Errorf("no docker: %v", err)
	}
}
