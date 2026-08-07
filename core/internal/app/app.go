package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"time"

	"github.com/RA341/dockman/internal/app/middleware"
	"github.com/RA341/dockman/internal/app/ui"
	"github.com/RA341/dockman/internal/auth"
	"github.com/RA341/dockman/internal/cleaner"
	"github.com/RA341/dockman/internal/config"
	"github.com/RA341/dockman/internal/database"
	"github.com/RA341/dockman/internal/docker"
	"github.com/RA341/dockman/internal/docker/compose"
	"github.com/RA341/dockman/internal/docker/container"
	"github.com/RA341/dockman/internal/docker/updater"
	"github.com/RA341/dockman/internal/dockyaml"
	"github.com/RA341/dockman/internal/files"
	"github.com/RA341/dockman/internal/gitsync"
	"github.com/RA341/dockman/internal/host"
	"github.com/RA341/dockman/internal/host/filesystem"
	hostMiddleware "github.com/RA341/dockman/internal/host/middleware"
	"github.com/RA341/dockman/internal/info"
	"github.com/RA341/dockman/internal/notifications"
	"github.com/RA341/dockman/internal/secrets"
	"github.com/RA341/dockman/internal/ssh"
	"github.com/RA341/dockman/internal/viewer"
	"github.com/RA341/dockman/pkg/argos"
	"github.com/RA341/dockman/pkg/logger"
	"github.com/RA341/dockman/pkg/memlimit"
	"github.com/moby/moby/client"

	"github.com/rs/zerolog/log"
)

type App struct {
	Config *config.AppConfig

	Auth             *auth.Service
	HostManager      *host.Service
	File             *files.Service
	Info             *info.Service
	SSH              *ssh.Service
	UserConfigSrv    *config.Service
	CleanerSrv       *cleaner.Service
	Viewer           *viewer.Service
	DockYaml         *dockyaml.Service
	GitSync          *gitsync.Service
	UpdatePolicies   *updater.PolicyService
	UpdateAutomation *updater.AutomationService
	Notifications    *notifications.Service
	Secrets          *secrets.Service
}

func (a *App) VerifyServices() error {
	val := reflect.ValueOf(a).Elem()
	typ := val.Type()

	for i := 0; i < val.NumField(); i++ {
		field := val.Field(i)
		fieldName := typ.Field(i).Name

		// We only care about pointers (services)
		if field.Kind() == reflect.Ptr && field.IsNil() {
			return fmt.Errorf("critical error: service '%s' was not initialized", fieldName)
		}
	}
	return nil
}

func NewApp(opt ...config.AppOpt) (app *App) {
	conf, err := config.Load(opt...)
	if err != nil {
		log.Fatal().Err(err).Msg("Error parsing config")
	}

	logger.InitConsole(conf.Log.Level, conf.Log.Verbose)

	// Cap the Go heap to the container's cgroup memory limit. The runtime does
	// not do this on its own, so without it a transient spike inflates RSS and
	// stays resident. No-op outside a memory-limited container.
	memlimit.Configure()

	// db and info setup
	gormDB := database.New(conf.ConfigDir, info.IsDev())
	userDb := config.NewUserConfigDB(gormDB)
	infoDb := info.NewVersionHistoryManager(gormDB)
	infoSrv := info.NewService(infoDb)

	// auth setup
	sessionsDB := auth.NewSessionGormDB(gormDB, uint(conf.Auth.MaxSessions))
	authDB := auth.NewUserGormDB(gormDB)
	authSrv := auth.NewService(
		conf.Auth.Username,
		conf.Auth.Password,
		&conf.Auth,
		authDB,
		sessionsDB,
	)

	setupComposeRoot(conf.ComposeRoot)

	// docker manager setup
	sshDb := ssh.NewGormKeyManager(gormDB)
	machDb := ssh.NewGormMachineManger(gormDB)
	sshSrv := ssh.NewService(sshDb, machDb)

	dockyamlPath := filepath.Join(conf.ConfigDir, "dockyaml")
	if conf.DockYaml != "" {
		dockyamlPath = conf.DockYaml
	}

	store := dockyaml.NewStore(dockyamlPath)
	dockyamlSrv := dockyaml.New(store)

	aliasStore := host.NewAliasStore(gormDB)
	hostStore := host.NewStore(gormDB)
	hostManager := host.NewService(
		hostStore,
		aliasStore,
		sshSrv,
		conf.ComposeRoot,
		conf.LocalAddr,
	)

	// best-effort: remove a leftover self-update helper container from a
	// previous update once the local docker host is reachable.
	if dkSrv, err := hostManager.GetDockerService(host.LocalDocker); err == nil {
		docker.CleanupSelfUpdateHelper(context.Background(), dkSrv.Container.Cli())
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		docker.CleanupFileBrowserHelpers(cleanupCtx, dkSrv.Container.Cli())
		cancel()
	}

	fileSrv := files.New(
		hostManager.GetAlias,
		dockyamlSrv.GetYaml,
	)
	secretResolver := func(hostname, stackPath string) (filesystem.FileSystem, string, error) {
		stackFS, relpath, _, loadErr := fileSrv.LoadAll(stackPath, hostname)
		return stackFS, relpath, loadErr
	}
	secretRuntimeStore := secrets.NewPlainFileStore(secretResolver)
	secretRuntimeStore.ConfigureAliases(func(hostname string) ([]string, error) {
		aliases, listErr := hostManager.ListAliases(hostname)
		if listErr != nil {
			return nil, listErr
		}
		names := make([]string, 0, len(aliases))
		for _, alias := range aliases {
			names = append(names, alias.Alias)
		}
		return names, nil
	})
	secretSrv := secrets.NewService(secretRuntimeStore)
	sopsProvider := secrets.NewSOPSProvider(
		secretRuntimeStore,
		secretResolver,
		conf.SOPSBinary,
		conf.SOPSAgeKeyFile,
		conf.SOPSAgeRecipient,
	)
	sopsProvider.ConfigureRuntimeMountVerifier(func(ctx context.Context, hostname, absolutePath string) (bool, error) {
		sshClient, getErr := hostManager.GetSSH(hostname)
		if getErr != nil {
			return false, getErr
		}
		if sshClient == nil {
			return secrets.IsManagedHostRuntimeMount(absolutePath)
		}
		session, sessionErr := sshClient.NewSession()
		if sessionErr != nil {
			return false, fmt.Errorf("verify remote volatile secret runtime: %w", sessionErr)
		}
		defer session.Close()
		quotedPath := "'" + strings.ReplaceAll(absolutePath, "'", "'\"'\"'") + "'"
		command := "test \"$(findmnt -rn -o FSTYPE,SOURCE --target " + quotedPath + " 2>/dev/null)\" = \"tmpfs dockman-secrets\""
		if runErr := session.Run(command); runErr != nil {
			return false, nil
		}
		return true, nil
	})
	secretSrv.ConfigureSOPS(sopsProvider)
	hostManager.ConfigureComposeEnvironment(secretSrv.ComposeEnvironment)

	//err := git.NewMigrator(composeRoot)
	//if err != nil {
	//	log.Fatal().Err(err).Msg("unable to complete git migration")
	//}

	userConfigSrv := config.NewService(
		userDb,
		func() {},
	)

	cleanerStore := cleaner.NewStore(gormDB)
	cleanerSrv := cleaner.NewService(
		hostManager.GetDockerService,
		cleanerStore,
	)
	updatePolicySrv := updater.NewPolicyService(updater.NewPolicyStore(gormDB))
	notificationVault, notificationKeyPath, err := notifications.LoadOrCreateVault(conf.ConfigDir, conf.NotificationMasterKeyFile)
	if err != nil {
		log.Fatal().Err(err).Msg("unable to initialize notification credential encryption")
	}
	if conf.NotificationMasterKeyFile == "" {
		log.Warn().Str("path", notificationKeyPath).Msg("notification master key was generated locally; mount NOTIFICATION_MASTER_KEY_FILE as a Docker secret for production")
	}
	notificationSrv := notifications.NewService(gormDB, notificationVault)
	if err := notificationSrv.MigrateLegacySMTPConfigs(); err != nil {
		// One malformed legacy row must not keep the whole server down: every
		// other feature is unaffected, and an operator locked out of Dockman
		// cannot repair the row either. Notifications simply keep their
		// pre-migration configuration until it is fixed.
		log.Error().Err(err).Msg("legacy SMTP notification configuration was not migrated; existing notification channels are unaffected")
	}
	notificationSrv.StartDispatcher(conf.ServerContext)
	cleanerSrv.SetNotifier(func(_ context.Context, result cleaner.PruneResult, automated bool) {
		kind, severity, title := notifications.EventCleanerSuccess, "success", "Docker cleaner completed"
		parts := []string{fmt.Sprintf("Host: %s", result.Host)}
		if automated {
			parts = append(parts, "Trigger: scheduled")
		} else {
			parts = append(parts, "Trigger: manual")
		}
		failed := result.Err != ""
		if result.Err != "" {
			parts = append(parts, "Error: "+result.Err)
		}
		for _, item := range []struct {
			name  string
			value cleaner.OpResult
		}{
			{"Containers", result.Containers}, {"Images", result.Images}, {"Volumes", result.Volumes},
			{"Networks", result.Networks}, {"Build cache", result.BuildCache},
		} {
			if item.value.Err != "" {
				failed = true
				parts = append(parts, item.name+": "+item.value.Err)
			} else if item.value.Success != "" {
				parts = append(parts, item.name+": "+item.value.Success)
			}
		}
		if failed {
			kind, severity, title = notifications.EventCleanerFailure, "error", "Docker cleaner failed"
		}
		notificationSrv.Enqueue(notifications.ChannelEvent{Kind: kind, Host: result.Host, Title: title, Message: strings.Join(parts, "\n"), Severity: severity, Time: time.Now().UTC()})
	})
	updateAutomationSrv, err := updater.NewAutomationService(
		updater.NewScanStore(gormDB),
		func(ctx context.Context, hostname string) ([]updater.UpdateEnrollment, error) {
			dkSrv, getErr := hostManager.GetDockerService(hostname)
			if getErr != nil {
				return nil, getErr
			}
			containers, listErr := dkSrv.Container.ContainersList(ctx)
			if listErr != nil {
				return nil, listErr
			}
			return updatePolicySrv.Inventory(ctx, hostname, containers)
		},
		func(ctx context.Context, hostname string, containerIDs []string) ([]updater.ContainerUpdateCheck, error) {
			dkSrv, getErr := hostManager.GetDockerService(hostname)
			if getErr != nil {
				return nil, getErr
			}
			return dkSrv.Updater.CheckContainerUpdatesByID(ctx, containerIDs)
		},
	)
	if err != nil {
		log.Fatal().Err(err).Msg("unable to initialize automatic image scan scheduler")
	}
	versionDiscovery := updater.NewRegistryVersionDiscovery(6 * time.Hour)
	updateAutomationSrv.SetVersionDiscoverer(versionDiscovery.Discover)
	updateAutomationSrv.SetExecutor(func(ctx context.Context, hostname string, targets []updater.UpdateExecutionTarget) []updater.UpdateExecutionOutcome {
		dkSrv, getErr := hostManager.GetDockerService(hostname)
		if getErr != nil {
			outcomes := make([]updater.UpdateExecutionOutcome, 0, len(targets))
			for _, target := range targets {
				outcomes = append(outcomes, updater.UpdateExecutionOutcome{UpdateExecutionTarget: target, State: updater.ExecutionFailed, Message: getErr.Error()})
			}
			return outcomes
		}
		return docker.ExecuteAutomaticContainerUpdates(ctx, dkSrv, targets)
	})
	updateAutomationSrv.SetImageCleaner(func(ctx context.Context, hostname, imageID string) (bool, string, error) {
		dkSrv, getErr := hostManager.GetDockerService(hostname)
		if getErr != nil {
			return false, "", getErr
		}
		return docker.RemovePreviousImageIfUnused(ctx, dkSrv, imageID)
	})
	updateAutomationSrv.SetNotifier(notificationSrv.NotifyScan)
	updateAutomationSrv.SetExecutionNotifier(notificationSrv.NotifyExecution)
	go func() {
		<-conf.ServerContext.Done()
		if shutdownErr := updateAutomationSrv.Shutdown(); shutdownErr != nil {
			log.Warn().Err(shutdownErr).Msg("unable to stop automatic image scan scheduler cleanly")
		}
	}()

	gitStore := gitsync.NewStore(gormDB)
	var gitVault *gitsync.Vault
	if conf.GitSyncEnabled {
		var keyPath string
		gitVault, keyPath, err = gitsync.LoadOrCreateVault(conf.ConfigDir, conf.GitMasterKeyFile)
		if err != nil {
			log.Fatal().Err(err).Msg("unable to initialize Git credential encryption")
		}
		if conf.GitMasterKeyFile == "" {
			log.Warn().Str("path", keyPath).Msg("Git master key was generated locally; mount GIT_MASTER_KEY_FILE as a Docker secret for production")
		}
	}
	gitWorkspaceRoot, gitBackupRoot, err := conf.GetGitStorageRoots()
	if err != nil {
		log.Fatal().Err(err).Msg("invalid Git storage configuration")
	}
	gitSyncSrv := gitsync.NewService(conf.GitSyncEnabled, gitStore, gitVault, gitWorkspaceRoot)
	gitSyncSrv.ConfigureEventNotifier(func(event gitsync.AutomationEvent) {
		notificationSrv.Enqueue(notifications.ChannelEvent{Kind: event.Kind, Host: event.Host, Title: event.Title, Message: event.Message, Severity: event.Severity, Time: time.Now().UTC()})
	})
	gitSyncSrv.ConfigureCommitProvenance(conf.GitCommitInstance)
	if err := gitSyncSrv.ConfigureRetention(conf.GitHistoryRetentionDays, conf.GitBackupRetentionDays); err != nil {
		log.Fatal().Err(err).Msg("invalid Git retention configuration")
	}
	if err := gitSyncSrv.InitializeGitStackStatuses(); err != nil {
		log.Fatal().Err(err).Msg("unable to initialize compact Git stack status index")
	}
	fileSrv.ConfigureChangeNotifier(gitSyncSrv.MarkLocalChange)
	fileSrv.ConfigureDeleteGuard(gitSyncSrv.GuardFileDeletion)
	gitSyncSrv.ConfigureEditorCoherence(fileSrv.DirtyEditorPaths, fileSrv.NotifyExternalChange)
	gitSyncSrv.ConfigureStackAccess(
		func(hostname, stackPath string) (filesystem.FileSystem, string, error) {
			stackFS, relpath, _, loadErr := fileSrv.LoadAll(stackPath, hostname)
			return stackFS, relpath, loadErr
		},
		hostManager.ListConnected,
		gitBackupRoot,
	)
	gitSyncSrv.ConfigureDeployment(
		func(ctx context.Context, hostname, filename string) error {
			dkSrv, getErr := hostManager.GetDockerService(hostname)
			if getErr != nil {
				return getErr
			}
			if validation := dkSrv.Compose.Validate(ctx, filename); len(validation) > 0 {
				return errors.Join(validation...)
			}
			return nil
		},
		func(ctx context.Context, hostname, filename string, out io.Writer) error {
			dkSrv, getErr := hostManager.GetDockerService(hostname)
			if getErr != nil {
				return getErr
			}
			return dkSrv.Compose.DryRunUp(ctx, filename, out)
		},
		func(ctx context.Context, hostname, filename string, out io.Writer) error {
			dkSrv, getErr := hostManager.GetDockerService(hostname)
			if getErr != nil {
				return getErr
			}
			return dkSrv.Compose.Up(ctx, filename, out)
		},
		func(ctx context.Context, hostname, filename string, out io.Writer) error {
			dkSrv, getErr := hostManager.GetDockerService(hostname)
			if getErr != nil {
				return getErr
			}
			return dkSrv.Compose.UpWait(ctx, filename, out)
		},
		func(ctx context.Context, hostname, filename string, out io.Writer) error {
			dkSrv, getErr := hostManager.GetDockerService(hostname)
			if getErr != nil {
				return getErr
			}
			return dkSrv.Compose.DownPlain(ctx, filename, out)
		},
		compose.TryLockStack,
	)
	if interrupted, recoverErr := gitSyncSrv.RecoverInterruptedOperations(); recoverErr != nil {
		log.Fatal().Err(recoverErr).Msg("unable to recover interrupted Git operations")
	} else if interrupted > 0 {
		log.Warn().Int64("operations", interrupted).Msg("marked interrupted Git operations as failed")
	}
	gitSyncSrv.StartAutomation(conf.ServerContext)
	gitSyncSrv.StartWebhookWorker(conf.ServerContext)

	viewerSrv := viewer.New(
		hostManager.GetDockerService,
		func(input, host string) (root string, relpath string, err error) {
			fs, relpath, _, err := fileSrv.LoadAll(input, host)
			if err != nil {
				return "", "", err
			}

			join := filepath.Join(fs.Root(), relpath)
			return join, relpath, nil
		},
		hostManager.GetSSH,
		&conf.Viewer,
	)

	app = &App{
		Config:           conf,
		Auth:             authSrv,
		File:             fileSrv,
		HostManager:      hostManager,
		Info:             infoSrv,
		DockYaml:         dockyamlSrv,
		SSH:              sshSrv,
		UserConfigSrv:    userConfigSrv,
		CleanerSrv:       cleanerSrv,
		Viewer:           viewerSrv,
		GitSync:          gitSyncSrv,
		UpdatePolicies:   updatePolicySrv,
		UpdateAutomation: updateAutomationSrv,
		Notifications:    notificationSrv,
		Secrets:          secretSrv,
	}
	err = app.VerifyServices()
	if err != nil {
		log.Fatal().Err(err).Msg("error occurred while verifying services")
	}
	for _, hostname := range hostManager.ListConnected() {
		updateAutomationSrv.RefreshHost(hostname)
		dkSrv, getErr := hostManager.GetDockerService(hostname)
		if getErr != nil {
			log.Warn().Err(getErr).Str("host", hostname).Msg("unable to watch container policy changes")
			continue
		}
		events, unsubscribe := dkSrv.Container.SubscribeEvents()
		go watchUpdatePolicyEvents(conf.ServerContext, hostname, events, unsubscribe, updateAutomationSrv, notificationSrv, dkSrv.Container)
	}

	log.Info().Msg("Dockman initialized successfully")
	return app
}

func watchUpdatePolicyEvents(ctx context.Context, hostname string, events <-chan container.Event, unsubscribe func(), automation *updater.AutomationService, notificationSrv *notifications.Service, containers *container.Service) {
	defer unsubscribe()
	var timer *time.Timer
	var timerC <-chan time.Time
	restartCounts := make(map[string]int)
	lastRestartNotification := make(map[string]time.Time)
	notify := func(event container.Event, kind, title, severity, detail string) {
		message := fmt.Sprintf("Host: %s\nContainer: %s\nImage: %s", hostname, event.Name, event.Image)
		if detail != "" {
			message += "\n" + detail
		}
		notificationSrv.Enqueue(notifications.ChannelEvent{Kind: kind, Host: hostname, Title: title, Message: message, Severity: severity, Time: time.Now().UTC()})
	}
	inspectRestartCount := func(event container.Event) (int, bool) {
		inspectCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		inspected, err := containers.Cli().ContainerInspect(inspectCtx, event.ID, client.ContainerInspectOptions{})
		if err != nil || inspected.Container.State == nil {
			return 0, false
		}
		return inspected.Container.RestartCount, true
	}
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-events:
			if !ok {
				return
			}
			switch event.Action {
			case "create", "destroy", "rename", "update":
				if timer == nil {
					timer = time.NewTimer(2 * time.Second)
				} else {
					if !timer.Stop() {
						select {
						case <-timer.C:
						default:
						}
					}
					timer.Reset(2 * time.Second)
				}
				timerC = timer.C
			}
			switch event.Action {
			case "oom":
				notify(event, notifications.EventContainerOOM, "Container terminated by OOM", "error", "Docker reported an out-of-memory event.")
			case "health_status":
				if event.Status == "unhealthy" {
					notify(event, notifications.EventContainerUnhealthy, "Container became unhealthy", "error", "Health status: unhealthy")
				}
			case "restart":
				lastRestartNotification[event.ID] = time.Now()
				notify(event, notifications.EventContainerRestart, "Container restarted", "warning", "Docker reported a restart action.")
			case "die":
				if count, found := inspectRestartCount(event); found {
					restartCounts[event.ID] = count
				}
			case "start":
				if count, found := inspectRestartCount(event); found {
					previous, known := restartCounts[event.ID]
					restartCounts[event.ID] = count
					if known && count > previous && time.Since(lastRestartNotification[event.ID]) > 10*time.Second {
						lastRestartNotification[event.ID] = time.Now()
						notify(event, notifications.EventContainerRestart, "Container restarted automatically", "warning", fmt.Sprintf("Restart count: %d", count))
					}
				}
			case "destroy":
				delete(restartCounts, event.ID)
				delete(lastRestartNotification, event.ID)
			}
		case <-timerC:
			timerC = nil
			automation.RefreshHost(hostname)
		}
	}
}

func setupComposeRoot(composeRoot string) (cr string) {
	var err error
	if !filepath.IsAbs(composeRoot) {
		composeRoot, err = filepath.Abs(composeRoot)
		if err != nil {
			log.Fatal().
				Str("path", composeRoot).
				Msg("Err getting abs path for composeRoot")
		}
	}

	err = os.MkdirAll(composeRoot, 0755)
	if err != nil {
		log.Fatal().Err(err).
			Str("compose-root", composeRoot).
			Msg("failed to create compose root folder")
	}

	return composeRoot
}

/*
Registers all routes required by dockman with the following hierarchy

	/ <- UI files
	/api
	|-----/ <- public endpoints
	|-----/auth <- auth endpoints
	|-----/protec <- protected paths
	|-----|-----/ 	     <- normal endpoints
	|-----|-----/:host/* <- endpoints require hosts info
*/
func (a *App) registerRoutes(mux *http.ServeMux) {
	apiRouter := http.NewServeMux()
	a.registerApiRoutes(apiRouter)
	withSubRouter(mux, "/api", a.withLogger(apiRouter))

	a.registerFrontend(mux)
}

func (a *App) registerFrontend(router *http.ServeMux) {
	if a.Config.UIProxy != nil {
		log.Info().Msg("using ui proxy")
		router.Handle("/", a.Config.UIProxy)
		return
	}

	if a.Config.UIFS != nil {
		log.Info().Msg("using ui fs")
		router.Handle("/", middleware.Gzip(ui.NewSpaHandler(a.Config.UIFS)))
		return
	}

	log.Info().Msg("no ui files found, setting default page")
	router.Handle("/", middleware.Gzip(ui.NewDefaultUIHandler()))
}

func (a *App) registerApiAuthRoutes(authRouter *http.ServeMux) {
	if !a.Config.Auth.Enable {
		return
	}

	authRouter.Handle(auth.NewConnectHandler(a.Auth))

	if a.Config.Auth.OIDCEnable {
		withSubRouter(
			authRouter,
			"/login",
			auth.NewHandlerHttp(a.Auth),
		)
	}
}

func (a *App) registerApiRoutes(publicApiMux *http.ServeMux) {
	publicApiMux.HandleFunc(
		"/hello",
		func(writer http.ResponseWriter, request *http.Request) {
			_, err := writer.Write([]byte("Fuck off"))
			if err != nil {
				return
			}
		},
	)

	// /auth
	authRouter := http.NewServeMux()
	a.registerApiAuthRoutes(authRouter)
	withSubRouter(publicApiMux, "/auth", authRouter)
	withSubRouter(publicApiMux, "/git-webhooks", gitsync.NewWebhookHandler(a.GitSync))

	protectedApiMux := http.NewServeMux()
	a.registerApiProtectedRoutes(protectedApiMux)

	withSubRouter(
		publicApiMux,
		"/protected",
		a.withAuth(protectedApiMux),
	)
}

// /api/protected
func (a *App) registerApiProtectedRoutes(protectedApiMux *http.ServeMux) {
	// penger
	protectedApiMux.HandleFunc(
		"/ping",
		func(w http.ResponseWriter, r *http.Request) {
			_, err := w.Write([]byte("pong"))
			if err != nil {
				log.Warn().Err(err).Msg("unable to write pong")
				return
			}
		},
	)

	// info
	protectedApiMux.Handle(info.NewConnectHandler(a.Info))
	// user config
	protectedApiMux.Handle(config.NewConnectHandler(a.UserConfigSrv))
	// host manager
	protectedApiMux.Handle(host.NewHandler(a.HostManager))

	// viewer http doesnt need hosts uses uuid
	withSubRouter(
		protectedApiMux,
		"/viewer",
		viewer.NewHandlerHttp(a.Viewer),
	)
	withSubRouter(
		protectedApiMux,
		"/git",
		gitsync.NewHTTPHandler(a.GitSync),
	)

	// /:host
	// Host-specific sub-router
	hostMux := http.NewServeMux()
	a.registerApiHostRoutes(hostMux)
	protectedApiMux.Handle(
		"/{host}/",
		a.HostPathMiddleware(hostMux),
	)
}

func withSubRouter(parent *http.ServeMux, path string, child http.Handler) {
	if strings.HasSuffix(path, "/") {
		panic(fmt.Sprintf("path must not end with /: %s", path))
	}

	basepath := path + "/"
	parent.Handle(
		basepath,
		http.StripPrefix(path, child),
	)
}

// /api/protected/:host
func (a *App) registerApiHostRoutes(hostMux *http.ServeMux) {
	// dockyaml
	hostMux.Handle(dockyaml.NewHandler(a.DockYaml))

	// files
	hostMux.Handle(files.NewHandler(a.File))
	// files http handlers
	hostMux.Handle(
		"/file/",
		http.StripPrefix("/file",
			files.NewFileHandler(a.File),
		),
	)
	// docker
	hostMux.Handle(
		docker.NewConnectHandler(
			a.HostManager.GetDockerService,
		),
	)
	// docker http
	withSubRouter(
		hostMux,
		"/docker",
		docker.NewHandlerHttpWithUpdates(a.HostManager.GetDockerService, a.Config.AllowSelfExec, a.UpdatePolicies, a.UpdateAutomation, a.Notifications),
	)
	// Runtime Compose secrets are host-scoped. The middleware-selected host is
	// resolved again for every operation, so local and SSH hosts never share
	// state or filesystem handles.
	withSubRouter(hostMux, "/secrets", secrets.NewHTTPHandler(a.Secrets))
	// cleaner
	hostMux.Handle(cleaner.NewHandler(a.CleanerSrv))
	// viewer
	hostMux.Handle(viewer.NewHandler(a.Viewer))
}

func (a *App) HostPathMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hostname := r.PathValue("host")
		if hostname == "" {
			http.Error(w, "Hostname is missing: "+r.URL.String(), http.StatusBadRequest)
			return
		}

		hostCtx := hostMiddleware.SetHost(
			r.Context(),
			hostname,
		)

		// Strip the dynamic prefix
		// Eg: /server-1/files.FileService/GetFile
		// Becomes: /files.FileService/GetFile
		prefix := "/" + hostname
		strippedHandler := http.StripPrefix(prefix, next)
		strippedHandler.ServeHTTP(w, r.WithContext(hostCtx))
	})
}

func (a *App) withLogger(mux http.Handler) http.Handler {
	var apiHandler = mux
	if a.Config.Log.HttpLogger {
		apiHandler = middleware.LoggingMiddleware(apiHandler)
	}
	return apiHandler
}

func (a *App) withAuth(mux http.Handler) http.Handler {
	if !a.Config.Auth.Enable {
		if !info.IsDev() && a.Config.Log.AuthWarning {
			printAuthWarning()
		}

		return mux
	}
	return auth.Middleware(a.Auth, mux)
}

// visualWidth calculates the length of the string without ANSI color codes
func visualWidth(s string) int {
	re := regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)
	plain := re.ReplaceAllString(s, "")
	return len(plain)
}

func printAuthWarning() {
	boxWidth := 50

	// List of message lines
	messages := []string{
		argos.Colorize("Caution: Authentication Disabled", argos.ColorYellow),
		argos.Colorize("Running without auth is fine for testing", argos.ColorCyan),
		argos.Colorize("but you should enable it before exposing Dockman", argos.ColorCyan),
		argos.Colorize("to a network or using it regularly.", argos.ColorCyan),
		"",
		argos.Colorize("Why this matters:", argos.ColorYellow),
		argos.Colorize("Dockman has root-level access to manage", argos.ColorCyan),
		argos.Colorize("Docker containers and resources.", argos.ColorCyan),
		"",
		argos.Colorize("Guide:", argos.ColorYellow),
		argos.Colorize("https://dockman.radn.dev/docs/authentication/", argos.ColorGreen),
	}

	var sb strings.Builder
	topBorder := argos.Colorize("╔"+strings.Repeat("═", boxWidth+4)+"╗", argos.ColorRed)
	bottomBorder := argos.Colorize("╚"+strings.Repeat("═", boxWidth+4)+"╝", argos.ColorRed)
	emptyLine := argos.Colorize("║"+argos.ColorReset+strings.Repeat(" ", boxWidth+4)+argos.ColorRed+"║", argos.ColorRed)

	sb.WriteString("\n" + topBorder + "\n")
	sb.WriteString(emptyLine + "\n")

	for _, msg := range messages {
		if msg == "" {
			sb.WriteString(emptyLine + "\n")
			continue
		}

		// Calculate padding
		contentWidth := visualWidth(msg)
		padding := boxWidth - contentWidth
		if padding < 0 {
			padding = 0
		}

		// Border + 2 space margin + Text + Remaining Padding + Border
		sb.WriteString(argos.Colorize("║", argos.ColorRed))
		sb.WriteString("  " + msg + strings.Repeat(" ", padding+2))
		sb.WriteString(argos.Colorize("║"+argos.ColorReset+"\n", argos.ColorRed))
	}

	sb.WriteString(emptyLine + "\n")
	sb.WriteString(bottomBorder + "\n\n")

	fmt.Print(sb.String())
}
