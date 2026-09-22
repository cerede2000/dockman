package host

import (
	"github.com/RA341/dockman/internal/docker/compose"
	"github.com/RA341/dockman/internal/ssh"
	"gorm.io/gorm"
)

type Store interface {
	Get(Host string) (Config, error)
	// GetByID looks a host up by its primary key, which is the only handle
	// that survives a rename.
	GetByID(id uint) (Config, error)
	GetLocal() (Config, error)
	Add(conf *Config) error
	Delete(conf *Config) error
	Update(conf *Config) error
	List() ([]Config, error)
	ListEnabled() ([]Config, error)
}

type ClientType string

const (
	SSH   ClientType = "ssh"
	LOCAL ClientType = "local"
)

type Config struct {
	gorm.Model
	Name         string
	Type         ClientType
	Enable       bool   `gorm:"not null;default:false"`
	DockerSocket string `gorm:""`

	// Belongs To Relationship (SSHOptions)
	// If SSHID is 0, Preload will simply return nil for SSHOptions
	SSHID      uint                `gorm:"default:null"`
	SSHOptions *ssh.MachineOptions `gorm:"foreignKey:SSHID"`

	// Has Many Relationship (FolderAliases)
	FolderAliases []FolderAlias `gorm:"foreignKey:ConfigID"`
	MachineAddr   string

	// Build limits cap every image build Dockman runs on this host; 0 means
	// no limit (see compose.BuildLimits).
	BuildCPULimit    float64 `gorm:"column:build_cpu_limit;not null;default:0"`
	BuildMemoryLimit int64   `gorm:"column:build_memory_limit;not null;default:0"`
}

// BuildLimits is the host's build caps in the form the compose layer applies.
func (c *Config) BuildLimits() compose.BuildLimits {
	return compose.BuildLimits{CPUs: c.BuildCPULimit, MemoryBytes: c.BuildMemoryLimit}
}

func (*Config) TableName() string {
	return "host_config"
}
