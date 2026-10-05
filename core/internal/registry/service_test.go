package registry

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/RA341/dockman/internal/database"
	"github.com/RA341/dockman/pkg/credvault"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// A service on a real database: database.New applies every embedded
// migration, so these tests also prove the table exists where GORM expects it.
func newTestService(t *testing.T) (*Service, *gorm.DB, string) {
	t.Helper()
	db := database.New(t.TempDir(), true)
	key := make([]byte, 32)
	_, err := rand.Read(key)
	require.NoError(t, err)
	vault, err := credvault.New("registry credential", key)
	require.NoError(t, err)
	dir := t.TempDir()
	return NewService(NewStore(db), vault, dir), db, dir
}

func TestACredentialIsStoredSealedAndNeverHandedBack(t *testing.T) {
	service, db, _ := newTestService(t)

	view, err := service.Create(Input{Registry: "ghcr.io", Username: "robot", Secret: "ghp_averysecrettoken"})
	require.NoError(t, err)
	require.Equal(t, "ghcr.io", view.Registry)
	require.True(t, view.HasSecret)
	require.Equal(t, "ghp_…oken", view.SecretHint)

	// The row itself must not carry the secret in clear.
	var row Credential
	require.NoError(t, db.Where("registry = ?", "ghcr.io").First(&row).Error)
	require.NotContains(t, string(row.EncryptedPayload), "averysecrettoken")
	require.False(t, bytes.Contains(row.EncryptedPayload, []byte("averysecrettoken")))

	// Nor does anything the UI receives.
	listed, err := service.List()
	require.NoError(t, err)
	require.Len(t, listed, 1)
	encoded, err := json.Marshal(listed)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "averysecrettoken")
}

func TestALoginIsFoundForEverySpellingOfItsRegistry(t *testing.T) {
	service, _, _ := newTestService(t)
	_, err := service.Create(Input{Registry: "https://docker.io/", Username: "me", Secret: "dckr_pat_secret_value"})
	require.NoError(t, err)

	for _, image := range []string{"nginx", "nginx:1.27", "docker.io/library/nginx", "index.docker.io/me/app:tag"} {
		auth, ok := service.AuthForImage(image)
		require.Truef(t, ok, "no credential found for %s", image)
		require.Equal(t, "me", auth.Username)
	}
	_, ok := service.AuthForImage("ghcr.io/me/app")
	require.False(t, ok, "a credential must not leak to another registry")
}

func TestTheDockerApiReceivesTheCredentialItExpects(t *testing.T) {
	service, _, _ := newTestService(t)
	_, err := service.Create(Input{Registry: "ghcr.io", Username: "robot", Secret: "ghp_averysecrettoken"})
	require.NoError(t, err)

	encoded := service.EngineAuthForImage("ghcr.io/team/app:1.0")
	require.NotEmpty(t, encoded)
	raw, err := base64.URLEncoding.DecodeString(encoded)
	require.NoError(t, err)
	var document struct {
		Username      string `json:"username"`
		Password      string `json:"password"`
		ServerAddress string `json:"serveraddress"`
	}
	require.NoError(t, json.Unmarshal(raw, &document))
	require.Equal(t, "robot", document.Username)
	require.Equal(t, "ghp_averysecrettoken", document.Password)
	require.Equal(t, "ghcr.io", document.ServerAddress)

	require.Empty(t, service.EngineAuthForImage("busybox:1.37"),
		"an image nothing is stored for must stay an anonymous pull")
}

// What makes `compose pull` and every build authenticate: the Docker CLI
// configuration Dockman owns.
func TestTheDockerCliConfigurationCarriesTheLogin(t *testing.T) {
	service, _, dir := newTestService(t)
	path := filepath.Join(dir, "config.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"auths":{"other.example":{"auth":"a3JpbjpwYXNz"}},"psFormat":"table"}`), 0o600))

	_, err := service.Create(Input{Registry: "ghcr.io", Username: "robot", Secret: "ghp_averysecrettoken"})
	require.NoError(t, err)

	document := readConfig(t, path)
	auths := document["auths"].(map[string]any)
	entry := auths["ghcr.io"].(map[string]any)
	require.Equal(t, base64.StdEncoding.EncodeToString([]byte("robot:ghp_averysecrettoken")), entry["auth"])
	require.Contains(t, auths, "other.example", "a login Dockman did not write stays")
	require.Equal(t, "table", document["psFormat"], "the rest of the file stays")

	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	// Docker Hub is written under the only key the CLI reads.
	_, err = service.Create(Input{Registry: "docker.io", Username: "me", Secret: "dckr_pat_secret_value"})
	require.NoError(t, err)
	auths = readConfig(t, path)["auths"].(map[string]any)
	require.Contains(t, auths, "https://index.docker.io/v1/")
}

func TestDeletingACredentialTakesItOutOfTheDockerConfiguration(t *testing.T) {
	service, _, dir := newTestService(t)
	view, err := service.Create(Input{Registry: "ghcr.io", Username: "robot", Secret: "ghp_averysecrettoken"})
	require.NoError(t, err)
	require.NoError(t, service.Delete(view.UUID))

	auths := readConfig(t, filepath.Join(dir, "config.json"))["auths"].(map[string]any)
	require.NotContains(t, auths, "ghcr.io", "a deleted credential must stop authenticating pulls")
	_, ok := service.Lookup("ghcr.io")
	require.False(t, ok)
}

func TestAnEditKeepsTheSecretUnlessANewOneIsTyped(t *testing.T) {
	service, _, dir := newTestService(t)
	view, err := service.Create(Input{Registry: "ghcr.io", Username: "robot", Secret: "ghp_averysecrettoken"})
	require.NoError(t, err)

	_, err = service.Update(view.UUID, Input{Registry: "ghcr.io", Username: "robot2"})
	require.NoError(t, err)
	auth, ok := service.Lookup("ghcr.io")
	require.True(t, ok)
	require.Equal(t, "robot2", auth.Username)
	require.Equal(t, "ghp_averysecrettoken", auth.Secret)

	_, err = service.Update(view.UUID, Input{Registry: "ghcr.io", Username: "robot2", Secret: "ghp_anothersecrettoken"})
	require.NoError(t, err)
	auth, _ = service.Lookup("ghcr.io")
	require.Equal(t, "ghp_anothersecrettoken", auth.Secret)
	entry := readConfig(t, filepath.Join(dir, "config.json"))["auths"].(map[string]any)["ghcr.io"].(map[string]any)
	require.Equal(t, base64.StdEncoding.EncodeToString([]byte("robot2:ghp_anothersecrettoken")), entry["auth"],
		"the Docker configuration follows the edit")
}

func TestTwoCredentialsForOneRegistryAreRefused(t *testing.T) {
	service, _, _ := newTestService(t)
	_, err := service.Create(Input{Registry: "ghcr.io", Username: "robot", Secret: "ghp_averysecrettoken"})
	require.NoError(t, err)
	_, err = service.Create(Input{Registry: "https://ghcr.io/", Username: "other", Secret: "ghp_othersecrettoken"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "already exists")
}

func TestWhatTheFormMustNotAccept(t *testing.T) {
	service, _, _ := newTestService(t)
	for name, input := range map[string]Input{
		"no registry": {Username: "u", Secret: "s3cr3t_value"},
		"no username": {Registry: "ghcr.io", Secret: "s3cr3t_value"},
		"no secret":   {Registry: "ghcr.io", Username: "u"},
		"two hosts":   {Registry: "ghcr.io registry.example.com", Username: "u", Secret: "s3cr3t_value"},
	} {
		_, err := service.Create(input)
		require.Errorf(t, err, "%s was accepted", name)
	}
}

// Operators paste what they have in front of them, which is usually a
// repository URL. Keeping the host is the useful reading of it.
func TestAPastedRepositoryUrlKeepsOnlyItsRegistry(t *testing.T) {
	service, _, _ := newTestService(t)
	view, err := service.Create(Input{Registry: "https://ghcr.io/cerede2000/dockman", Username: "robot", Secret: "ghp_averysecrettoken"})
	require.NoError(t, err)
	require.Equal(t, "ghcr.io", view.Registry)
	_, ok := service.AuthForImage("ghcr.io/cerede2000/dockman:integration")
	require.True(t, ok)
}

// A master key that changed must not take the other credentials down with it.
func TestAnUnreadableCredentialDoesNotBlockTheOthers(t *testing.T) {
	service, db, _ := newTestService(t)
	_, err := service.Create(Input{Registry: "ghcr.io", Username: "robot", Secret: "ghp_averysecrettoken"})
	require.NoError(t, err)
	require.NoError(t, db.Exec(
		"INSERT INTO registry_credentials (created_at, updated_at, uuid, registry, username, encrypted_payload, secret_hint, last_test_error) VALUES (datetime('now'), datetime('now'), 'stale', 'other.example', 'me', ?, '', '')",
		[]byte("sealed with a key that is gone")).Error)

	require.NoError(t, service.Reload())
	_, ok := service.Lookup("ghcr.io")
	require.True(t, ok, "a readable credential must survive an unreadable neighbour")
	_, ok = service.Lookup("other.example")
	require.False(t, ok)
}

func readConfig(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var document map[string]any
	require.NoError(t, json.Unmarshal(raw, &document))
	return document
}
