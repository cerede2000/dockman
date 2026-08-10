package compose

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// ServicePlan is what the compose file currently asks for one service,
// independent of whatever happens to be running.
type ServicePlan struct {
	// ConfigHash is the value Compose writes in the
	// com.docker.compose.config-hash label when it creates the container. A
	// running container whose label differs was created from a different
	// manifest, which is exactly the signal for "the compose file changed".
	ConfigHash string
	// Buildable marks a service with a build section. Its image is produced
	// locally rather than pulled, and Compose deliberately clears the build
	// section before hashing, so neither the build configuration nor its
	// context leaves any trace in ConfigHash. Such a service must always go
	// back through Compose, which rebuilds it and decides for itself.
	Buildable bool
	// Replicas is how many containers the manifest asks for. Compose also
	// strips scale and deploy.replicas before hashing, so a stack scaled from
	// one to three would otherwise show an unchanged hash and never grow.
	// Zero means the manifest did not state it plainly, which sends the
	// service back to Compose rather than guessing.
	Replicas int
}

// ProjectPlan reports, per service name, what the compose file expects right
// now. Both queries are pure model resolution: no daemon call, no container
// touched.
func (c *Service) ProjectPlan(ctx context.Context, filename string) (map[string]ServicePlan, error) {
	hashes, err := c.serviceConfigHashes(ctx, filename)
	if err != nil {
		return nil, err
	}
	shape, err := c.serviceShapes(ctx, filename)
	if err != nil {
		return nil, err
	}
	plan := make(map[string]ServicePlan, len(hashes))
	for name, hash := range hashes {
		service := shape[name]
		service.ConfigHash = hash
		plan[name] = service
	}
	return plan, nil
}

// serviceConfigHashes asks Compose for the config hash of every service, the
// same value it stamps on the containers it creates.
func (c *Service) serviceConfigHashes(ctx context.Context, filename string) (map[string]string, error) {
	out, err := c.captureCmd(ctx, filename, func(cmdList []string) []string {
		return append(cmdList, "config", "--hash", "*")
	}, nil)
	if err != nil {
		return nil, fmt.Errorf("read service config hashes: %w", err)
	}
	return parseConfigHashes(string(out))
}

func parseConfigHashes(out string) (map[string]string, error) {
	hashes := make(map[string]string)
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		// "<service> <hash>". Counting fields is not enough: Compose writes
		// warnings on the same stream, and a two-word one ("WARN[0000]
		// something") would be recorded as a service that does not exist -
		// which then gets passed to `compose up`, where it fails the whole
		// update. The hash is a SHA-256 digest, so require exactly that.
		if len(fields) != 2 || !isServiceConfigHash(fields[1]) {
			continue
		}
		hashes[fields[0]] = fields[1]
	}
	if len(hashes) == 0 {
		return nil, fmt.Errorf("compose reported no service config hash")
	}
	return hashes, nil
}

const configHashLength = 64

func isServiceConfigHash(value string) bool {
	if len(value) != configHashLength {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

// serviceShapes reports what the config hash deliberately leaves out: whether
// a service is built locally, and how many containers it asks for.
//
// --no-interpolate leaves ${VAR} references unexpanded and --no-env-resolution
// leaves env_file alone, so the decoded model never holds the values of the
// inline SOPS secrets the stack may be running with. Only two facts per
// service are kept; the raw model is zeroed on the way out.
func (c *Service) serviceShapes(ctx context.Context, filename string) (map[string]ServicePlan, error) {
	out, err := c.captureCmd(ctx, filename, func(cmdList []string) []string {
		return append(cmdList,
			"config", "--format", "json",
			"--no-interpolate", "--no-env-resolution",
		)
	}, nil)
	if err != nil {
		return nil, fmt.Errorf("read compose model: %w", err)
	}
	defer clear(out)
	return parseServiceShapes(out)
}

func parseServiceShapes(model []byte) (map[string]ServicePlan, error) {
	// Compose prints the JSON document on its own, but a warning can land in
	// front of it, so start at the opening brace.
	start := 0
	for start < len(model) && model[start] != '{' {
		start++
	}
	var decoded struct {
		Services map[string]struct {
			Build  json.RawMessage `json:"build"`
			Scale  json.RawMessage `json:"scale"`
			Deploy *struct {
				Replicas json.RawMessage `json:"replicas"`
			} `json:"deploy"`
		} `json:"services"`
	}
	if err := json.Unmarshal(model[start:], &decoded); err != nil {
		return nil, fmt.Errorf("decode compose model: %w", err)
	}
	shapes := make(map[string]ServicePlan, len(decoded.Services))
	for name, service := range decoded.Services {
		shape := ServicePlan{Buildable: presentJSONValue(service.Build), Replicas: 1}
		// scale wins over deploy.replicas, as it does in Compose itself.
		replicas := service.Scale
		if !presentJSONValue(replicas) && service.Deploy != nil {
			replicas = service.Deploy.Replicas
		}
		if presentJSONValue(replicas) {
			shape.Replicas = parseReplicaCount(replicas)
		}
		shapes[name] = shape
	}
	return shapes, nil
}

func presentJSONValue(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	return trimmed != "" && trimmed != "null"
}

// parseReplicaCount returns 0 for anything it cannot read as a plain count -
// an uninterpolated ${REPLICAS} in particular. Callers treat 0 as "ask
// Compose", which is the safe reading of an unknown replica count.
func parseReplicaCount(raw json.RawMessage) int {
	value := strings.Trim(strings.TrimSpace(string(raw)), `"`)
	count, err := strconv.Atoi(value)
	if err != nil || count < 0 {
		return 0
	}
	return count
}
