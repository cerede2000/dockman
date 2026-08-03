package updater

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return fn(req) }

func TestParseRegistryImageReference(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		image               string
		registry, repo, tag string
	}{
		{"alpine", "registry-1.docker.io", "library/alpine", "latest"},
		{"library/nginx:stable", "registry-1.docker.io", "library/nginx", "stable"},
		{"ghcr.io/example/app:v1", "ghcr.io", "example/app", "v1"},
		{"registry.example:5000/team/app", "registry.example:5000", "team/app", "latest"},
	} {
		got, err := parseRegistryImageReference(test.image)
		if err != nil {
			t.Fatalf("parse %q: %v", test.image, err)
		}
		if got.registry != test.registry || got.repo != test.repo || got.tag != test.tag {
			t.Errorf("parse %q = %#v", test.image, got)
		}
	}
}

func TestRegistryManifestDigestAnonymousBearer(t *testing.T) {
	t.Parallel()
	const digest = "0123456789abcdef"
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		response := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}
		switch r.URL.Path {
		case "/token":
			if r.URL.Query().Get("scope") != "repository:team/app:pull" {
				t.Errorf("unexpected scope %q", r.URL.Query().Get("scope"))
			}
			response.Body = io.NopCloser(strings.NewReader(`{"token":"public-token"}`))
		case "/v2/team/app/manifests/latest":
			if r.Header.Get("Authorization") != "Bearer public-token" {
				response.StatusCode = http.StatusUnauthorized
				response.Header.Set("WWW-Authenticate", `Bearer realm="https://auth.test/token",service="test"`)
				return response, nil
			}
			response.Header.Set("Docker-Content-Digest", "sha256:"+digest)
		default:
			response.StatusCode = http.StatusNotFound
		}
		return response, nil
	})}

	got, err := registryManifestDigestWithClient(context.Background(), "registry.test/team/app:latest", client)
	if err != nil {
		t.Fatal(err)
	}
	if got != digest {
		t.Fatalf("digest = %q, want %q", got, digest)
	}
}

func TestAnonymousBearerRejectsBasic(t *testing.T) {
	t.Parallel()
	_, err := anonymousBearerToken(context.Background(), http.DefaultClient, `Basic realm="registry"`, "team/app")
	if err == nil || !strings.Contains(err.Error(), "only anonymous Bearer") {
		t.Fatalf("unexpected error: %v", err)
	}
}
