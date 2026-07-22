package gitsync

import (
	"time"

	"gorm.io/gorm"
)

const (
	AuthPublic     = "public"
	AuthHTTPSToken = "https_token"
	AuthSSHKey     = "ssh_key"
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
	UUID           string `gorm:"not null;uniqueIndex"`
	Name           string `gorm:"not null;uniqueIndex"`
	Provider       string `gorm:"not null;default:generic"`
	RemoteURL      string `gorm:"not null"`
	DefaultBranch  string `gorm:"not null;default:main"`
	Mode           string `gorm:"not null;default:managed"`
	CredentialUUID *string
	Status         string `gorm:"not null;default:uninitialized"`
	LastError      string
	LastFetchedAt  *time.Time
}

func (Repository) TableName() string { return "git_repositories" }

type StackBinding struct {
	gorm.Model
	UUID           string `gorm:"not null;uniqueIndex"`
	RepositoryUUID string `gorm:"not null;index"`
	Host           string `gorm:"not null;uniqueIndex:idx_git_stack_binding_target"`
	StackPath      string `gorm:"not null;uniqueIndex:idx_git_stack_binding_target"`
	SubPath        string `gorm:"not null"`
	ComposePaths   string
	Enabled        bool `gorm:"not null;default:true"`
}

func (StackBinding) TableName() string { return "git_stack_bindings" }

type Operation struct {
	ID             uint `gorm:"primarykey"`
	CreatedAt      time.Time
	UpdatedAt      time.Time
	UUID           string `gorm:"not null;uniqueIndex"`
	RepositoryUUID string `gorm:"index"`
	BindingUUID    string
	OperationType  string `gorm:"not null"`
	State          string `gorm:"not null;index"`
	StartedAt      *time.Time
	FinishedAt     *time.Time
	ErrorMessage   string
}

func (Operation) TableName() string { return "git_operations" }

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
