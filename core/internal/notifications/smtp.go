package notifications

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/RA341/dockman/internal/docker/updater"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	SecuritySTARTTLS = "starttls"
	SecurityTLS      = "tls"
	SecurityNone     = "none"
	maxDeliveries    = 50
)

type SMTPConfig struct {
	ID                uint      `gorm:"primaryKey" json:"-"`
	CreatedAt         time.Time `json:"createdAt"`
	UpdatedAt         time.Time `json:"updatedAt"`
	Host              string    `gorm:"not null;uniqueIndex" json:"host"`
	Enabled           bool      `gorm:"not null" json:"enabled"`
	Server            string    `gorm:"not null" json:"server"`
	Port              int       `gorm:"not null" json:"port"`
	Security          string    `gorm:"not null" json:"security"`
	Username          string    `gorm:"not null;default:''" json:"username"`
	EncryptedPassword []byte    `json:"-"`
	FromAddress       string    `gorm:"not null" json:"fromAddress"`
	Recipients        string    `gorm:"not null" json:"recipients"`
	NotifyUpdates     bool      `gorm:"not null" json:"notifyUpdates"`
	NotifyErrors      bool      `gorm:"not null" json:"notifyErrors"`
}

func (SMTPConfig) TableName() string { return "update_smtp_configs" }

type NotificationState struct {
	ID                       uint `gorm:"primaryKey"`
	UpdatedAt                time.Time
	Host                     string `gorm:"not null;uniqueIndex:idx_update_notification_state"`
	Schedule                 string `gorm:"not null;uniqueIndex:idx_update_notification_state"`
	LastAvailableFingerprint string `gorm:"not null;default:''"`
	LastErrorFingerprint     string `gorm:"not null;default:''"`
}

func (NotificationState) TableName() string { return "update_notification_states" }

type Delivery struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	CreatedAt time.Time `json:"createdAt"`
	Host      string    `gorm:"not null;index" json:"host"`
	Kind      string    `gorm:"not null" json:"kind"`
	Subject   string    `gorm:"not null" json:"subject"`
	Success   bool      `gorm:"not null" json:"success"`
	Error     string    `gorm:"not null;default:''" json:"error,omitempty"`
}

func (Delivery) TableName() string { return "update_notification_deliveries" }

type ConfigInput struct {
	Enabled       bool   `json:"enabled"`
	Server        string `json:"server"`
	Port          int    `json:"port"`
	Security      string `json:"security"`
	Username      string `json:"username"`
	Password      string `json:"password,omitempty"`
	FromAddress   string `json:"fromAddress"`
	Recipients    string `json:"recipients"`
	NotifyUpdates bool   `json:"notifyUpdates"`
	NotifyErrors  bool   `json:"notifyErrors"`
}

type ConfigView struct {
	ConfigInput
	HasPassword bool      `json:"hasPassword"`
	Configured  bool      `json:"configured"`
	UpdatedAt   time.Time `json:"updatedAt,omitempty"`
}

type Sender interface {
	Send(context.Context, SMTPMessage) error
}

type SMTPMessage struct {
	Config     SMTPConfig
	Password   string
	Recipients []string
	Subject    string
	Body       string
}

type Service struct {
	db     *gorm.DB
	vault  *Vault
	sender Sender
}

func NewService(db *gorm.DB, vault *Vault) *Service {
	return &Service{db: db, vault: vault, sender: NetworkSender{Timeout: 20 * time.Second}}
}

func NewServiceWithSender(db *gorm.DB, vault *Vault, sender Sender) *Service {
	return &Service{db: db, vault: vault, sender: sender}
}

func (s *Service) Get(host string) (ConfigView, []Delivery, error) {
	var row SMTPConfig
	err := s.db.Where("host = ?", host).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ConfigView{ConfigInput: ConfigInput{Port: 587, Security: SecuritySTARTTLS, NotifyUpdates: true, NotifyErrors: true}}, nil, nil
	}
	if err != nil {
		return ConfigView{}, nil, err
	}
	var deliveries []Delivery
	if err := s.db.Where("host = ?", host).Order("created_at DESC").Limit(maxDeliveries).Find(&deliveries).Error; err != nil {
		return ConfigView{}, nil, err
	}
	return view(row), deliveries, nil
}

func (s *Service) Save(host string, input ConfigInput) (ConfigView, error) {
	input = normalizeInput(input)
	if err := validateInput(input); err != nil {
		return ConfigView{}, err
	}
	var existing SMTPConfig
	err := s.db.Where("host = ?", host).First(&existing).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return ConfigView{}, err
	}
	password := existing.EncryptedPassword
	if input.Username != "" && input.Password == "" && len(password) == 0 {
		return ConfigView{}, errors.New("an SMTP password is required for this username")
	}
	if input.Password != "" {
		password, err = s.vault.Encrypt([]byte(input.Password), host)
		if err != nil {
			return ConfigView{}, err
		}
	}
	if input.Username == "" {
		password = nil
	}
	row := SMTPConfig{
		Host: host, Enabled: input.Enabled, Server: input.Server, Port: input.Port, Security: input.Security,
		Username: input.Username, EncryptedPassword: password, FromAddress: input.FromAddress,
		Recipients: input.Recipients, NotifyUpdates: input.NotifyUpdates, NotifyErrors: input.NotifyErrors,
	}
	if existing.ID != 0 {
		row.ID, row.CreatedAt = existing.ID, existing.CreatedAt
	}
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "host"}},
			DoUpdates: clause.AssignmentColumns([]string{"enabled", "server", "port", "security", "username", "encrypted_password", "from_address", "recipients", "notify_updates", "notify_errors", "updated_at"}),
		}).Create(&row).Error; err != nil {
			return err
		}
		return tx.Where("host = ?", host).Delete(&NotificationState{}).Error
	}); err != nil {
		return ConfigView{}, err
	}
	return view(row), nil
}

func (s *Service) Test(ctx context.Context, host string) error {
	row, err := s.loadEnabled(host, false)
	if err != nil {
		return err
	}
	subject := fmt.Sprintf("[Dockman] SMTP test for %s", safeHeaderValue(host))
	body := fmt.Sprintf("Dockman successfully connected to this SMTP configuration.\n\nHost: %s\nSent: %s\n", host, time.Now().Format(time.RFC3339))
	err = s.deliver(ctx, row, "test", subject, body)
	return err
}

func (s *Service) NotifyScan(ctx context.Context, run updater.UpdateScanRun, checks []updater.ContainerUpdateCheck) error {
	if run.Trigger != "scheduled" {
		return nil
	}
	row, err := s.loadEnabled(run.Host, true)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	available, failures := relevantChecks(checks)
	if run.Error != "" {
		failures = append(failures, "scan: "+run.Error)
	}
	state, err := s.loadState(run.Host, run.Schedule)
	if err != nil {
		return err
	}
	availableFingerprint := fingerprint(available)
	errorFingerprint := fingerprint(failures)
	notifyAvailable := row.NotifyUpdates && availableFingerprint != "" && availableFingerprint != state.LastAvailableFingerprint
	notifyErrors := row.NotifyErrors && errorFingerprint != "" && errorFingerprint != state.LastErrorFingerprint
	if !notifyAvailable && !notifyErrors {
		return s.resetResolvedFingerprints(&state, availableFingerprint, errorFingerprint)
	}
	messageAvailable := available
	if !notifyAvailable {
		messageAvailable = nil
	}
	messageFailures := failures
	if !notifyErrors {
		messageFailures = nil
	}
	subject := notificationSubject(run.Host, len(messageAvailable), len(messageFailures))
	body := notificationBody(run, messageAvailable, messageFailures)
	if err := s.deliver(ctx, row, "scan", subject, body); err != nil {
		return err
	}
	if row.NotifyUpdates {
		state.LastAvailableFingerprint = availableFingerprint
	}
	if row.NotifyErrors {
		state.LastErrorFingerprint = errorFingerprint
	}
	return s.saveState(&state)
}

// NotifyExecution sends one bounded summary for a scheduled automatic update
// batch. Failed digests are circuit-broken, so this does not repeat on every
// cron tick unless the operator explicitly retries or a new digest appears.
func (s *Service) NotifyExecution(ctx context.Context, run updater.UpdateExecutionRun, outcomes []updater.UpdateExecutionOutcome) error {
	row, err := s.loadEnabled(run.Host, true)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	var successes, failures []string
	for _, outcome := range outcomes {
		item := fmt.Sprintf("%s | %s | %s", outcome.ContainerName, outcome.Image, outcome.State)
		switch outcome.State {
		case updater.ExecutionUpdated, updater.ExecutionCurrent:
			if row.NotifyUpdates {
				successes = append(successes, item)
			}
		case updater.ExecutionFailed, updater.ExecutionRolledBack:
			if row.NotifyErrors {
				failures = append(failures, item+" | "+outcome.Message)
			}
		}
	}
	if len(successes) == 0 && len(failures) == 0 {
		return nil
	}
	slices.Sort(successes)
	slices.Sort(failures)
	subject := fmt.Sprintf("[Dockman] automatic updates on %s · %d updated · %d failed", safeHeaderValue(run.Host), run.Updated, run.Failed+run.RolledBack)
	completed := time.Now()
	if run.CompletedAt != nil {
		completed = *run.CompletedAt
	}
	var body strings.Builder
	fmt.Fprintf(&body, "Dockman automatic image update summary\n\nHost: %s\nSchedule: %s\nCompleted: %s\n", run.Host, run.Schedule, completed.Format(time.RFC3339))
	if len(successes) > 0 {
		fmt.Fprintf(&body, "\nSuccessful (%d):\n", len(successes))
		for _, item := range successes {
			fmt.Fprintf(&body, "- %s\n", item)
		}
	}
	if len(failures) > 0 {
		fmt.Fprintf(&body, "\nFailed or rolled back (%d):\n", len(failures))
		for _, item := range failures {
			fmt.Fprintf(&body, "- %s\n", item)
		}
		body.WriteString("\nThe same failed digest is blocked until acknowledged in Dockman or replaced upstream.\n")
	}
	return s.deliver(ctx, row, "update", subject, body.String())
}

func (s *Service) loadState(host, schedule string) (NotificationState, error) {
	var state NotificationState
	err := s.db.Where("host = ? AND schedule = ?", host, schedule).First(&state).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return NotificationState{Host: host, Schedule: schedule}, nil
	}
	return state, err
}

func (s *Service) saveState(state *NotificationState) error {
	return s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "host"}, {Name: "schedule"}},
		DoUpdates: clause.AssignmentColumns([]string{"last_available_fingerprint", "last_error_fingerprint", "updated_at"}),
	}).Create(state).Error
}

func (s *Service) resetResolvedFingerprints(state *NotificationState, available, failures string) error {
	updates := map[string]any{}
	if available == "" && state.LastAvailableFingerprint != "" {
		updates["last_available_fingerprint"] = ""
	}
	if failures == "" && state.LastErrorFingerprint != "" {
		updates["last_error_fingerprint"] = ""
	}
	if len(updates) == 0 {
		return nil
	}
	if state.ID == 0 {
		return nil
	}
	return s.db.Model(&NotificationState{}).Where("id = ?", state.ID).Updates(updates).Error
}

func (s *Service) loadEnabled(host string, requireEnabled bool) (SMTPConfig, error) {
	var row SMTPConfig
	if err := s.db.Where("host = ?", host).First(&row).Error; err != nil {
		return row, err
	}
	if requireEnabled && !row.Enabled {
		return row, gorm.ErrRecordNotFound
	}
	return row, nil
}

func (s *Service) deliver(ctx context.Context, row SMTPConfig, kind, subject, body string) error {
	password := ""
	var err error
	if len(row.EncryptedPassword) > 0 {
		plain, decryptErr := s.vault.Decrypt(row.EncryptedPassword, row.Host)
		if decryptErr != nil {
			err = decryptErr
		} else {
			password = string(plain)
			defer func() { clear(plain) }()
		}
	}
	recipients, parseErr := parseRecipients(row.Recipients)
	if err == nil {
		err = parseErr
	}
	if err == nil {
		err = s.sender.Send(ctx, SMTPMessage{Config: row, Password: password, Recipients: recipients, Subject: subject, Body: body})
	}
	delivery := Delivery{Host: row.Host, Kind: kind, Subject: subject, Success: err == nil}
	if err != nil {
		delivery.Error = safeDeliveryError(err, password)
	}
	_ = s.recordDelivery(&delivery)
	return err
}

func (s *Service) recordDelivery(delivery *Delivery) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(delivery).Error; err != nil {
			return err
		}
		var stale []Delivery
		if err := tx.Where("host = ?", delivery.Host).Order("created_at DESC").Offset(maxDeliveries).Find(&stale).Error; err != nil {
			return err
		}
		if len(stale) > 0 {
			ids := make([]uint, 0, len(stale))
			for _, item := range stale {
				ids = append(ids, item.ID)
			}
			return tx.Delete(&Delivery{}, ids).Error
		}
		return nil
	})
}

type NetworkSender struct{ Timeout time.Duration }

func (s NetworkSender) Send(ctx context.Context, message SMTPMessage) error {
	from, err := mail.ParseAddress(message.Config.FromAddress)
	if err != nil {
		return fmt.Errorf("format SMTP sender: %w", err)
	}
	address := net.JoinHostPort(message.Config.Server, strconv.Itoa(message.Config.Port))
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	dialer := &net.Dialer{Timeout: timeout}
	var conn net.Conn
	err = nil
	tlsConfig := &tls.Config{ServerName: message.Config.Server, MinVersion: tls.VersionTLS12}
	if message.Config.Security == SecurityTLS {
		conn, err = (&tls.Dialer{NetDialer: dialer, Config: tlsConfig}).DialContext(ctx, "tcp", address)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", address)
	}
	if err != nil {
		return fmt.Errorf("connect SMTP server: %w", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	client, err := smtp.NewClient(conn, message.Config.Server)
	if err != nil {
		return fmt.Errorf("initialize SMTP session: %w", err)
	}
	defer client.Close()
	if err := client.Hello(smtpClientHostname(from.Address)); err != nil {
		return fmt.Errorf("initialize SMTP greeting: %w", err)
	}
	if message.Config.Security == SecuritySTARTTLS {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return errors.New("SMTP server does not advertise STARTTLS")
		}
		if err := client.StartTLS(tlsConfig); err != nil {
			return fmt.Errorf("start SMTP TLS: %w", err)
		}
	}
	if message.Config.Username != "" {
		if err := client.Auth(smtp.PlainAuth("", message.Config.Username, message.Password, message.Config.Server)); err != nil {
			return fmt.Errorf("authenticate to SMTP server: %w", err)
		}
	}
	if err := client.Mail(from.Address); err != nil {
		return fmt.Errorf("set SMTP sender: %w", err)
	}
	for _, recipient := range message.Recipients {
		if err := client.Rcpt(recipient); err != nil {
			return fmt.Errorf("set SMTP recipient: %w", err)
		}
	}
	writer, err := client.Data()
	if err != nil {
		return fmt.Errorf("start SMTP message: %w", err)
	}
	buffer := bufio.NewWriter(writer)
	payload, payloadErr := formatSMTPMessage(message, time.Now(), newMessageID(from.Address))
	if payloadErr != nil {
		_ = writer.Close()
		return payloadErr
	}
	_, err = buffer.WriteString(payload)
	if err == nil {
		err = buffer.Flush()
	}
	closeErr := writer.Close()
	if err != nil {
		return fmt.Errorf("write SMTP message: %w", err)
	}
	if closeErr != nil {
		return fmt.Errorf("finish SMTP message: %w", closeErr)
	}
	if err := client.Quit(); err != nil {
		return fmt.Errorf("finish SMTP session: %w", err)
	}
	return nil
}

// formatSMTPMessage emits a standards-compliant transactional message. In
// particular, Message-ID and MIME-safe address/subject headers keep Dockman
// notifications from looking like malformed bulk mail to receiving filters.
func formatSMTPMessage(message SMTPMessage, sentAt time.Time, messageID string) (string, error) {
	from, err := mail.ParseAddress(message.Config.FromAddress)
	if err != nil {
		return "", fmt.Errorf("format SMTP sender: %w", err)
	}
	if strings.TrimSpace(from.Name) == "" {
		from.Name = "Dockman"
	}
	body := strings.ReplaceAll(message.Body, "\r\n", "\n")
	body = strings.ReplaceAll(body, "\r", "\n")
	var encodedBody bytes.Buffer
	quoted := quotedprintable.NewWriter(&encodedBody)
	if _, err := quoted.Write([]byte(body)); err != nil {
		return "", fmt.Errorf("encode SMTP body: %w", err)
	}
	if err := quoted.Close(); err != nil {
		return "", fmt.Errorf("finish SMTP body encoding: %w", err)
	}
	return fmt.Sprintf(
		"Date: %s\r\nMessage-ID: %s\r\nFrom: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n%s\r\n",
		sentAt.Format(time.RFC1123Z), messageID, from.String(), strings.Join(message.Recipients, ", "),
		mime.QEncoding.Encode("UTF-8", safeHeaderValue(message.Subject)), encodedBody.String(),
	), nil
}

// smtpClientHostname avoids the default "EHLO localhost" emitted by net/smtp.
// Authenticated submission relays expose that value in Received headers, and
// public mailbox providers can penalize a generic localhost identity. The
// verified sender domain is stable and aligned with the message identity.
func smtpClientHostname(fromAddress string) string {
	if at := strings.LastIndexByte(fromAddress, '@'); at >= 0 && at+1 < len(fromAddress) {
		if domain := strings.Trim(strings.TrimSpace(fromAddress[at+1:]), ">."); domain != "" {
			return domain
		}
	}
	return "dockman.local"
}

func newMessageID(fromAddress string) string {
	domain := "dockman.local"
	if at := strings.LastIndexByte(fromAddress, '@'); at >= 0 && at+1 < len(fromAddress) {
		domain = strings.Trim(strings.TrimSpace(fromAddress[at+1:]), ">")
	}
	random := make([]byte, 12)
	if _, err := rand.Read(random); err != nil {
		sum := sha256.Sum256([]byte(fmt.Sprintf("%s:%d", fromAddress, time.Now().UnixNano())))
		random = sum[:12]
	}
	return fmt.Sprintf("<%s@%s>", hex.EncodeToString(random), domain)
}

func normalizeInput(input ConfigInput) ConfigInput {
	input.Server = strings.TrimSpace(input.Server)
	input.Security = strings.ToLower(strings.TrimSpace(input.Security))
	input.Username = strings.TrimSpace(input.Username)
	input.FromAddress = strings.TrimSpace(input.FromAddress)
	input.Recipients = strings.Join(strings.FieldsFunc(input.Recipients, func(r rune) bool { return r == ',' || r == ';' || r == '\n' || r == '\r' }), ",")
	return input
}

func validateInput(input ConfigInput) error {
	if input.Server == "" || len(input.Server) > 253 || strings.ContainsAny(input.Server, "\r\n\x00/@") {
		return errors.New("enter a valid SMTP server hostname or IP address")
	}
	if input.Port < 1 || input.Port > 65535 {
		return errors.New("SMTP port must be between 1 and 65535")
	}
	if input.Security != SecuritySTARTTLS && input.Security != SecurityTLS && input.Security != SecurityNone {
		return errors.New("SMTP security must be STARTTLS, TLS, or none")
	}
	if input.Security == SecurityNone && (input.Username != "" || input.Password != "") {
		return errors.New("SMTP authentication is refused without transport encryption")
	}
	if len(input.Username) > 320 || strings.ContainsAny(input.Username, "\r\n\x00") {
		return errors.New("SMTP username is invalid")
	}
	if len(input.Password) > 4096 {
		return errors.New("SMTP password is too long")
	}
	if _, err := mail.ParseAddress(input.FromAddress); err != nil || strings.ContainsAny(input.FromAddress, "\r\n") {
		return errors.New("SMTP sender address is invalid")
	}
	recipients, err := parseRecipients(input.Recipients)
	if err != nil || len(recipients) == 0 {
		return errors.New("enter at least one valid notification recipient")
	}
	if len(recipients) > 25 {
		return errors.New("at most 25 notification recipients are allowed")
	}
	return nil
}

func parseRecipients(value string) ([]string, error) {
	parts := strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ';' || r == '\n' || r == '\r' })
	recipients := make([]string, 0, len(parts))
	for _, part := range parts {
		parsed, err := mail.ParseAddress(strings.TrimSpace(part))
		if err != nil || strings.ContainsAny(part, "\r\n") {
			return nil, errors.New("notification recipient is invalid")
		}
		recipients = append(recipients, parsed.Address)
	}
	return recipients, nil
}

func view(row SMTPConfig) ConfigView {
	return ConfigView{ConfigInput: ConfigInput{Enabled: row.Enabled, Server: row.Server, Port: row.Port, Security: row.Security, Username: row.Username, FromAddress: row.FromAddress, Recipients: row.Recipients, NotifyUpdates: row.NotifyUpdates, NotifyErrors: row.NotifyErrors}, HasPassword: len(row.EncryptedPassword) > 0, Configured: true, UpdatedAt: row.UpdatedAt}
}

func relevantChecks(checks []updater.ContainerUpdateCheck) ([]string, []string) {
	var available, failures []string
	for _, check := range checks {
		switch check.Status {
		case updater.ContainerUpdateAvailable:
			available = append(available, fmt.Sprintf("%s | %s | %s", check.ContainerName, check.Image, check.RemoteDigest))
		case updater.ContainerUpdateError:
			failures = append(failures, fmt.Sprintf("%s | %s | %s", check.ContainerName, check.Image, check.Reason))
		}
	}
	slices.Sort(available)
	slices.Sort(failures)
	return available, failures
}

func fingerprint(values []string) string {
	if len(values) == 0 {
		return ""
	}
	sum := sha256.Sum256([]byte(strings.Join(values, "\n")))
	return hex.EncodeToString(sum[:])
}

func notificationSubject(host string, updates, failures int) string {
	parts := make([]string, 0, 2)
	if updates > 0 {
		parts = append(parts, fmt.Sprintf("%d update(s)", updates))
	}
	if failures > 0 {
		parts = append(parts, fmt.Sprintf("%d scan error(s)", failures))
	}
	return fmt.Sprintf("[Dockman] %s on %s", strings.Join(parts, " · "), safeHeaderValue(host))
}

func notificationBody(run updater.UpdateScanRun, available, failures []string) string {
	var body strings.Builder
	completed := time.Now()
	if run.CompletedAt != nil {
		completed = *run.CompletedAt
	}
	fmt.Fprintf(&body, "Dockman scheduled image scan summary\n\nHost: %s\nSchedule: %s\nCompleted: %s\nTargets: %d\n", run.Host, run.Schedule, completed.Format(time.RFC3339), run.Targets)
	if len(available) > 0 {
		fmt.Fprintf(&body, "\nUpdates available (%d):\n", len(available))
		for _, item := range available {
			fmt.Fprintf(&body, "- %s\n", item)
		}
	}
	if len(failures) > 0 {
		fmt.Fprintf(&body, "\nScan errors (%d):\n", len(failures))
		for _, item := range failures {
			fmt.Fprintf(&body, "- %s\n", item)
		}
	}
	body.WriteString("\nNo container was updated. Open Dockman Updates or Monitor to review and act.\n")
	return body.String()
}

func safeHeaderValue(value string) string {
	return strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' || r == 0 {
			return -1
		}
		return r
	}, value)
}

func safeDeliveryError(err error, secrets ...string) string {
	value := strings.TrimSpace(err.Error())
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		value = strings.ReplaceAll(value, secret, "[redacted]")
		value = strings.ReplaceAll(value, base64.StdEncoding.EncodeToString([]byte(secret)), "[redacted]")
	}
	if len(value) > 1000 {
		value = value[:1000]
	}
	return value
}
