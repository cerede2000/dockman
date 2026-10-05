package registry

import "testing"

// A credential is found by the host an image reference resolves to. Every
// spelling of Docker Hub has to land on the same host, or a login stored for
// "docker.io" would never be used for "nginx:alpine".
func TestAnImageReferenceResolvesToTheHostACredentialIsStoredUnder(t *testing.T) {
	cases := []struct {
		image    string
		registry string
		repo     string
		tag      string
	}{
		{"nginx", DockerHubRegistry, "library/nginx", "latest"},
		{"nginx:1.27-alpine", DockerHubRegistry, "library/nginx", "1.27-alpine"},
		{"cerede2000/dockman:integration", DockerHubRegistry, "cerede2000/dockman", "integration"},
		{"docker.io/library/busybox:1.37", DockerHubRegistry, "library/busybox", "1.37"},
		{"index.docker.io/library/busybox", DockerHubRegistry, "library/busybox", "latest"},
		{"ghcr.io/cerede2000/dockman:integration", "ghcr.io", "cerede2000/dockman", "integration"},
		{"registry.example.com:5000/team/app:1.2.3", "registry.example.com:5000", "team/app", "1.2.3"},
		{"localhost:5000/app", "localhost:5000", "app", "latest"},
		{"GHCR.IO/Team/App:Tag", "ghcr.io", "Team/App", "Tag"},
	}
	for _, test := range cases {
		ref, err := ParseImage(test.image)
		if err != nil {
			t.Fatalf("%s: %v", test.image, err)
		}
		if ref.Registry != test.registry || ref.Repo != test.repo || ref.Tag != test.tag {
			t.Errorf("%s -> %+v, want %s %s %s", test.image, ref, test.registry, test.repo, test.tag)
		}
	}
}

func TestAnUnusableReferenceIsRefused(t *testing.T) {
	for _, image := range []string{"", "   ", "https://ghcr.io/team/app", "ghcr.io/team/app@sha256:abc"} {
		if _, err := ParseImage(image); err == nil {
			t.Errorf("%q was accepted", image)
		}
	}
}

// The host is what an operator pastes: a URL, a trailing slash, any case.
func TestTheStoredHostIsWhatAnOperatorPastes(t *testing.T) {
	cases := map[string]string{
		"ghcr.io":                     "ghcr.io",
		"https://ghcr.io":             "ghcr.io",
		"https://ghcr.io/":            "ghcr.io",
		"GHCR.io":                     "ghcr.io",
		"docker.io":                   DockerHubRegistry,
		"https://index.docker.io/v1/": DockerHubRegistry,
		"registry.hub.docker.com":     DockerHubRegistry,
		"registry.example.com:5000":   "registry.example.com:5000",
		"  nas.lan:5000  ":            "nas.lan:5000",
	}
	for input, want := range cases {
		if got := NormalizeHost(input); got != want {
			t.Errorf("NormalizeHost(%q) = %q, want %q", input, got, want)
		}
	}
}

// The Docker CLI reads Docker Hub under one key and ignores any other
// spelling, so a config.json written with the resolved host would silently
// not authenticate.
func TestTheDockerConfigurationKeyOfDockerHubIsTheOneTheCLIReads(t *testing.T) {
	if got := ConfigKey("docker.io"); got != "https://index.docker.io/v1/" {
		t.Fatalf("ConfigKey(docker.io) = %q", got)
	}
	if got := ConfigKey("ghcr.io"); got != "ghcr.io" {
		t.Fatalf("ConfigKey(ghcr.io) = %q", got)
	}
}
