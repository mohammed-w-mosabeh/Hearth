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

func TestContainerImages(t *testing.T) {
	b := &fakeBroker{answers: map[string]string{
		"docker.containers.list": `[{"Id":"0123456789abcdef","Names":["/web"],"Image":"nginx:1"},{"Id":"fedcba9876543210","Names":[],"Image":"redis"}]`,
	}}
	got, err := ContainerImages(context.Background(), b)
	if err != nil || len(got) != 2 || got["web"] != "nginx:1" || got["fedcba987654"] != "redis" {
		t.Fatalf("%v %+v", err, got)
	}
	none := &fakeBroker{errs: map[string]error{"docker.containers.list": broker.ErrNoDocker}}
	if got, err := ContainerImages(context.Background(), none); err != nil || got == nil || len(got) != 0 {
		t.Errorf("no docker: %v %+v", err, got)
	}
	down := &fakeBroker{errs: map[string]error{"docker.containers.list": errors.New("broker down")}}
	if _, err := ContainerImages(context.Background(), down); err == nil {
		t.Error("an unreadable Docker is not an empty one")
	}
}
