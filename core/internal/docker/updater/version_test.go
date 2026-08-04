package updater

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestNewestAllowedTagPolicies(t *testing.T) {
	t.Parallel()
	tags := []string{"v3.1.2", "3.2.0", "V4.0.0", "v5.0.0-rc.1", "latest", "v3.1.10"}
	current, ok := parseVersionTag("v3.1.1")
	if !ok {
		t.Fatal("current version was not parsed")
	}
	for _, test := range []struct{ policy, want string }{
		{VersionPolicyPatch, "v3.1.10"},
		{VersionPolicyMinor, "3.2.0"},
		{VersionPolicyMajor, "V4.0.0"},
	} {
		got, found := newestAllowedTag(current, tags, test.policy, false)
		if !found || got.raw != test.want {
			t.Errorf("policy %s = %q (%v), want %q", test.policy, got.raw, found, test.want)
		}
	}
	got, found := newestAllowedTag(current, tags, VersionPolicyMajor, true)
	if !found || got.raw != "v5.0.0-rc.1" {
		t.Fatalf("prerelease discovery = %q (%v)", got.raw, found)
	}
}

func TestCoarseMajorTagPatchPolicyStaysInMajor(t *testing.T) {
	t.Parallel()
	current, _ := parseVersionTag("v3")
	got, found := newestAllowedTag(current, []string{"v3.2.1", "v4.0.0"}, VersionPolicyPatch, false)
	if !found || got.raw != "v3.2.1" {
		t.Fatalf("coarse patch result = %q (%v)", got.raw, found)
	}
}

func TestRegistryTagsAnonymousBearerPagination(t *testing.T) {
	t.Parallel()
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		response := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"tags":["v1.0.0"]}`))}
		if request.URL.Path == "/token" {
			response.Body = io.NopCloser(strings.NewReader(`{"token":"public-token"}`))
			return response, nil
		}
		if request.Header.Get("Authorization") != "Bearer public-token" {
			response.StatusCode = http.StatusUnauthorized
			response.Header.Set("WWW-Authenticate", `Bearer realm="https://auth.test/token",service="test"`)
			return response, nil
		}
		if request.URL.Query().Get("last") == "v1.0.0" {
			response.Body = io.NopCloser(strings.NewReader(`{"tags":["v1.1.0"]}`))
			return response, nil
		}
		response.Header.Set("Link", `</v2/team/app/tags/list?n=100&last=v1.0.0>; rel="next"`)
		return response, nil
	})}
	tags, err := registryTagsWithClient(context.Background(), registryImageReference{registry: "registry.test", repo: "team/app", tag: "v1.0.0"}, client)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(tags, ",") != "v1.0.0,v1.1.0" {
		t.Fatalf("tags = %#v", tags)
	}
}

func TestRegistryVersionDiscoveryCachesCatalog(t *testing.T) {
	t.Parallel()
	discovery := NewRegistryVersionDiscovery(time.Hour)
	discovery.cache["registry-1.docker.io/library/alpine"] = versionCacheEntry{tags: []string{"v3.20.1"}, expiresAt: time.Now().Add(time.Hour)}
	results := discovery.Discover(context.Background(), "local", []VersionDiscoveryTarget{{ContainerID: "one", Image: "alpine:v3.20.0", Policy: VersionPolicyPatch}})
	if len(results) != 1 || !results[0].Available || results[0].LatestTag != "v3.20.1" {
		t.Fatalf("unexpected discovery: %#v", results)
	}
}
