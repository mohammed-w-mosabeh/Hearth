package docker

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
	"github.com/mohammed-w-mosabeh/hearth/internal/broker"
	"github.com/mohammed-w-mosabeh/hearth/internal/safeexec"
)

type updateBroker struct {
	containers string
	sent       *broker.ComposeUpdateParams
	err        error
}

func (b *updateBroker) Call(_ context.Context, method string, params, out any) error {
	switch method {
	case "docker.containers.list":
		return json.Unmarshal([]byte(b.containers), out)
	case "docker.images.inspect":
		return json.Unmarshal([]byte(`{"RepoDigests":["nginx@sha256:aaaa"]}`), out)
	case "compose.update":
		p := params.(broker.ComposeUpdateParams)
		b.sent = &p
		return b.err
	}
	return errors.New("unexpected " + method)
}

func (*updateBroker) Stream(context.Context, string, any, func(json.RawMessage) error) error {
	return errors.New("unexpected")
}

type fakeTags struct{ status, remote string }

func (f fakeTags) Check(context.Context, []string, []string) []TagUpdate {
	return []TagUpdate{{Status: f.status, Remote: f.remote}}
}

const oneContainer = `[{"ImageID":"sha256:1","Labels":{"com.docker.compose.project":"app","com.docker.compose.service":"web",
	"com.docker.compose.project.working_dir":"/home/me/app","com.docker.compose.project.config_files":"/home/me/app/compose.yaml"}}]`

func rendered(stdout string, code int) Runner {
	return func(_ context.Context, name string, args ...string) (safeexec.Result, error) {
		if err := safeexec.Check(name, args...); err != nil {
			return safeexec.Result{}, err
		}
		return safeexec.Result{Stdout: []byte(stdout), ExitCode: code}, nil
	}
}

const plain = `{"name":"app","services":{"web":{"image":"nginx:1.27"}}}`

func TestPlanShowsWhichServicesChangeDigest(t *testing.T) {
	b := &updateBroker{containers: oneContainer}
	for status, want := range map[string]string{UpdateNewer: "new digest abcdefabcdef", UpdateCurrent: "digest unchanged", UpdateUnknown: "digest unknown", UpdateLocal: "built here"} {
		p, err := planUpdate(context.Background(), b, fakeTags{status, "sha256:abcdefabcdefabcdef"}, rendered(plain, 0), "app")
		if err != nil || len(p.Items) != 1 || len(p.Blocked) != 0 {
			t.Fatalf("%s: %v %+v", status, err, p)
		}
		if !strings.Contains(p.Items[0].Detail, want) || !strings.Contains(p.Items[0].Detail, "nginx:1.27") {
			t.Errorf("%s: %q", status, p.Items[0].Detail)
		}
	}
}

func TestPlanRefusesAndLeavesNothingToRun(t *testing.T) {
	two := strings.Replace(plain, `"nginx:1.27"}`, `"nginx:1.27"},"db":{"image":"postgres"}`, 1)
	for name, tc := range map[string]struct {
		containers, out string
		code            int
		want            string
	}{
		"risky":        {oneContainer, `{"name":"app","services":{"web":{"image":"nginx","privileged":true}}}`, 0, "web: privileged"},
		"provider":     {oneContainer, `{"name":"app","services":{"web":{"image":"x","provider":{"type":"y"}}}}`, 0, "web: provider"},
		"cannot read":  {oneContainer, "", 1, "could not be rendered"},
		"new service":  {oneContainer, two, 0, "db: new service"},
		"no project":   {`[]`, plain, 0, "app"},
		"no files":     {`[{"Labels":{"com.docker.compose.project":"app","com.docker.compose.service":"web"}}]`, plain, 0, "not known"},
		"not rendered": {oneContainer, "not json", 0, "not one JSON"},
	} {
		p, err := planUpdate(context.Background(), &updateBroker{containers: tc.containers}, fakeTags{}, rendered(tc.out, tc.code), "app")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(p.Items) != 0 || p.State != nil || len(p.Blocked) == 0 || !strings.Contains(p.Blocked[0].Name, tc.want) {
			t.Errorf("%s: %+v", name, p)
		}
	}
}

func TestHearthsLabelsNameTheRealFiles(t *testing.T) {
	var got []string
	run := func(_ context.Context, _ string, args ...string) (safeexec.Result, error) {
		got = args
		return safeexec.Result{Stdout: []byte(plain)}, nil
	}
	c := `[{"Labels":{"com.docker.compose.project":"app","com.docker.compose.service":"web",
		"com.docker.compose.project.working_dir":"/var/lib/hearth","com.docker.compose.project.config_files":"/var/lib/hearth/compose.json",
		"org.hearth.compose.dir":"/home/me/app","org.hearth.compose.files":"/home/me/app/a.yaml,/home/me/app/b.yaml"}}]`
	if _, err := planUpdate(context.Background(), &updateBroker{containers: c}, fakeTags{status: UpdateCurrent}, run, "app"); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(got, " ")
	if !strings.Contains(joined, "--project-directory /home/me/app --file /home/me/app/a.yaml --file /home/me/app/b.yaml") {
		t.Errorf("args: %s", joined)
	}
}

func TestRunSendsWhatThePreviewShowed(t *testing.T) {
	b := &updateBroker{containers: oneContainer}
	reg := actions.NewRegistry(nil)
	RegisterUpdate(reg, b, fakeTags{status: UpdateCurrent}, rendered(plain, 0))
	p, err := planUpdate(context.Background(), b, fakeTags{status: UpdateCurrent}, rendered(plain, 0), "app")
	if err != nil {
		t.Fatal(err)
	}
	st := p.State.(updateState)
	if string(st.Params.Config) != plain || st.Params.Dir != "/home/me/app" || st.Params.Files[0] != "/home/me/app/compose.yaml" {
		t.Errorf("state: %+v", st.Params)
	}
	if _, err := broker.CheckComposeUpdate(st.Params); err != nil {
		t.Errorf("the broker would refuse what the preview allowed: %v", err)
	}
}
