package config

import (
	"context"
	"fmt"
	"io/fs"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/RA341/dockman/internal/auth"
	"github.com/RA341/dockman/internal/viewer"
)

const EnvPrefix = "DOCKMAN"

// AppConfig tags are parsed by processStruct
type AppConfig struct {
	Port                      int    `config:"flag=port,env=PORT,default=8866,usage=Port to run the server on"`
	AllowedOrigins            string `config:"flag=origins,env=ORIGINS,default=,usage=Extra allowed browser origins in CSV; same-origin is always allowed"`
	HTTPMaxBodyMB             int    `config:"flag=httpMaxBodyMB,env=HTTP_MAX_BODY_MB,default=16,usage=Maximum request body size in MiB (0 disables the limit)"`
	HTTPMaxUploadMB           int    `config:"flag=httpMaxUploadMB,env=HTTP_MAX_UPLOAD_MB,default=1024,usage=Maximum file upload size in MiB (0 disables the limit)"`
	HTTPReadHeaderSeconds     int    `config:"flag=httpReadHeaderTimeout,env=HTTP_READ_HEADER_TIMEOUT,default=10,usage=HTTP header read timeout in seconds"`
	HTTPIdleSeconds           int    `config:"flag=httpIdleTimeout,env=HTTP_IDLE_TIMEOUT,default=120,usage=HTTP keep-alive idle timeout in seconds"`
	AllowSelfExec             bool   `config:"flag=allowSelfExec,env=ALLOW_SELF_EXEC,default=false,usage=Allow exec sessions inside Dockman containers (unsafe; troubleshooting only)"`
	GitSyncEnabled            bool   `config:"flag=gitSync,env=GIT_SYNC,default=false,usage=Enable the experimental Git synchronization foundation"`
	GitMasterKeyFile          string `config:"flag=gitMasterKeyFile,env=GIT_MASTER_KEY_FILE,default=,usage=Path to the 32-byte or base64 Git credential encryption key"`
	GitStoragePath            string `config:"flag=gitStoragePath,env=GIT_STORAGE_PATH,default=,usage=Optional dedicated directory for Git repository objects and backups"`
	GitHistoryRetentionDays   int    `config:"flag=gitHistoryRetentionDays,env=GIT_HISTORY_RETENTION_DAYS,default=30,usage=Days to retain Git synchronization activity history"`
	GitBackupRetentionDays    int    `config:"flag=gitBackupRetentionDays,env=GIT_BACKUP_RETENTION_DAYS,default=30,usage=Days to retain Git synchronization backups"`
	GitCommitInstance         string `config:"flag=gitCommitInstance,env=GIT_COMMIT_INSTANCE,default=dockman,usage=Stable instance name recorded in Git commit provenance"`
	DeployTrace               bool   `config:"flag=deployTrace,env=DEPLOY_TRACE,default=false,usage=Record every controlled deployment stage, its duration, its context state and why a rollback ran; written to the log and to the deployment output shown in the UI"`
	NotificationMasterKeyFile string `config:"flag=notificationMasterKeyFile,env=NOTIFICATION_MASTER_KEY_FILE,default=,usage=Path to the 32-byte or base64 notification credential encryption key"`
	SOPSBinary                string `config:"flag=sopsBinary,env=SOPS_BINARY,default=sops,usage=Path to the SOPS executable"`
	SOPSAgeKeyFile            string `config:"flag=sopsAgeKeyFile,env=SOPS_AGE_KEY_FILE,default=,usage=Path to an independently backed-up age identity file used by SOPS"`
	SOPSAgeRecipient          string `config:"flag=sopsAgeRecipient,env=SOPS_AGE_RECIPIENT,default=,usage=Public age recipient used to encrypt stack secret sources"`
	UIPath                    string `config:"flag=ui,env=UI_PATH,default=dist,usage=Path to frontend files"`
	LocalAddr                 string `config:"flag=ma,env=MACHINE_ADDR,default=0.0.0.0,usage=Local machine IP address"`
	ComposeRoot               string `config:"flag=cr,env=COMPOSE_ROOT,default=./compose,usage=Root directory for compose files"`
	ConfigDir                 string `config:"flag=conf,env=CONFIG,default=./config,usage=Directory to store dockman config"`
	DockYaml                  string `config:"flag=dyp,env=YAML_PATH,default=./config/dockyaml,usage=custom path for dockman.yml files"`

	Auth   auth.Config     `config:""` // empty tag to indicate to parse struct
	Log    Logger          `config:""`
	Viewer viewer.Config   `config:""`
	Certs  SelfSignedCerts `config:""`

	UIFS          fs.FS
	ServerContext context.Context
	UIProxy       http.Handler
}

func (c *AppConfig) GetAllowedOrigins() []string {
	var origins []string
	for _, elem := range strings.Split(c.AllowedOrigins, ",") {
		if origin := strings.TrimSpace(elem); origin != "" {
			origins = append(origins, strings.TrimSuffix(origin, "/"))
		}
	}
	return origins
}

func (c *AppConfig) GetGitStorageRoots() (string, string, error) {
	configuredRoot := strings.TrimSpace(c.GitStoragePath)
	if configuredRoot == "" {
		return filepath.Join(c.ConfigDir, "git", "repositories"), filepath.Join(c.ConfigDir, "git", "backups"), nil
	}
	if !filepath.IsAbs(configuredRoot) {
		return "", "", fmt.Errorf("Git storage path must be absolute: %s", configuredRoot)
	}
	configuredRoot = filepath.Clean(configuredRoot)
	if filepath.Dir(configuredRoot) == configuredRoot {
		return "", "", fmt.Errorf("Git storage path cannot be a filesystem root: %s", configuredRoot)
	}
	return filepath.Join(configuredRoot, "repositories"), filepath.Join(configuredRoot, "backups"), nil
}

func (c *AppConfig) GetDockmanWithMachineUrl() string {
	return fmt.Sprintf("http://%s:%d", c.LocalAddr, c.Port)
}

type SelfSignedCerts struct {
	PublicCertPath string `config:"flag=sscPub,env=PUB_CERT_PATH,default=,usage=path to a public cert"`
	PrivateKeyPath string `config:"flag=sscPrv,env=PRIV_KEY_PATH,default=,usage=path to a private cert"`
}

func (c SelfSignedCerts) IsSet() bool {
	return c.PublicCertPath != "" && c.PrivateKeyPath != ""
}

type Logger struct {
	Level       string `config:"flag=logLevel,env=LOG_LEVEL,default=info,usage=disabled|debug|info|warn|error|fatal"`
	Verbose     bool   `config:"flag=logVerbose,env=LOG_VERBOSE,default=false,usage=show more info in logs"`
	HttpLogger  bool   `config:"flag=logHttp,env=LOG_HTTP,default=false,usage=enable printing of http routes logs"`
	AuthWarning bool   `config:"flag=logAuthWarn,env=LOG_AUTH_WARNING,default=true,usage=display auth warning at app startup"`
}
