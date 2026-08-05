package notifications

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	ChannelSMTP    = "smtp"
	ChannelWebhook = "webhook"
	ChannelGotify  = "gotify"
	ChannelNtfy    = "ntfy"
	ChannelDiscord = "discord"
	ChannelApprise = "apprise"

	maxChannelResponse = 64 << 10
	maxChannelPayload  = 256 << 10
)

const (
	EventUpdateAvailable    = "updates.available"
	EventUpdateSuccess      = "updates.success"
	EventUpdateFailure      = "updates.failure"
	EventCleanerSuccess     = "cleaner.success"
	EventCleanerFailure     = "cleaner.failure"
	EventBuildSuccess       = "build.success"
	EventBuildFailure       = "build.failure"
	EventGitSyncSuccess     = "git.sync.success"
	EventGitSyncFailure     = "git.sync.failure"
	EventGitStackDiscovered = "git.stack.discovered"
	EventGitConflict        = "git.conflict"
	EventGitDeploySuccess   = "git.deploy.success"
	EventGitDeployFailure   = "git.deploy.failure"
	EventGitRollback        = "git.rollback"
	EventContainerRestart   = "container.restart"
	EventContainerOOM       = "container.oom"
	EventContainerUnhealthy = "container.unhealthy"
)

var validEventTypes = map[string]struct{}{
	EventUpdateAvailable: {}, EventUpdateSuccess: {}, EventUpdateFailure: {},
	EventCleanerSuccess: {}, EventCleanerFailure: {}, EventBuildSuccess: {}, EventBuildFailure: {},
	EventGitSyncSuccess: {}, EventGitSyncFailure: {}, EventGitStackDiscovered: {}, EventGitConflict: {},
	EventGitDeploySuccess: {}, EventGitDeployFailure: {}, EventGitRollback: {},
	EventContainerRestart: {}, EventContainerOOM: {}, EventContainerUnhealthy: {},
}

type ChannelConfig struct {
	ID                uint      `gorm:"primaryKey" json:"id"`
	CreatedAt         time.Time `json:"createdAt"`
	UpdatedAt         time.Time `json:"updatedAt"`
	Host              string    `gorm:"not null;uniqueIndex:idx_update_notification_channel" json:"host"`
	Name              string    `gorm:"not null;uniqueIndex:idx_update_notification_channel" json:"name"`
	Type              string    `gorm:"not null" json:"type"`
	Enabled           bool      `gorm:"not null" json:"enabled"`
	Target            string    `gorm:"not null;default:''" json:"target"`
	Topic             string    `gorm:"not null;default:''" json:"topic"`
	Priority          int       `gorm:"not null;default:0" json:"priority"`
	Tags              string    `gorm:"not null;default:''" json:"tags"`
	AllowInsecureHTTP bool      `gorm:"not null;default:false" json:"allowInsecureHttp"`
	NotifyUpdates     bool      `gorm:"not null" json:"notifyUpdates"`
	NotifyErrors      bool      `gorm:"not null" json:"notifyErrors"`
	EventTypes        string    `gorm:"not null;default:''" json:"-"`
	SecretKey         string    `gorm:"not null;uniqueIndex" json:"-"`
	EncryptedConfig   []byte    `gorm:"not null" json:"-"`
}

func (ChannelConfig) TableName() string { return "update_notification_channels" }

type ChannelInput struct {
	ID                uint     `json:"id,omitempty"`
	Name              string   `json:"name"`
	Type              string   `json:"type"`
	Enabled           bool     `json:"enabled"`
	URL               string   `json:"url,omitempty"`
	Token             string   `json:"token,omitempty"`
	Username          string   `json:"username,omitempty"`
	Password          string   `json:"password,omitempty"`
	Server            string   `json:"server,omitempty"`
	Port              int      `json:"port,omitempty"`
	Security          string   `json:"security,omitempty"`
	FromAddress       string   `json:"fromAddress,omitempty"`
	Recipients        string   `json:"recipients,omitempty"`
	ClearCredentials  bool     `json:"clearCredentials,omitempty"`
	Topic             string   `json:"topic,omitempty"`
	Priority          int      `json:"priority,omitempty"`
	Tags              string   `json:"tags,omitempty"`
	AllowInsecureHTTP bool     `json:"allowInsecureHttp"`
	NotifyUpdates     bool     `json:"notifyUpdates"`
	NotifyErrors      bool     `json:"notifyErrors"`
	Events            []string `json:"events,omitempty"`
}

type ChannelView struct {
	ID                uint      `json:"id"`
	Name              string    `json:"name"`
	Type              string    `json:"type"`
	Enabled           bool      `json:"enabled"`
	Target            string    `json:"target"`
	Topic             string    `json:"topic,omitempty"`
	Priority          int       `json:"priority,omitempty"`
	Tags              string    `json:"tags,omitempty"`
	AllowInsecureHTTP bool      `json:"allowInsecureHttp"`
	NotifyUpdates     bool      `json:"notifyUpdates"`
	NotifyErrors      bool      `json:"notifyErrors"`
	Events            []string  `json:"events"`
	Server            string    `json:"server,omitempty"`
	Port              int       `json:"port,omitempty"`
	Security          string    `json:"security,omitempty"`
	FromAddress       string    `json:"fromAddress,omitempty"`
	Recipients        string    `json:"recipients,omitempty"`
	Configured        bool      `json:"configured"`
	HasToken          bool      `json:"hasToken"`
	HasUsername       bool      `json:"hasUsername"`
	HasPassword       bool      `json:"hasPassword"`
	Error             string    `json:"error,omitempty"`
	UpdatedAt         time.Time `json:"updatedAt,omitempty"`
}

type channelSecrets struct {
	URL         string `json:"url"`
	Token       string `json:"token,omitempty"`
	Username    string `json:"username,omitempty"`
	Password    string `json:"password,omitempty"`
	Server      string `json:"server,omitempty"`
	Port        int    `json:"port,omitempty"`
	Security    string `json:"security,omitempty"`
	FromAddress string `json:"fromAddress,omitempty"`
	Recipients  string `json:"recipients,omitempty"`
}

type ChannelEvent struct {
	Kind     string    `json:"kind"`
	Host     string    `json:"host"`
	Title    string    `json:"title"`
	Message  string    `json:"message"`
	Severity string    `json:"severity"`
	Time     time.Time `json:"timestamp"`
}

type ChannelSender interface {
	Send(context.Context, ChannelConfig, channelSecrets, ChannelEvent) error
}

type HTTPChannelSender struct {
	Timeout time.Duration
}

// StartDispatcher starts one bounded, process-wide delivery worker. Producers
// never wait for SMTP or an HTTP endpoint, and the queue has a hard ceiling so
// a broken channel cannot grow Dockman's heap without bound.
func (s *Service) StartDispatcher(ctx context.Context) {
	if s == nil {
		return
	}
	s.dispatchOnce.Do(func() {
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case event := <-s.eventQueue:
					if err := s.Publish(ctx, event); err != nil {
						log.Warn().Err(err).Str("host", event.Host).Str("event", event.Kind).Msg("notification delivery failed")
					}
				}
			}
		}()
	})
}

// Enqueue schedules an operational event without blocking the Docker/Git
// action that produced it. False means the bounded queue was full.
func (s *Service) Enqueue(event ChannelEvent) bool {
	if s == nil || strings.TrimSpace(event.Host) == "" || strings.TrimSpace(event.Kind) == "" {
		return false
	}
	if event.Time.IsZero() {
		event.Time = time.Now().UTC()
	}
	select {
	case s.eventQueue <- event:
		return true
	default:
		log.Warn().Str("host", event.Host).Str("event", event.Kind).Msg("notification queue full; event dropped")
		return false
	}
}

// Publish fans an event out only to channels explicitly subscribed to its
// type. Delivery failures are isolated and recorded independently.
func (s *Service) Publish(ctx context.Context, event ChannelEvent) error {
	destinations, err := s.enabledDestinations(event.Host)
	if err != nil {
		return err
	}
	var deliveryErrors []error
	for _, destination := range destinations {
		if !eventTypeEnabled(destination.events, event.Kind) {
			continue
		}
		if sendErr := destination.send(ctx, event); sendErr != nil {
			deliveryErrors = append(deliveryErrors, fmt.Errorf("%s: %w", destination.name, sendErr))
		}
	}
	return errors.Join(deliveryErrors...)
}

func eventTypeEnabled(values []string, kind string) bool {
	for _, value := range values {
		if value == kind {
			return true
		}
	}
	return false
}

func (s *Service) ListChannels(host string) ([]ChannelView, error) {
	var rows []ChannelConfig
	if err := s.db.Where("host = ?", host).Order("name ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	views := make([]ChannelView, 0, len(rows))
	for _, row := range rows {
		secrets, err := s.decryptChannel(row)
		if err != nil {
			view := channelView(row, channelSecrets{})
			view.Error = "encrypted channel configuration cannot be read"
			views = append(views, view)
			continue
		}
		views = append(views, channelView(row, secrets))
	}
	return views, nil
}

// MigrateLegacySMTPConfigs converts the former one-SMTP-per-host model into
// ordinary named channels. It is idempotent and deletes a legacy row only
// after the encrypted replacement has been committed successfully.
func (s *Service) MigrateLegacySMTPConfigs() error {
	var legacy []SMTPConfig
	if err := s.db.Find(&legacy).Error; err != nil {
		return err
	}
	for _, row := range legacy {
		var count int64
		if err := s.db.Model(&ChannelConfig{}).Where("host = ? AND type = ?", row.Host, ChannelSMTP).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			// A replacement already exists (for example after a crash between
			// the two writes). The legacy row must not remain active as a second
			// SMTP destination.
			if err := s.db.Delete(&SMTPConfig{}, row.ID).Error; err != nil {
				return fmt.Errorf("remove superseded SMTP configuration for %s: %w", row.Host, err)
			}
			continue
		}
		password := ""
		if len(row.EncryptedPassword) > 0 {
			plain, err := s.vault.Decrypt(row.EncryptedPassword, row.Host)
			if err != nil {
				return fmt.Errorf("migrate SMTP channel for %s: %w", row.Host, err)
			}
			password = string(plain)
			clear(plain)
		}
		events := make([]string, 0, 3)
		if row.NotifyUpdates {
			events = append(events, EventUpdateAvailable, EventUpdateSuccess)
		}
		if row.NotifyErrors {
			events = append(events, EventUpdateFailure)
		}
		if _, err := s.SaveChannel(row.Host, ChannelInput{
			Name: "SMTP", Type: ChannelSMTP, Enabled: row.Enabled, Server: row.Server, Port: row.Port,
			Security: row.Security, Username: row.Username, Password: password, FromAddress: row.FromAddress,
			Recipients: row.Recipients, Events: events,
		}); err != nil {
			return fmt.Errorf("migrate SMTP channel for %s: %w", row.Host, err)
		}
		if err := s.db.Delete(&SMTPConfig{}, row.ID).Error; err != nil {
			return fmt.Errorf("remove migrated SMTP configuration for %s: %w", row.Host, err)
		}
	}
	return nil
}

func (s *Service) SaveChannel(host string, input ChannelInput) (ChannelView, error) {
	input = normalizeChannelInput(input)
	var existing ChannelConfig
	if input.ID != 0 {
		if err := s.db.Where("id = ? AND host = ?", input.ID, host).First(&existing).Error; err != nil {
			return ChannelView{}, err
		}
	}
	var secrets channelSecrets
	if existing.ID != 0 {
		var err error
		secrets, err = s.decryptChannel(existing)
		if err != nil {
			return ChannelView{}, err
		}
		if existing.Type != input.Type {
			secrets = channelSecrets{}
		}
	}
	if input.ClearCredentials {
		secrets.Token = ""
		secrets.Username = ""
		secrets.Password = ""
	}
	if input.URL != "" {
		secrets.URL = input.URL
	}
	if input.Token != "" {
		secrets.Token = input.Token
	}
	if input.Username != "" {
		secrets.Username = input.Username
	}
	if input.Password != "" {
		secrets.Password = input.Password
	}
	if input.Type == ChannelSMTP {
		secrets.URL, secrets.Token = "", ""
		secrets.Server, secrets.Port, secrets.Security = input.Server, input.Port, input.Security
		secrets.FromAddress, secrets.Recipients = input.FromAddress, input.Recipients
	} else {
		secrets.Server, secrets.Port, secrets.Security = "", 0, ""
		secrets.FromAddress, secrets.Recipients = "", ""
	}
	if err := validateChannelInput(input, secrets); err != nil {
		return ChannelView{}, err
	}
	key := existing.SecretKey
	if key == "" {
		var err error
		key, err = randomChannelKey()
		if err != nil {
			return ChannelView{}, err
		}
	}
	plain, err := json.Marshal(secrets)
	if err != nil {
		return ChannelView{}, err
	}
	defer clear(plain)
	encrypted, err := s.vault.EncryptFor(plain, channelScope(host, key))
	if err != nil {
		return ChannelView{}, err
	}
	events := normalizeEventTypes(input.Events, input.NotifyUpdates, input.NotifyErrors)
	notifyUpdates := eventTypeEnabled(events, EventUpdateAvailable) || eventTypeEnabled(events, EventUpdateSuccess)
	notifyErrors := eventTypeEnabled(events, EventUpdateFailure)
	row := ChannelConfig{
		ID: existing.ID, CreatedAt: existing.CreatedAt, Host: host, Name: input.Name, Type: input.Type,
		Enabled: input.Enabled, Target: channelDisplayTarget(input.Type, secrets), Topic: input.Topic, Priority: input.Priority,
		Tags: input.Tags, AllowInsecureHTTP: input.AllowInsecureHTTP, NotifyUpdates: notifyUpdates,
		NotifyErrors: notifyErrors, EventTypes: strings.Join(events, "\n"), SecretKey: key, EncryptedConfig: encrypted,
	}
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "id"}},
			DoUpdates: clause.AssignmentColumns([]string{"name", "type", "enabled", "target", "topic", "priority", "tags", "allow_insecure_http", "notify_updates", "notify_errors", "event_types", "encrypted_config", "updated_at"}),
		}).Create(&row).Error; err != nil {
			return err
		}
		return tx.Where("host = ? AND channel_key = ?", host, channelStateKey(row.ID)).Delete(&ChannelNotificationState{}).Error
	}); err != nil {
		return ChannelView{}, err
	}
	return channelView(row, secrets), nil
}

func (s *Service) DeleteChannel(host string, id uint) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		result := tx.Where("id = ? AND host = ?", id, host).Delete(&ChannelConfig{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		return tx.Where("host = ? AND channel_key = ?", host, channelStateKey(id)).Delete(&ChannelNotificationState{}).Error
	})
}

func (s *Service) TestChannel(ctx context.Context, host string, id uint) error {
	row, secrets, err := s.loadChannel(host, id)
	if err != nil {
		return err
	}
	event := ChannelEvent{Kind: "test", Host: host, Title: "Dockman notification test", Message: "Dockman successfully connected to this notification channel.", Severity: "info", Time: time.Now()}
	return s.deliverChannel(ctx, row, secrets, event)
}

func (s *Service) loadChannel(host string, id uint) (ChannelConfig, channelSecrets, error) {
	var row ChannelConfig
	if err := s.db.Where("id = ? AND host = ?", id, host).First(&row).Error; err != nil {
		return row, channelSecrets{}, err
	}
	secrets, err := s.decryptChannel(row)
	return row, secrets, err
}

func (s *Service) decryptChannel(row ChannelConfig) (channelSecrets, error) {
	plain, err := s.vault.DecryptFor(row.EncryptedConfig, channelScope(row.Host, row.SecretKey))
	if err != nil {
		return channelSecrets{}, err
	}
	defer clear(plain)
	var secrets channelSecrets
	if err := json.Unmarshal(plain, &secrets); err != nil {
		return channelSecrets{}, errors.New("invalid encrypted notification channel configuration")
	}
	return secrets, nil
}

func (s *Service) deliverChannel(ctx context.Context, row ChannelConfig, secrets channelSecrets, event ChannelEvent) error {
	var err error
	if row.Type == ChannelSMTP {
		recipients, parseErr := parseRecipients(secrets.Recipients)
		if parseErr != nil {
			err = parseErr
		} else {
			err = s.sender.Send(ctx, SMTPMessage{Config: SMTPConfig{
				Host: row.Host, Enabled: row.Enabled, Server: secrets.Server, Port: secrets.Port,
				Security: secrets.Security, Username: secrets.Username, FromAddress: secrets.FromAddress,
				Recipients: secrets.Recipients,
			}, Password: secrets.Password, Recipients: recipients, Subject: event.Title, Body: event.Message})
		}
	} else {
		err = s.channelSender.Send(ctx, row, secrets, event)
	}
	delivery := Delivery{Host: row.Host, ChannelType: row.Type, ChannelName: row.Name, Kind: event.Kind, Subject: event.Title, Success: err == nil}
	if err != nil {
		delivery.Error = safeDeliveryError(err, secrets.URL, secrets.Token, secrets.Username, secrets.Password)
	}
	_ = s.recordDelivery(&delivery)
	if err != nil {
		return errors.New(delivery.Error)
	}
	return nil
}

func (s HTTPChannelSender) Send(ctx context.Context, config ChannelConfig, secrets channelSecrets, event ChannelEvent) error {
	requestURL, headers, body, err := channelRequest(config, secrets, event)
	if err != nil {
		return err
	}
	if len(body) > maxChannelPayload {
		return errors.New("notification payload exceeds 256 KiB")
	}
	client, err := secureNotificationClient(requestURL, config.AllowInsecureHTTP, s.Timeout)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("send %s notification: %w", config.Type, err)
	}
	defer resp.Body.Close()
	response, readErr := io.ReadAll(io.LimitReader(resp.Body, maxChannelResponse+1))
	if readErr != nil {
		return fmt.Errorf("read %s response: %w", config.Type, readErr)
	}
	if len(response) > maxChannelResponse {
		return fmt.Errorf("%s response exceeds 64 KiB", config.Type)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		detail := strings.TrimSpace(string(response))
		if len(detail) > 300 {
			detail = detail[:300]
		}
		return fmt.Errorf("%s returned HTTP %d%s", config.Type, resp.StatusCode, optionalDetail(detail))
	}
	return nil
}

func channelRequest(config ChannelConfig, secrets channelSecrets, event ChannelEvent) (string, map[string]string, []byte, error) {
	headers := map[string]string{"Content-Type": "application/json", "User-Agent": "Dockman/notification"}
	endpoint := strings.TrimRight(secrets.URL, "/")
	var payload any
	switch config.Type {
	case ChannelWebhook:
		payload = event
		if secrets.Token != "" {
			headers["Authorization"] = "Bearer " + secrets.Token
		} else if secrets.Username != "" {
			headers["Authorization"] = "Basic " + basicAuth(secrets.Username, secrets.Password)
		}
	case ChannelGotify:
		endpoint += "/message?token=" + url.QueryEscape(secrets.Token)
		priority := config.Priority
		if priority == 0 {
			priority = severityPriority(event.Severity, 2, 5, 8)
		}
		payload = map[string]any{"title": event.Title, "message": event.Message, "priority": priority}
	case ChannelNtfy:
		u, err := url.Parse(endpoint)
		if err != nil {
			return "", nil, nil, err
		}
		u.Path = path.Join(u.Path, config.Topic)
		endpoint = u.String()
		priority := config.Priority
		if priority == 0 {
			priority = severityPriority(event.Severity, 3, 4, 5)
		}
		payload = map[string]any{"topic": config.Topic, "title": event.Title, "message": event.Message, "priority": priority, "tags": splitTags(config.Tags)}
		if secrets.Token != "" {
			headers["Authorization"] = "Bearer " + secrets.Token
		} else if secrets.Username != "" {
			headers["Authorization"] = "Basic " + basicAuth(secrets.Username, secrets.Password)
		}
	case ChannelDiscord:
		payload = map[string]any{"username": "Dockman", "embeds": []map[string]any{{"title": event.Title, "description": truncateRunes(event.Message, 4000), "color": discordColor(event.Severity), "footer": map[string]string{"text": "Host: " + event.Host}}}}
	case ChannelApprise:
		endpoint += "/notify/" + url.PathEscape(secrets.Token)
		kind := event.Severity
		if kind == "error" {
			kind = "failure"
		}
		payload = map[string]any{"title": event.Title, "body": event.Message, "type": kind, "format": "text"}
		if config.Tags != "" {
			payload.(map[string]any)["tag"] = config.Tags
		}
	default:
		return "", nil, nil, errors.New("unsupported notification channel type")
	}
	body, err := json.Marshal(payload)
	return endpoint, headers, body, err
}

func secureNotificationClient(rawURL string, allowInsecure bool, timeout time.Duration) (*http.Client, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return nil, errors.New("notification endpoint is not a valid URL")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && allowInsecure) {
		return nil, errors.New("notification endpoint must use HTTPS; explicitly allow insecure HTTP only for a trusted private service")
	}
	if isForbiddenNotificationHostname(u.Hostname()) {
		return nil, errors.New("notification endpoint hostname is forbidden")
	}
	if ip := net.ParseIP(u.Hostname()); ip != nil && forbiddenNotificationIP(ip, allowInsecure) {
		return nil, errors.New("notification endpoint address is forbidden")
	}
	if timeout <= 0 || timeout > 30*time.Second {
		timeout = 10 * time.Second
	}
	dialer := &net.Dialer{Timeout: timeout}
	transport := &http.Transport{
		// Do not let an environment proxy bypass destination-IP validation or
		// receive notification credentials.
		Proxy: nil,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, err
			}
			ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
			if err != nil {
				return nil, err
			}
			for _, ip := range ips {
				if forbiddenNotificationIP(ip, allowInsecure) {
					continue
				}
				return dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			}
			return nil, errors.New("notification endpoint resolved only to forbidden addresses")
		},
		TLSHandshakeTimeout: 5 * time.Second,
		IdleConnTimeout:     15 * time.Second,
		DisableKeepAlives:   true,
	}
	return &http.Client{Timeout: timeout, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}

func forbiddenNotificationIP(ip net.IP, allowPrivate bool) bool {
	if ip == nil || ip.IsUnspecified() || ip.IsLoopback() || ip.IsMulticast() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return true
	}
	return ip.IsPrivate() && !allowPrivate
}

func isForbiddenNotificationHostname(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	return host == "localhost" || strings.HasSuffix(host, ".localhost") || host == "host.docker.internal" || strings.Contains(host, "metadata.google.internal")
}

func normalizeChannelInput(input ChannelInput) ChannelInput {
	input.Name = strings.TrimSpace(input.Name)
	input.Type = strings.ToLower(strings.TrimSpace(input.Type))
	input.URL = strings.TrimSpace(input.URL)
	input.Token = strings.TrimSpace(input.Token)
	input.Username = strings.TrimSpace(input.Username)
	input.Topic = strings.Trim(strings.TrimSpace(input.Topic), "/")
	input.Tags = strings.TrimSpace(input.Tags)
	input.Server = strings.TrimSpace(input.Server)
	input.Security = strings.ToLower(strings.TrimSpace(input.Security))
	input.FromAddress = strings.TrimSpace(input.FromAddress)
	input.Recipients = strings.Join(strings.FieldsFunc(input.Recipients, func(r rune) bool { return r == ',' || r == ';' || r == '\n' || r == '\r' }), ",")
	return input
}

func validateChannelInput(input ChannelInput, secrets channelSecrets) error {
	if input.Name == "" || len(input.Name) > 80 || strings.ContainsAny(input.Name, "\r\n\x00") {
		return errors.New("notification channel name is invalid")
	}
	if !validChannelType(input.Type) {
		return errors.New("unsupported notification channel type")
	}
	for _, eventType := range input.Events {
		if _, valid := validEventTypes[strings.TrimSpace(eventType)]; !valid {
			return fmt.Errorf("unsupported notification event type %q", eventType)
		}
	}
	if input.Type == ChannelSMTP {
		return validateInput(ConfigInput{
			Enabled: input.Enabled, Server: secrets.Server, Port: secrets.Port, Security: secrets.Security,
			Username: secrets.Username, Password: secrets.Password, FromAddress: secrets.FromAddress,
			Recipients: secrets.Recipients, NotifyUpdates: input.NotifyUpdates, NotifyErrors: input.NotifyErrors,
		})
	}
	if len(secrets.URL) > 4096 || len(secrets.Token) > 4096 || len(secrets.Username) > 320 || len(secrets.Password) > 4096 {
		return errors.New("notification channel credential is too long")
	}
	if _, err := secureNotificationClient(secrets.URL, input.AllowInsecureHTTP, 10*time.Second); err != nil {
		return err
	}
	if input.Type == ChannelDiscord {
		u, _ := url.Parse(secrets.URL)
		if !strings.EqualFold(u.Hostname(), "discord.com") && !strings.EqualFold(u.Hostname(), "discordapp.com") {
			return errors.New("Discord webhooks must use discord.com")
		}
		if !strings.HasPrefix(u.Path, "/api/webhooks/") {
			return errors.New("Discord webhook URL is invalid")
		}
	}
	if (input.Type == ChannelGotify || input.Type == ChannelApprise) && secrets.Token == "" {
		return errors.New(input.Type + " token or configuration key is required")
	}
	if input.Type == ChannelNtfy && input.Topic == "" {
		return errors.New("ntfy topic is required")
	}
	if input.Type == ChannelNtfy && (strings.Contains(input.Topic, "..") || strings.ContainsAny(input.Topic, "?#\\")) {
		return errors.New("ntfy topic is invalid")
	}
	if input.Type == ChannelNtfy && (input.Priority < 0 || input.Priority > 5) {
		return errors.New("ntfy priority must be between 0 and 5")
	}
	if input.Priority < 0 || input.Priority > 10 {
		return errors.New("notification priority must be between 0 and 10")
	}
	return nil
}

func validChannelType(value string) bool {
	return value == ChannelSMTP || value == ChannelWebhook || value == ChannelGotify || value == ChannelNtfy || value == ChannelDiscord || value == ChannelApprise
}

func channelView(row ChannelConfig, secrets channelSecrets) ChannelView {
	configured := secrets.URL != ""
	if row.Type == ChannelSMTP {
		configured = secrets.Server != "" && secrets.Port > 0 && secrets.FromAddress != "" && secrets.Recipients != ""
	}
	return ChannelView{ID: row.ID, Name: row.Name, Type: row.Type, Enabled: row.Enabled, Target: row.Target, Topic: row.Topic, Priority: row.Priority, Tags: row.Tags, AllowInsecureHTTP: row.AllowInsecureHTTP, NotifyUpdates: row.NotifyUpdates, NotifyErrors: row.NotifyErrors, Events: channelEventTypes(row), Server: secrets.Server, Port: secrets.Port, Security: secrets.Security, FromAddress: secrets.FromAddress, Recipients: secrets.Recipients, Configured: configured, HasToken: secrets.Token != "", HasUsername: secrets.Username != "", HasPassword: secrets.Password != "", UpdatedAt: row.UpdatedAt}
}

func channelDisplayTarget(channelType string, secrets channelSecrets) string {
	if channelType == ChannelSMTP {
		return net.JoinHostPort(secrets.Server, strconv.Itoa(secrets.Port))
	}
	return displayTarget(secrets.URL)
}

func normalizeEventTypes(values []string, notifyUpdates, notifyErrors bool) []string {
	seen := make(map[string]struct{})
	for _, value := range values {
		value = strings.TrimSpace(value)
		if _, valid := validEventTypes[value]; valid {
			seen[value] = struct{}{}
		}
	}
	// Backward-compatible clients only send the two original switches.
	if values == nil {
		if notifyUpdates {
			seen[EventUpdateAvailable], seen[EventUpdateSuccess] = struct{}{}, struct{}{}
		}
		if notifyErrors {
			seen[EventUpdateFailure] = struct{}{}
		}
	}
	result := make([]string, 0, len(seen))
	for value := range seen {
		result = append(result, value)
	}
	slices.Sort(result)
	return result
}

func channelEventTypes(row ChannelConfig) []string {
	if strings.TrimSpace(row.EventTypes) == "" {
		return normalizeEventTypes(nil, row.NotifyUpdates, row.NotifyErrors)
	}
	return normalizeEventTypes(strings.Fields(row.EventTypes), row.NotifyUpdates, row.NotifyErrors)
}

func randomChannelKey() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

func channelScope(host, key string) string { return "notification-channel/" + host + "/" + key }
func channelStateKey(id uint) string       { return "channel:" + strconv.FormatUint(uint64(id), 10) }

func displayTarget(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return "configured endpoint"
	}
	return u.Scheme + "://" + u.Host
}

func severityPriority(severity string, normal, warning, failure int) int {
	if severity == "error" {
		return failure
	}
	if severity == "warning" {
		return warning
	}
	return normal
}

func discordColor(severity string) int {
	switch severity {
	case "error":
		return 0xed4245
	case "warning":
		return 0xfee75c
	case "success":
		return 0x57f287
	default:
		return 0x3498db
	}
}

func splitTags(value string) []string {
	return strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ';' || r == ' ' })
}

func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit-1]) + "…"
}

func optionalDetail(value string) string {
	if value == "" {
		return ""
	}
	return ": " + value
}

func basicAuth(username, password string) string {
	request := &http.Request{Header: make(http.Header)}
	request.SetBasicAuth(username, password)
	return strings.TrimPrefix(request.Header.Get("Authorization"), "Basic ")
}
