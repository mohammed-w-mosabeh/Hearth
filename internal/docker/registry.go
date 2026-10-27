package docker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// "Update available" (SPEC 5): the digest a registry serves for a tag,
// compared with the digest the image was pulled as. On request only;
// HTTPS only; anonymous pull tokens only (the user's Docker credentials
// are never read); manifests only, bodies bounded (SECURITY S21a).

// Update states of one tag.
const (
	UpdateCurrent = "current" // the registry serves what is here
	UpdateNewer   = "update"  // the registry serves another digest
	UpdateLocal   = "local"   // built here or never pulled: nothing to compare
	UpdateUnknown = "unknown" // the registry could not be asked (Error says why)
)

// TagUpdate is the update check of one tag.
type TagUpdate struct {
	Tag    string `json:"tag"`
	Status string `json:"status"`
	Local  string `json:"local,omitempty"`
	Remote string `json:"remote,omitempty"`
	Error  string `json:"error,omitempty"`
}

// Registry asks registries for manifest digests.
type Registry struct {
	Client *http.Client
	// Base maps a registry host to its base URL; default "https://<host>".
	Base func(host string) string
}

// NewRegistry returns a Registry with a 10 s timeout that follows
// redirects only within a host.
func NewRegistry() *Registry {
	return &Registry{Client: &http.Client{
		Timeout: 10 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > 2 || req.URL.Host != via[0].URL.Host || req.URL.Scheme != "https" {
				return errors.New("redirect refused")
			}
			return nil
		},
	}}
}

// ref is a normalized image reference.
type ref struct {
	Host, Repo, Tag string
}

func (r ref) name() string { return r.Host + "/" + r.Repo }

var (
	repoRe = regexp.MustCompile(`^[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*(?:/[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*)*$`)
	tagRe  = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)
	hostRe = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?(?:\.[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?)*(?::[0-9]{1,5})?$`)
	// digestRe is a sha256 content digest.
	digestRe = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
)

// parseRef normalizes "nginx", "nginx:1", "ghcr.io/a/b:v" or
// "nginx@sha256:..." (tag "" then) the way Docker does.
func parseRef(s string) (ref, error) {
	name, tag, pinned := s, "latest", false
	if i := strings.Index(s, "@"); i >= 0 {
		name, pinned = s[:i], true
		if !digestRe.MatchString(s[i+1:]) {
			return ref{}, fmt.Errorf("not an image reference: %q", s)
		}
	}
	if i := strings.LastIndex(name, ":"); i > strings.LastIndex(name, "/") {
		name, tag = name[:i], name[i+1:]
		if !tagRe.MatchString(tag) {
			return ref{}, fmt.Errorf("not an image reference: %q", s)
		}
	}
	if pinned {
		tag = ""
	}
	host, repo := "docker.io", name
	if i := strings.Index(name, "/"); i >= 0 {
		first := name[:i]
		if strings.ContainsAny(first, ".:") || first == "localhost" {
			host, repo = first, name[i+1:]
		}
	}
	if host == "docker.io" && !strings.Contains(repo, "/") {
		repo = "library/" + repo
	}
	if !hostRe.MatchString(host) || !repoRe.MatchString(repo) {
		return ref{}, fmt.Errorf("not an image reference: %q", s)
	}
	return ref{Host: host, Repo: repo, Tag: tag}, nil
}

// localDigest finds the digest an image was pulled as for r's repository.
func localDigest(r ref, repoDigests []string) string {
	for _, rd := range repoDigests {
		name, dg, ok := strings.Cut(rd, "@")
		if !ok || !digestRe.MatchString(dg) {
			continue
		}
		if l, err := parseRef(name); err == nil && l.name() == r.name() {
			return dg
		}
	}
	return ""
}

// Check compares each tag with its registry.
func (g *Registry) Check(ctx context.Context, tags, repoDigests []string) []TagUpdate {
	out := make([]TagUpdate, 0, len(tags))
	for _, t := range tags {
		u := TagUpdate{Tag: t, Status: UpdateUnknown}
		r, err := parseRef(t)
		switch {
		case err != nil:
			u.Error = err.Error()
		case r.Tag == "":
			u.Status, u.Error = UpdateLocal, "pinned to a digest"
		default:
			u.Local = localDigest(r, repoDigests)
			if u.Local == "" {
				u.Status = UpdateLocal
				break
			}
			u.Remote, err = g.digest(ctx, r)
			switch {
			case err != nil:
				u.Error = err.Error()
			case u.Remote == u.Local:
				u.Status = UpdateCurrent
			default:
				u.Status = UpdateNewer
			}
		}
		out = append(out, u)
	}
	return out
}

// manifestTypes are the manifests asked for: an index first, as a
// multi-platform image pulled by tag is recorded with the index digest.
var manifestTypes = strings.Join([]string{
	"application/vnd.oci.image.index.v1+json",
	"application/vnd.docker.distribution.manifest.list.v2+json",
	"application/vnd.docker.distribution.manifest.v2+json",
	"application/vnd.oci.image.manifest.v1+json",
}, ", ")

const maxManifest = 4 << 20

// digest returns the registry's digest for r's tag.
func (g *Registry) digest(ctx context.Context, r ref) (string, error) {
	base := "https://" + registryHost(r.Host)
	if g.Base != nil {
		base = g.Base(r.Host)
	}
	u := base + "/v2/" + r.Repo + "/manifests/" + r.Tag
	token := ""
	for attempt := 0; ; attempt++ {
		dg, challenge, err := g.manifest(ctx, http.MethodHead, u, token)
		if err == nil && dg == "" {
			dg, _, err = g.manifest(ctx, http.MethodGet, u, token) // no digest header on HEAD
		}
		if challenge == "" || attempt > 0 {
			return dg, err
		}
		if token, err = g.token(ctx, challenge, r.Repo); err != nil {
			return "", err
		}
	}
}

// registryHost is where a registry's API lives.
func registryHost(host string) string {
	if host == "docker.io" {
		return "registry-1.docker.io"
	}
	return host
}

// manifest asks for a manifest; on 401 it returns the Bearer challenge.
func (g *Registry) manifest(ctx context.Context, method, u, token string) (digest, challenge string, err error) {
	req, err := http.NewRequestWithContext(ctx, method, u, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Accept", manifestTypes)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := g.Client.Do(req)
	if err != nil {
		return "", "", errors.New("the registry did not answer")
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode == http.StatusUnauthorized && token == "":
		c := resp.Header.Get("WWW-Authenticate")
		if !strings.HasPrefix(strings.ToLower(c), "bearer ") {
			return "", "", errors.New("the registry wants credentials")
		}
		return "", c, nil
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return "", "", errors.New("the registry wants credentials (private image?)")
	case resp.StatusCode == http.StatusNotFound:
		return "", "", errors.New("the registry has no such tag")
	case resp.StatusCode == http.StatusTooManyRequests:
		return "", "", errors.New("the registry's rate limit was reached; try later")
	case resp.StatusCode != http.StatusOK:
		return "", "", fmt.Errorf("the registry answered %s", resp.Status)
	}
	if dg := resp.Header.Get("Docker-Content-Digest"); digestRe.MatchString(dg) {
		return dg, "", nil
	}
	if method == http.MethodHead {
		return "", "", nil
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(resp.Body, maxManifest+1))
	if err != nil || n > maxManifest {
		return "", "", errors.New("unexpected answer from the registry")
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), "", nil
}

var challengeRe = regexp.MustCompile(`(\w+)="([^"]*)"`)

// token fetches an anonymous pull token for repo from a Bearer challenge.
func (g *Registry) token(ctx context.Context, challenge, repo string) (string, error) {
	params := map[string]string{}
	for _, m := range challengeRe.FindAllStringSubmatch(challenge, -1) {
		params[strings.ToLower(m[1])] = m[2]
	}
	realm, err := url.Parse(params["realm"])
	if err != nil || realm.Scheme != "https" || realm.Host == "" || realm.User != nil {
		return "", errors.New("the registry's token service is not HTTPS")
	}
	q := realm.Query()
	if s := params["service"]; s != "" {
		q.Set("service", s)
	}
	q.Set("scope", "repository:"+repo+":pull")
	realm.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, realm.String(), nil)
	if err != nil {
		return "", err
	}
	resp, err := g.Client.Do(req)
	if err != nil {
		return "", errors.New("the registry's token service did not answer")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", errors.New("the registry refused an anonymous token (private image?)")
	}
	var tok struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&tok); err != nil {
		return "", errors.New("unexpected answer from the token service")
	}
	if tok.Token == "" {
		tok.Token = tok.AccessToken
	}
	if tok.Token == "" {
		return "", errors.New("unexpected answer from the token service")
	}
	return tok.Token, nil
}
