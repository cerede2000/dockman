package host

import (
	"log"
	"os"
	"testing"

	"github.com/RA341/dockman/internal/database"
	"github.com/RA341/dockman/internal/ssh"
	"github.com/stretchr/testify/require"
)

const testDir = "test"

func Setup() (*Service, *ssh.MachineManagerDB) {
	err := os.MkdirAll(testDir, os.ModePerm)
	if err != nil {
		log.Fatal("could not create test folder", err)
	}

	db := database.New(testDir, true)
	macMan := ssh.NewGormMachineManger(db)
	keyMan := ssh.NewGormKeyManager(db)
	sshSrv := ssh.NewService(keyMan, macMan)
	st := NewStore(db)
	aliasStore := NewAliasStore(db)

	return NewService(st, aliasStore, sshSrv, testDir, ""), macMan
}

func TestAdd(t *testing.T) {
	defer os.RemoveAll(testDir)
	_, macStore := Setup()

	cond := Config{
		Name:         "test",
		Type:         SSH,
		Enable:       false,
		DockerSocket: "",
		SSHID:        0,
		SSHOptions: &ssh.MachineOptions{
			Host:             "",
			Port:             0,
			User:             "",
			Password:         "",
			RemotePublicKey:  "",
			UsePublicKeyAuth: false,
		},
		FolderAliases: nil,
	}

	err := macStore.Save(cond.SSHOptions)
	require.NoError(t, err)

	require.NotNil(t, cond.SSHOptions)
}
