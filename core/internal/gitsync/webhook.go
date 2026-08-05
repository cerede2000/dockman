package gitsync

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"gorm.io/gorm"
)

const (
	maxGitWebhookPayload    = 1 << 20
	maxGitWebhookDeliveries = 4096
)

var webhookHeaderPattern = regexp.MustCompile(`^[A-Za-z0-9-]{1,128}$`)

type RepositoryWebhookInput struct {
	Enabled      bool `json:"enabled"`
	RotateSecret bool `json:"rotateSecret"`
}

type RepositoryWebhookView struct {
	ID             string     `json:"id,omitempty"`
	RepositoryID   string     `json:"repositoryId"`
	Enabled        bool       `json:"enabled"`
	Configured     bool       `json:"configured"`
	Path           string     `json:"path,omitempty"`
	Secret         string     `json:"secret,omitempty"`
	LastDeliveryID string     `json:"lastDeliveryId,omitempty"`
	LastEvent      string     `json:"lastEvent,omitempty"`
	LastStatus     string     `json:"lastStatus,omitempty"`
	LastError      string     `json:"lastError,omitempty"`
	LastReceivedAt *time.Time `json:"lastReceivedAt,omitempty"`
}

type githubPushPayload struct {
	Ref        string `json:"ref"`
	Deleted    bool   `json:"deleted"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
}

func (s *Service) RepositoryWebhook(repositoryID string) (RepositoryWebhookView, error) {
	if !s.enabled {
		return RepositoryWebhookView{}, errors.New("Git synchronization is disabled")
	}
	if _, err := s.store.GetRepository(repositoryID); err != nil {
		return RepositoryWebhookView{}, err
	}
	var row RepositoryWebhook
	err := s.store.db.Where("repository_uuid = ?", repositoryID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return RepositoryWebhookView{RepositoryID: repositoryID}, nil
	}
	if err != nil {
		return RepositoryWebhookView{}, err
	}
	return repositoryWebhookView(row), nil
}

func (s *Service) ConfigureRepositoryWebhook(repositoryID string, input RepositoryWebhookInput) (RepositoryWebhookView, error) {
	if !s.enabled || s.vault == nil {
		return RepositoryWebhookView{}, errors.New("Git synchronization is disabled")
	}
	repository, err := s.store.GetRepository(repositoryID)
	if err != nil {
		return RepositoryWebhookView{}, err
	}
	if repository.Provider != "github" {
		return RepositoryWebhookView{}, errors.New("inbound webhooks currently support GitHub repositories only")
	}
	var row RepositoryWebhook
	err = s.store.db.Where("repository_uuid = ?", repositoryID).First(&row).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return RepositoryWebhookView{}, err
	}
	created := row.ID == 0
	if created {
		row = RepositoryWebhook{UUID: uuid.NewString(), RepositoryUUID: repositoryID}
	}
	plainSecret := ""
	if created || input.RotateSecret {
		plainSecret, err = randomWebhookSecret()
		if err != nil {
			return RepositoryWebhookView{}, err
		}
		row.EncryptedSecret, err = s.vault.EncryptFor([]byte(plainSecret), webhookSecretScope(row.UUID))
		if err != nil {
			return RepositoryWebhookView{}, err
		}
	}
	row.Enabled = input.Enabled
	if err := s.store.db.Save(&row).Error; err != nil {
		return RepositoryWebhookView{}, err
	}
	view := repositoryWebhookView(row)
	view.Secret = plainSecret // returned exactly once after creation/rotation
	return view, nil
}

func repositoryWebhookView(row RepositoryWebhook) RepositoryWebhookView {
	return RepositoryWebhookView{
		ID: row.UUID, RepositoryID: row.RepositoryUUID, Enabled: row.Enabled,
		Configured: len(row.EncryptedSecret) > 0, Path: "/api/git-webhooks/" + row.UUID,
		LastDeliveryID: row.LastDeliveryID, LastEvent: row.LastEvent, LastStatus: row.LastStatus,
		LastError: row.LastError, LastReceivedAt: row.LastReceivedAt,
	}
}

func randomWebhookSecret() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

func webhookSecretScope(id string) string { return "git-webhook/" + id }

func NewWebhookHandler(service *Service) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /{webhookId}", func(w http.ResponseWriter, r *http.Request) {
		service.receiveGitHubWebhook(w, r)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		mux.ServeHTTP(w, r)
	})
}

func (s *Service) receiveGitHubWebhook(w http.ResponseWriter, r *http.Request) {
	if !s.enabled || s.vault == nil {
		http.NotFound(w, r)
		return
	}
	var hook RepositoryWebhook
	if err := s.store.db.Where("uuid = ? AND enabled = ?", r.PathValue("webhookId"), true).First(&hook).Error; err != nil {
		http.NotFound(w, r)
		return
	}
	deliveryID := strings.TrimSpace(r.Header.Get("X-GitHub-Delivery"))
	event := strings.TrimSpace(r.Header.Get("X-GitHub-Event"))
	if !webhookHeaderPattern.MatchString(deliveryID) || !webhookHeaderPattern.MatchString(event) {
		http.Error(w, "invalid GitHub webhook headers", http.StatusBadRequest)
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		http.Error(w, "GitHub webhook content type must be application/json", http.StatusUnsupportedMediaType)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxGitWebhookPayload))
	if err != nil {
		http.Error(w, "invalid or oversized webhook payload", http.StatusRequestEntityTooLarge)
		return
	}
	secret, err := s.vault.DecryptFor(hook.EncryptedSecret, webhookSecretScope(hook.UUID))
	if err != nil {
		http.Error(w, "webhook configuration cannot be read", http.StatusInternalServerError)
		return
	}
	validSignature := verifyGitHubSignature(body, r.Header.Get("X-Hub-Signature-256"), secret)
	clear(secret)
	if !validSignature {
		http.Error(w, "invalid webhook signature", http.StatusUnauthorized)
		return
	}
	if err := s.recordWebhookDelivery(hook.UUID, deliveryID, event); err != nil {
		if errors.Is(err, errWebhookReplay) {
			http.Error(w, "webhook delivery already processed", http.StatusConflict)
			return
		}
		http.Error(w, "unable to record webhook delivery", http.StatusInternalServerError)
		return
	}
	now := time.Now().UTC()
	updates := map[string]any{"last_delivery_id": deliveryID, "last_event": event, "last_status": "accepted", "last_error": "", "last_received_at": &now}
	if event == "ping" {
		_ = s.store.db.Model(&RepositoryWebhook{}).Where("uuid = ?", hook.UUID).Updates(updates).Error
		w.WriteHeader(http.StatusAccepted)
		return
	}
	if event != "push" {
		updates["last_status"] = "ignored"
		_ = s.store.db.Model(&RepositoryWebhook{}).Where("uuid = ?", hook.UUID).Updates(updates).Error
		w.WriteHeader(http.StatusAccepted)
		return
	}
	var payload githubPushPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		http.Error(w, "invalid GitHub push payload", http.StatusBadRequest)
		return
	}
	repository, err := s.store.GetRepository(hook.RepositoryUUID)
	if err != nil {
		http.Error(w, "repository no longer exists", http.StatusGone)
		return
	}
	expectedIdentity, err := githubRepositoryIdentity(repository.RemoteURL)
	if err != nil || !strings.EqualFold(expectedIdentity, strings.TrimSpace(payload.Repository.FullName)) {
		http.Error(w, "webhook repository does not match", http.StatusForbidden)
		return
	}
	if payload.Deleted || payload.Ref != "refs/heads/"+repository.DefaultBranch {
		updates["last_status"] = "ignored"
		_ = s.store.db.Model(&RepositoryWebhook{}).Where("uuid = ?", hook.UUID).Updates(updates).Error
		w.WriteHeader(http.StatusAccepted)
		return
	}
	switch s.enqueueWebhookRepository(repository.UUID) {
	case webhookCoalesced:
		updates["last_status"] = "coalesced"
	case webhookQueueFull:
		updates["last_status"] = "error"
		updates["last_error"] = "Git webhook queue is full; polling remains available as a safety net"
		_ = s.store.db.Model(&RepositoryWebhook{}).Where("uuid = ?", hook.UUID).Updates(updates).Error
		http.Error(w, "Git webhook queue is busy", http.StatusServiceUnavailable)
		return
	}
	_ = s.store.db.Model(&RepositoryWebhook{}).Where("uuid = ?", hook.UUID).Updates(updates).Error
	w.WriteHeader(http.StatusAccepted)
}

func verifyGitHubSignature(body []byte, header string, secret []byte) bool {
	if !strings.HasPrefix(header, "sha256=") {
		return false
	}
	provided, err := hex.DecodeString(strings.TrimPrefix(header, "sha256="))
	if err != nil || len(provided) != sha256.Size {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(body)
	return hmac.Equal(provided, mac.Sum(nil))
}

var errWebhookReplay = errors.New("webhook delivery already processed")

func (s *Service) recordWebhookDelivery(webhookID, deliveryID, event string) error {
	row := WebhookDelivery{WebhookUUID: webhookID, DeliveryID: deliveryID, Event: event}
	err := s.store.db.Create(&row).Error
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "unique") {
		return errWebhookReplay
	}
	if err != nil {
		return err
	}
	cutoff := time.Now().UTC().Add(-7 * 24 * time.Hour)
	_ = s.store.db.Unscoped().Where("created_at < ?", cutoff).Delete(&WebhookDelivery{}).Error
	retained := s.store.db.Model(&WebhookDelivery{}).Select("id").Where("webhook_uuid = ?", webhookID).Order("id DESC").Limit(maxGitWebhookDeliveries)
	_ = s.store.db.Unscoped().Where("webhook_uuid = ?", webhookID).Where("id NOT IN (?)", retained).Delete(&WebhookDelivery{}).Error
	return nil
}

type webhookEnqueueResult uint8

const (
	webhookQueued webhookEnqueueResult = iota
	webhookCoalesced
	webhookQueueFull
)

func (s *Service) enqueueWebhookRepository(repositoryID string) webhookEnqueueResult {
	s.webhookMu.Lock()
	if _, pending := s.webhookPending[repositoryID]; pending {
		s.webhookMu.Unlock()
		return webhookCoalesced
	}
	s.webhookPending[repositoryID] = struct{}{}
	s.webhookMu.Unlock()
	select {
	case s.webhookQueue <- repositoryID:
		return webhookQueued
	default:
		s.webhookMu.Lock()
		delete(s.webhookPending, repositoryID)
		s.webhookMu.Unlock()
		return webhookQueueFull
	}
}

func (s *Service) StartWebhookWorker(ctx context.Context) {
	if !s.enabled {
		return
	}
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case repositoryID := <-s.webhookQueue:
				s.webhookMu.Lock()
				delete(s.webhookPending, repositoryID)
				s.webhookMu.Unlock()
				s.runWebhookRepository(ctx, repositoryID)
			}
		}
	}()
}

func (s *Service) runWebhookRepository(ctx context.Context, repositoryID string) {
	bindings, err := s.store.ListBindings()
	if err != nil {
		log.Warn().Err(err).Str("repository", repositoryID).Msg("unable to list Git webhook bindings")
		s.updateRepositoryWebhookResult(repositoryID, "error", err.Error())
		return
	}
	processed := 0
	var failures []string
	for _, binding := range bindings {
		if ctx.Err() != nil {
			return
		}
		if binding.RepositoryUUID != repositoryID || !binding.Enabled || !binding.AutoSyncEnabled || binding.AutoSyncPaused {
			continue
		}
		if _, err := s.RunBindingAutoSyncNow(ctx, binding.UUID); err != nil {
			log.Warn().Err(err).Str("binding", binding.UUID).Msg("Git webhook synchronization failed")
			failures = append(failures, binding.StackPath+": "+safeGitError(err))
		}
		processed++
	}
	if len(failures) > 0 {
		s.updateRepositoryWebhookResult(repositoryID, "error", strings.Join(failures, "; "))
	} else if processed == 0 {
		s.updateRepositoryWebhookResult(repositoryID, "ignored", "No enabled, unpaused automatic Folder Link uses this repository")
	} else {
		s.updateRepositoryWebhookResult(repositoryID, "processed", "")
	}
}

func (s *Service) updateRepositoryWebhookResult(repositoryID, status, message string) {
	if message != "" {
		message = safeGitError(fmt.Errorf("%s", message))
	}
	_ = s.store.db.Model(&RepositoryWebhook{}).Where("repository_uuid = ?", repositoryID).Updates(map[string]any{"last_status": status, "last_error": message}).Error
}
