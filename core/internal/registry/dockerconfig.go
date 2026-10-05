package registry

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/rs/zerolog/log"
)

// dockerConfig is the Docker CLI configuration Dockman owns, the one the
// entrypoint points DOCKER_CONFIG at. Writing the logins there is what makes
// `compose pull`, `compose up` and every build authenticate: those run the
// Docker CLI, which reads this file and nothing Dockman could pass it.
//
// Entries for registries Dockman does not know are left untouched: an
// operator may have run `docker login` from the host shell, and this file is
// shared with that.
type dockerConfig struct{ dir string }

func newDockerConfig(dir string) *dockerConfig { return &dockerConfig{dir: dir} }

func (c *dockerConfig) path() string { return filepath.Join(c.dir, "config.json") }

func (c *dockerConfig) write(credentials map[string]Auth) error {
	if c.dir == "" {
		return nil
	}
	document, auths, err := c.load()
	if err != nil {
		return err
	}
	for _, auth := range credentials {
		key := ConfigKey(auth.Registry)
		entry := map[string]any{}
		if existing, ok := auths[key].(map[string]any); ok {
			entry = existing
		}
		entry["auth"] = base64.StdEncoding.EncodeToString([]byte(auth.Username + ":" + auth.Secret))
		delete(entry, "identitytoken")
		auths[key] = entry
	}
	document["auths"] = auths
	return c.save(document)
}

func (c *dockerConfig) remove(host string) error {
	if c.dir == "" {
		return nil
	}
	document, auths, err := c.load()
	if err != nil {
		return err
	}
	if _, ok := auths[ConfigKey(host)]; !ok {
		return nil
	}
	delete(auths, ConfigKey(host))
	document["auths"] = auths
	return c.save(document)
}

func (c *dockerConfig) load() (map[string]any, map[string]any, error) {
	document := map[string]any{}
	raw, err := os.ReadFile(c.path())
	switch {
	case err == nil:
		if err := json.Unmarshal(raw, &document); err != nil {
			// A file the CLI itself cannot read would break every command;
			// say so instead of overwriting what might be the operator's.
			return nil, nil, fmt.Errorf("%s is not valid JSON: %w", c.path(), err)
		}
	case os.IsNotExist(err):
	default:
		return nil, nil, err
	}
	auths := map[string]any{}
	if existing, ok := document["auths"].(map[string]any); ok {
		auths = existing
	}
	return document, auths, nil
}

func (c *dockerConfig) save(document map[string]any) error {
	if err := os.MkdirAll(c.dir, 0o700); err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return err
	}
	// Written whole, through a temporary file: a Compose command reading it
	// at the same moment sees the old file or the new one, never half of one.
	temporary, err := os.CreateTemp(c.dir, ".config-*.json")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer func() { _ = os.Remove(name) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(append(encoded, '\n')); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, c.path()); err != nil {
		return err
	}
	log.Debug().Str("path", c.path()).Int("registries", countAuths(document)).Msg("Docker CLI credentials written")
	return nil
}

func countAuths(document map[string]any) int {
	auths, ok := document["auths"].(map[string]any)
	if !ok {
		return 0
	}
	return len(auths)
}

// configuredRegistries lists the hosts present in the file, for diagnostics
// and tests; sorted so the output never depends on map ordering.
func (c *dockerConfig) configuredRegistries() ([]string, error) {
	_, auths, err := c.load()
	if err != nil {
		return nil, err
	}
	hosts := make([]string, 0, len(auths))
	for key := range auths {
		hosts = append(hosts, key)
	}
	sort.Strings(hosts)
	return hosts, nil
}

// DefaultConfigDir is the Docker CLI configuration Dockman writes to: the one
// the entrypoint points DOCKER_CONFIG at, so the Compose commands read what
// is written here. Outside a container, Docker's own default is used.
func DefaultConfigDir(configDir string) string {
	if fromEnv := os.Getenv("DOCKER_CONFIG"); fromEnv != "" {
		return fromEnv
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".docker")
	}
	return filepath.Join(configDir, "docker-cli")
}
