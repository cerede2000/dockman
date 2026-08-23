package config

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"

	"github.com/RA341/dockman/pkg/argos"
	"github.com/RA341/dockman/pkg/fileutil"
)

type Service struct {
	store             Store
	updateUpdaterFunc func()
}

func NewService(store Store, updaterFunc func()) *Service {
	return &Service{
		store:             store,
		updateUpdaterFunc: updaterFunc,
	}
}

func (s *Service) GetConfig() (*UserConfig, error) {
	return s.store.GetConfig()
}

func (s *Service) SaveConfig(conf *UserConfig, updaterUpdater bool) error {
	err := s.store.SetConfig(conf)
	if err != nil {
		return err
	}

	if updaterUpdater {
		s.updateUpdaterFunc()
	}

	return nil
}

func Load(opts ...AppOpt) (*AppConfig, error) {
	config, err := parseStruct()
	if err != nil {
		return nil, err
	}

	for _, o := range opts {
		o(config)
	}
	defaultIfNotSet(config)

	argos.PrettyPrint(config, EnvPrefix)
	return config, nil
}

func parseStruct() (*AppConfig, error) {
	conf := &AppConfig{}
	if err := argos.Scan(conf, EnvPrefix); err != nil {
		return nil, err
	}
	flag.Parse()

	// ConfigDir holds the database and both master-key vaults - every stored
	// Git and notification credential lives under it. It is Dockman's alone, so
	// it is created private and tightened if an older release left it readable.
	//
	// ComposeRoot is deliberately NOT tightened: it is the directory the
	// operator edits from the host, and restricting it would lock them out of
	// their own stacks.
	pathsToResolve := []struct {
		target  *string
		mode    os.FileMode
		private bool
	}{
		{&conf.ConfigDir, 0o700, true},
		{&conf.ComposeRoot, 0o777, false},
		{&conf.DockYaml, 0o700, true},
	}
	for _, p := range pathsToResolve {
		absPath, err := filepath.Abs(*p.target)
		if err != nil {
			return nil, fmt.Errorf("failed to get abs path for %s: %w", *p.target, err)
		}
		*p.target = absPath

		if err = os.MkdirAll(absPath, p.mode); err != nil {
			return nil, err
		}
		if p.private {
			tightenPrivateDirectory(absPath)
		}
	}

	return conf, nil
}

// tightenPrivateDirectory restricts a directory that must stay Dockman's own.
//
// MkdirAll applies its mode only to directories it creates, so an installation
// that already exists keeps whatever mode an older release gave it - 0777
// masked by the umask, usually 0755, on a directory holding the database and
// the master keys. This is what repairs those on upgrade.
//
// It never fails startup. The container entrypoint chowns this directory to the
// user Dockman runs as before handing over, so tightening it cannot lock
// Dockman out of its own configuration; but if the directory turns out not to
// be ours to change, refusing to boot over a file mode would be worse than
// reporting it and carrying on.
func tightenPrivateDirectory(path string) {
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return
	}
	if info.Mode().Perm()&0o077 == 0 {
		return
	}
	if err := os.Chmod(path, 0o700); err != nil {
		fmt.Printf("WARNING: %s holds the database and master keys and is readable by other local accounts (%#o); restrict it with: chmod 700 %s\n", path, info.Mode().Perm(), path)
		return
	}
	fmt.Printf("Restricted %s to its owner: it holds the database and the master keys\n", path)
}

// final checks
func defaultIfNotSet(config *AppConfig) {
	uiPath := config.UIPath
	if uiPath != "" {
		if file, err := WithUIFromFile(uiPath); err == nil {
			config.UIFS = file
		}
	}

	if config.Port == 0 {
		config.Port = 8866
	}

	if config.LocalAddr == "0.0.0.0" {
		ip, err := getLocalIP()
		if err == nil {
			config.LocalAddr = ip
		}
	}

	if config.ServerContext == nil {
		config.ServerContext = context.Background()
	}
}

func getLocalIP() (string, error) {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return "", err
	}
	defer fileutil.Close(conn)

	localAddr := conn.LocalAddr().(*net.UDPAddr)
	return localAddr.IP.String(), nil
}
