package registry

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/RA341/dockman/pkg/credvault"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
)

// Auth is a resolved login, as the pull paths need it.
type Auth struct {
	Registry string
	Username string
	Secret   string
}

// Input is what the operator types.
type Input struct {
	Registry string `json:"registry"`
	Username string `json:"username"`
	Secret   string `json:"secret"`
}

// View is what the UI gets back. The secret never appears: only whether one
// is stored and the hint that lets an operator recognise which one.
type View struct {
	UUID          string     `json:"uuid"`
	Registry      string     `json:"registry"`
	Username      string     `json:"username"`
	SecretHint    string     `json:"secretHint"`
	HasSecret     bool       `json:"hasSecret"`
	UpdatedAt     time.Time  `json:"updatedAt"`
	LastTestedAt  *time.Time `json:"lastTestedAt,omitempty"`
	LastTestError string     `json:"lastTestError,omitempty"`
}

type secretPayload struct {
	Secret string `json:"secret"`
}

type Service struct {
	store  Store
	vault  *credvault.Vault
	config *dockerConfig

	mu    sync.RWMutex
	cache map[string]Auth
}

func NewService(store Store, vault *credvault.Vault, dockerConfigDir string) *Service {
	service := &Service{
		store:  store,
		vault:  vault,
		config: newDockerConfig(dockerConfigDir),
		cache:  map[string]Auth{},
	}
	return service
}

// Reload rebuilds the in-memory lookup table and rewrites the Docker CLI
// configuration, so a restart lands in the same state as a change does.
func (s *Service) Reload() error {
	rows, err := s.store.List()
	if err != nil {
		return err
	}
	cache := make(map[string]Auth, len(rows))
	for _, row := range rows {
		secret, err := s.decrypt(row)
		if err != nil {
			// One unreadable row must not take the others down: it means the
			// master key changed, and the operator needs the others working
			// while they re-enter that one.
			log.Error().Err(err).Str("registry", row.Registry).Msg("Stored registry credential cannot be decrypted; re-enter it in Settings > Registries")
			continue
		}
		cache[row.Registry] = Auth{Registry: row.Registry, Username: row.Username, Secret: secret}
	}
	s.mu.Lock()
	s.cache = cache
	s.mu.Unlock()
	return s.config.write(cache)
}

// Lookup answers for a registry host, normalised the same way as the image
// references it is matched against.
func (s *Service) Lookup(host string) (Auth, bool) {
	if s == nil {
		return Auth{}, false
	}
	key := NormalizeHost(host)
	s.mu.RLock()
	defer s.mu.RUnlock()
	auth, ok := s.cache[key]
	return auth, ok
}

// AuthForImage resolves the credential an image reference needs.
func (s *Service) AuthForImage(image string) (Auth, bool) {
	ref, err := ParseImage(image)
	if err != nil {
		return Auth{}, false
	}
	return s.Lookup(ref.Registry)
}

// EngineAuthForImage is what the Docker Engine API expects in RegistryAuth:
// base64url of the credential document. Empty when nothing is stored, which
// leaves the pull exactly as anonymous as it was before.
func (s *Service) EngineAuthForImage(image string) string {
	auth, ok := s.AuthForImage(image)
	if !ok {
		return ""
	}
	document := struct {
		Username      string `json:"username"`
		Password      string `json:"password"`
		ServerAddress string `json:"serveraddress"`
	}{Username: auth.Username, Password: auth.Secret, ServerAddress: auth.Registry}
	encoded, err := json.Marshal(document)
	if err != nil {
		return ""
	}
	return base64.URLEncoding.EncodeToString(encoded)
}

func (s *Service) List() ([]View, error) {
	rows, err := s.store.List()
	if err != nil {
		return nil, err
	}
	views := make([]View, 0, len(rows))
	for _, row := range rows {
		views = append(views, view(row))
	}
	return views, nil
}

func (s *Service) Create(input Input) (View, error) {
	normalized, err := validate(input, true)
	if err != nil {
		return View{}, err
	}
	if _, err := s.store.GetByRegistry(normalized.Registry); err == nil {
		return View{}, fmt.Errorf("a credential for %s already exists; edit it instead", normalized.Registry)
	} else if !isNotFound(err) {
		return View{}, err
	}
	row := Credential{
		UUID:       uuid.NewString(),
		Registry:   normalized.Registry,
		Username:   normalized.Username,
		SecretHint: secretHint(normalized.Secret),
	}
	sealed, err := s.encrypt(row.UUID, normalized.Secret)
	if err != nil {
		return View{}, err
	}
	row.EncryptedPayload = sealed
	if err := s.store.Save(&row); err != nil {
		return View{}, err
	}
	return view(row), s.Reload()
}

func (s *Service) Update(credentialUUID string, input Input) (View, error) {
	row, err := s.store.Get(credentialUUID)
	if err != nil {
		return View{}, err
	}
	normalized, err := validate(input, false)
	if err != nil {
		return View{}, err
	}
	if normalized.Registry != row.Registry {
		if _, err := s.store.GetByRegistry(normalized.Registry); err == nil {
			return View{}, fmt.Errorf("a credential for %s already exists; edit it instead", normalized.Registry)
		} else if !isNotFound(err) {
			return View{}, err
		}
	}
	row.Registry = normalized.Registry
	row.Username = normalized.Username
	if normalized.Secret != "" {
		sealed, err := s.encrypt(row.UUID, normalized.Secret)
		if err != nil {
			return View{}, err
		}
		row.EncryptedPayload = sealed
		row.SecretHint = secretHint(normalized.Secret)
	}
	if err := s.store.Save(&row); err != nil {
		return View{}, err
	}
	return view(row), s.Reload()
}

func (s *Service) Delete(credentialUUID string) error {
	row, err := s.store.Get(credentialUUID)
	if err != nil {
		return err
	}
	if err := s.store.Delete(credentialUUID); err != nil {
		return err
	}
	// The entry goes from the Docker configuration too, not just from the
	// database: a deleted credential that still authenticates pulls is a
	// credential nobody can see any more.
	if err := s.config.remove(row.Registry); err != nil {
		log.Error().Err(err).Msg("Could not remove the Docker CLI entry of a deleted registry credential")
	}
	return s.Reload()
}

// Test authenticates against the registry for real, with the secret typed in
// the form when there is one, and with the stored secret otherwise.
func (s *Service) Test(ctx context.Context, credentialUUID string, input Input) error {
	auth := Auth{Registry: NormalizeHost(input.Registry), Username: strings.TrimSpace(input.Username), Secret: input.Secret}
	var row Credential
	hasRow := false
	if credentialUUID != "" {
		stored, err := s.store.Get(credentialUUID)
		if err != nil {
			return err
		}
		row, hasRow = stored, true
		if auth.Registry == "" {
			auth.Registry = stored.Registry
		}
		if auth.Username == "" {
			auth.Username = stored.Username
		}
		if auth.Secret == "" {
			secret, err := s.decrypt(stored)
			if err != nil {
				return err
			}
			auth.Secret = secret
		}
	}
	if auth.Registry == "" || auth.Username == "" || auth.Secret == "" {
		return errors.New("registry, username and secret are required to test a login")
	}

	testErr := Probe(ctx, auth)
	if hasRow {
		now := time.Now()
		row.LastTestedAt = &now
		row.LastTestError = ""
		if testErr != nil {
			row.LastTestError = testErr.Error()
		}
		if err := s.store.Save(&row); err != nil {
			log.Error().Err(err).Msg("Could not record the result of a registry credential test")
		}
	}
	return testErr
}

func (s *Service) encrypt(credentialUUID, secret string) ([]byte, error) {
	payload, err := json.Marshal(secretPayload{Secret: secret})
	if err != nil {
		return nil, err
	}
	return s.vault.EncryptFor(payload, scope(credentialUUID))
}

func (s *Service) decrypt(row Credential) (string, error) {
	if len(row.EncryptedPayload) == 0 {
		return "", fmt.Errorf("credential %s holds no secret", row.Registry)
	}
	plain, err := s.vault.DecryptFor(row.EncryptedPayload, scope(row.UUID))
	if err != nil {
		return "", err
	}
	var payload secretPayload
	if err := json.Unmarshal(plain, &payload); err != nil {
		return "", err
	}
	return payload.Secret, nil
}

func scope(credentialUUID string) string { return "registry-credential/" + credentialUUID }

func validate(input Input, secretRequired bool) (Input, error) {
	host := NormalizeHost(input.Registry)
	if host == "" {
		return Input{}, errors.New("a registry host is required, for example ghcr.io or registry.example.com:5000")
	}
	if strings.ContainsAny(host, " \t") {
		return Input{}, fmt.Errorf("%q is not a registry host", input.Registry)
	}
	username := strings.TrimSpace(input.Username)
	if username == "" {
		return Input{}, errors.New("a username is required")
	}
	secret := input.Secret
	if secretRequired && strings.TrimSpace(secret) == "" {
		return Input{}, errors.New("a password or token is required")
	}
	return Input{Registry: host, Username: username, Secret: secret}, nil
}

func view(row Credential) View {
	return View{
		UUID:          row.UUID,
		Registry:      row.Registry,
		Username:      row.Username,
		SecretHint:    row.SecretHint,
		HasSecret:     len(row.EncryptedPayload) > 0,
		UpdatedAt:     row.UpdatedAt,
		LastTestedAt:  row.LastTestedAt,
		LastTestError: row.LastTestError,
	}
}

// secretHint keeps enough of a token to tell two of them apart and not enough
// to use one. Short secrets get no hint rather than most of themselves.
func secretHint(secret string) string {
	trimmed := strings.TrimSpace(secret)
	if len(trimmed) < 12 {
		return ""
	}
	return trimmed[:4] + "…" + trimmed[len(trimmed)-4:]
}
