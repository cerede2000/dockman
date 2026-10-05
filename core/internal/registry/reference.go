// Package registry holds the credentials Dockman uses to reach private
// container registries, and hands them to everything that pulls: the Compose
// and Docker command lines, the pulls Dockman performs through the Docker API,
// and the registry queries behind update detection.
package registry

import (
	"fmt"
	"strings"
)

// DockerHubRegistry is the host every Docker Hub spelling resolves to. A
// credential is looked up by the host of the image reference, so the
// normalisation has to be the same everywhere or a stored login would simply
// not be found.
const DockerHubRegistry = "registry-1.docker.io"

// Reference is an image reference split the way a registry request needs it.
type Reference struct {
	Registry string
	Repo     string
	Tag      string
}

// ParseImage splits an image reference. The rules are Docker's: a first
// segment carrying a dot, a colon or named localhost is a registry, anything
// else belongs to Docker Hub, where a single-segment repository lives under
// library/.
func ParseImage(image string) (Reference, error) {
	image = strings.TrimSpace(image)
	if image == "" || strings.Contains(image, "://") || strings.Contains(image, "@") {
		return Reference{}, fmt.Errorf("unsupported image reference %q", image)
	}
	registry := DockerHubRegistry
	remainder := image
	if slash := strings.IndexByte(image, '/'); slash >= 0 {
		first := image[:slash]
		if strings.Contains(first, ".") || strings.Contains(first, ":") || first == "localhost" {
			registry = first
			remainder = image[slash+1:]
		}
	}
	registry = NormalizeHost(registry)
	lastSlash := strings.LastIndexByte(remainder, '/')
	lastColon := strings.LastIndexByte(remainder, ':')
	tag := "latest"
	if lastColon > lastSlash {
		tag = remainder[lastColon+1:]
		remainder = remainder[:lastColon]
	}
	if registry == DockerHubRegistry && !strings.Contains(remainder, "/") {
		remainder = "library/" + remainder
	}
	if registry == "" || remainder == "" || tag == "" {
		return Reference{}, fmt.Errorf("invalid image reference %q", image)
	}
	return Reference{Registry: registry, Repo: remainder, Tag: tag}, nil
}

// NormalizeHost is what a credential is stored under. It accepts what an
// operator would paste - a URL, a trailing slash, mixed case, any of Docker
// Hub's names - and answers with the host the image references resolve to.
func NormalizeHost(host string) string {
	host = strings.TrimSpace(host)
	host = strings.TrimPrefix(strings.TrimPrefix(host, "https://"), "http://")
	host = strings.TrimSuffix(host, "/")
	if slash := strings.IndexByte(host, '/'); slash >= 0 {
		host = host[:slash]
	}
	host = strings.ToLower(host)
	switch host {
	case "docker.io", "index.docker.io", "registry.hub.docker.com", "hub.docker.com", "registry.docker.io":
		return DockerHubRegistry
	default:
		return host
	}
}

// ConfigKey is the key a Docker CLI configuration file uses for a registry.
// Docker Hub is the exception every client special-cases, and a config.json
// written with any other spelling is ignored by the CLI.
func ConfigKey(host string) string {
	if NormalizeHost(host) == DockerHubRegistry {
		return "https://index.docker.io/v1/"
	}
	return NormalizeHost(host)
}
