package gitsync

import (
	"time"

	"gorm.io/gorm"
)

const (
	AuthPublic       = "public"
	AuthHTTPSToken   = "https_token"
	AuthSSHKey       = "ssh_key"
	githubAPIVersion = "2026-03-10"
)

type Credential struct {
	gorm.Model
	UUID             string `gorm:"not null;uniqueIndex"`
	Name             string `gorm:"not null;uniqueIndex"`
	AuthType         string `gorm:"not null"`
	Username         string
	EncryptedPayload []byte `gorm:"type:blob"`
	SecretHint       string
}

func (Credential) TableName() string { return "git_credentials" }

type Repository struct {
	gorm.Model
	UUID              string `gorm:"not null;uniqueIndex"`
	Name              string `gorm:"not null;uniqueIndex"`
	Provider          string `gorm:"not null;default:generic"`
	RemoteURL         string `gorm:"not null"`
	DefaultBranch     string `gorm:"not null;default:main"`
	CommitAuthorName  string `gorm:"not null;default:Dockman Git Sync"`
	CommitAuthorEmail string `gorm:"not null;default:dockman@localhost.invalid"`
	Mode              string `gorm:"not null;default:managed"`
	CredentialUUID    *string
	Status            string `gorm:"not null;default:uninitialized"`
	ExcludePatterns   string `gorm:"type:text"`
	LastError         string
	LastFetchedAt     *time.Time
}

func (Repository) TableName() string { return "git_repositories" }

type StackBinding struct {
	gorm.Model
	UUID                      string `gorm:"not null;uniqueIndex"`
	RepositoryUUID            string `gorm:"not null;index"`
	Host                      string `gorm:"not null;uniqueIndex:idx_git_stack_binding_target"`
	StackPath                 string `gorm:"not null;uniqueIndex:idx_git_stack_binding_target"`
	SubPath                   string `gorm:"not null"`
	ComposePaths              string
	ComposeSelectionMode      string `gorm:"not null;default:all"`
	SelectedComposePaths      string `gorm:"type:text"`
	SyncProfile               string `gorm:"not null;default:compose_config"`
	IncludePatterns           string `gorm:"type:text"`
	ExcludePatterns           string `gorm:"type:text"`
	Enabled                   bool   `gorm:"not null;default:true"`
	AutoSyncEnabled           bool   `gorm:"not null;default:false"`
	AutoSyncPaused            bool   `gorm:"not null;default:false"`
	AutoSyncIntervalMinutes   int    `gorm:"not null;default:15"`
	AutoSyncState             string `gorm:"not null;default:disabled"`
	AutoSyncError             string `gorm:"type:text"`
	LastAutoSyncAt            *time.Time
	LastAutoSyncSuccessAt     *time.Time
	LastAutoSyncCommit        string
	AutoDeployEnabled         bool   `gorm:"not null;default:false"`
	AutoDeployNewStacks       bool   `gorm:"not null;default:false"`
	AutoDeployRollbackEnabled bool   `gorm:"not null;default:false"`
	AutoDeployComposePaths    string `gorm:"type:text"`
	AutoDeployState           string `gorm:"not null;default:disabled"`
	AutoDeployError           string `gorm:"type:text"`
	LastAutoDeployAt          *time.Time
	InitialSyncState          string `gorm:"not null;default:pending"`
	InitialSyncError          string `gorm:"type:text"`
	InitialSyncAt             *time.Time
	AutoReconcileEnabled      bool `gorm:"not null"`
}

func (StackBinding) TableName() string { return "git_stack_bindings" }

type BindingBaseline struct {
	ID          uint `gorm:"primarykey"`
	CreatedAt   time.Time
	UpdatedAt   time.Time
	BindingUUID string `gorm:"not null;uniqueIndex:idx_git_binding_baseline_path"`
	Path        string `gorm:"not null;uniqueIndex:idx_git_binding_baseline_path"`
	SHA256      string `gorm:"not null"`
}

func (BindingBaseline) TableName() string { return "git_binding_baselines" }

type Operation struct {
	ID             uint `gorm:"primarykey"`
	CreatedAt      time.Time
	UpdatedAt      time.Time
	UUID           string `gorm:"not null;uniqueIndex"`
	RepositoryUUID string `gorm:"index"`
	BindingUUID    string `gorm:"index"`
	ComposePath    string
	Trigger        string `gorm:"not null;default:system"`
	Details        string `gorm:"type:text"`
	CommitSHA      string
	BackupUUID     string
	OperationType  string `gorm:"not null"`
	State          string `gorm:"not null;index"`
	StartedAt      *time.Time
	FinishedAt     *time.Time
	ErrorMessage   string
}

func (Operation) TableName() string { return "git_operations" }

type GitBackup struct {
	ID             uint `gorm:"primarykey"`
	CreatedAt      time.Time
	UpdatedAt      time.Time
	UUID           string `gorm:"not null;uniqueIndex"`
	RepositoryUUID string `gorm:"not null;index"`
	BindingUUID    string `gorm:"not null;index"`
	Kind           string `gorm:"not null"`
	ComposePaths   string `gorm:"type:text"`
	ArchivePath    string `gorm:"not null;uniqueIndex"`
	CommitSHA      string
	FileCount      int
	SizeBytes      int64
	Restorable     bool       `gorm:"not null;default:false"`
	ExpiresAt      *time.Time `gorm:"index"`
}

func (GitBackup) TableName() string { return "git_backups" }

type Deployment struct {
	ID             uint `gorm:"primarykey"`
	CreatedAt      time.Time
	UpdatedAt      time.Time
	UUID           string `gorm:"not null;uniqueIndex"`
	RepositoryUUID string `gorm:"not null"`
	BindingUUID    string `gorm:"not null;index"`
	CommitSHA      string `gorm:"not null"`
	ComposeHash    string
	State          string `gorm:"not null"`
	Result         string
	Logs           string
}

func (Deployment) TableName() string { return "git_deployments" }

// GitStackStatus is a compact, event-driven projection for UI status badges.
// It deliberately stores no file inventory: displaying status must never scan
// a stack directory or a Git repository.
type GitStackStatus struct {
	ID               uint `gorm:"primarykey"`
	CreatedAt        time.Time
	UpdatedAt        time.Time
	BindingUUID      string `gorm:"not null;uniqueIndex:idx_git_stack_status_target"`
	ComposePath      string `gorm:"not null;uniqueIndex:idx_git_stack_status_target"`
	State            string `gorm:"not null;default:pending;index"`
	ErrorMessage     string `gorm:"type:text"`
	ConflictCount    int    `gorm:"not null;default:0"`
	AutomationPaused bool   `gorm:"not null;default:false"`
	PauseReason      string `gorm:"not null;default:''"`
	LastCheckedAt    *time.Time
	LastSuccessAt    *time.Time
	LastCommit       string
	DeployState      string `gorm:"not null;default:disabled"`
	DeployError      string `gorm:"type:text"`
	LastDeployAt     *time.Time
}

func (GitStackStatus) TableName() string { return "git_stack_statuses" }
