package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"ops-deploy-platform/internal/access"
	"ops-deploy-platform/internal/appconfig"
	"ops-deploy-platform/internal/auth"
	"ops-deploy-platform/internal/awscatalog"
	"ops-deploy-platform/internal/awscredentials"
	"ops-deploy-platform/internal/cicd"
	"ops-deploy-platform/internal/componentcatalog"
	"ops-deploy-platform/internal/dataservicecredentials"
	"ops-deploy-platform/internal/environment"
	"ops-deploy-platform/internal/gitlab"
	"ops-deploy-platform/internal/httpapi"
	"ops-deploy-platform/internal/jobs"
	"ops-deploy-platform/internal/kubetunnel"
	"ops-deploy-platform/internal/persistence"
	"ops-deploy-platform/internal/resourcecenter"
	"ops-deploy-platform/internal/runner"
	"ops-deploy-platform/internal/statebackend"
	"ops-deploy-platform/internal/staticcdn"
	statusservice "ops-deploy-platform/internal/status"
	"ops-deploy-platform/internal/tlscertificates"
)

func main() {
	syscall.Umask(0o077)
	if err := run(); err != nil {
		log.Printf("error: %v", err)
		os.Exit(1)
	}
}

func run() error {
	action := "serve"
	if len(os.Args) > 1 && os.Args[1][0] != '-' {
		action = os.Args[1]
		os.Args = append([]string{os.Args[0]}, os.Args[2:]...)
	}
	flags := flag.NewFlagSet("ops-deploy", flag.ContinueOnError)
	configPath := flags.String("config", "config.yaml", "manager configuration file")
	projectName := flags.String("project", "ops", "project key for CLI actions")
	environmentName := flags.String("environment", "test", "environment name for CLI actions")
	catalogRegion := flags.String("region", "ap-south-1", "AWS region for catalog queries")
	catalogService := flags.String("service", "", "AWS service catalog: rds-mysql, rds-postgres, documentdb, elasticache, msk, or amazon-mq")
	catalogEngineVersion := flags.String("engine-version", "", "optional database engine version filter")
	initOutput := flags.String("output", "", "init action output path; defaults to .env next to the configuration file")
	if err := flags.Parse(os.Args[1:]); err != nil {
		return err
	}
	if action == "hash-password" {
		return hashPasswordCLI()
	}
	if action == "init" {
		return initEnvironmentCLI(*configPath, *initOutput)
	}
	if err := loadDotEnv(filepath.Join(filepath.Dir(*configPath), ".env")); err != nil {
		return err
	}

	config, err := appconfig.Load(*configPath)
	if err != nil {
		return err
	}
	if action == "version" {
		fmt.Println(httpapi.Version)
		return nil
	}
	if err := secureRuntimeData(config.Paths.DataDir); err != nil {
		return err
	}
	applicationLog, err := configureApplicationLogging(config.Paths.DataDir)
	if err != nil {
		return err
	}
	defer applicationLog.Close()
	storageContext, storageCancel := context.WithTimeout(context.Background(), 15*time.Second)
	dataServices, err := persistence.Open(storageContext, config)
	storageCancel()
	if err != nil {
		return err
	}
	defer dataServices.Close()
	environments, err := environment.NewRepositoryWithStore(config.Paths.EnvironmentsDir, dataServices)
	if err != nil {
		return err
	}
	accessControl := access.NewService(dataServices)
	awsCredentialService, err := awscredentials.New(config, dataServices)
	if err != nil {
		return err
	}
	stateBackendService, err := statebackend.New(config, dataServices)
	if err != nil {
		return err
	}
	tlsCertificateService, err := tlscertificates.New(config, dataServices)
	if err != nil {
		return err
	}
	dataServiceCredentialService, err := dataservicecredentials.New(config, dataServices)
	if err != nil {
		return err
	}
	cicdService, err := cicd.New(config, dataServices)
	if err != nil {
		return err
	}
	jenkinsTunnels := kubetunnel.New(config, awsCredentialService)
	defer jenkinsTunnels.Close()
	cicdService.SetTunnelProvider(jenkinsTunnels)
	gitlabService, err := gitlab.New(config, dataServices)
	if err != nil {
		return err
	}
	if action == "catalog" {
		if strings.TrimSpace(*catalogService) == "" {
			return errors.New("catalog action requires --service")
		}
		items, queryErr := awscatalog.New(config, awsCredentialService).ServiceInstanceTypes(
			context.Background(), *projectName, *catalogRegion, *catalogService, *catalogEngineVersion,
		)
		if queryErr != nil {
			return fmt.Errorf("query AWS service catalog: %w", queryErr)
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]any{
			"project": *projectName, "region": *catalogRegion, "service": *catalogService,
			"engine_version": *catalogEngineVersion, "instance_types": items, "source": "aws-live",
		})
	}
	summaries, err := environments.List(nil)
	if err != nil {
		return err
	}
	bootstrapEnvironments := make([]access.BootstrapEnvironment, 0, len(summaries))
	for _, summary := range summaries {
		bootstrapEnvironments = append(bootstrapEnvironments, access.BootstrapEnvironment{
			ProjectKey: summary.Project, Environment: summary.Environment, TargetName: summary.Name, Region: summary.Region,
		})
	}
	bootstrapContext, bootstrapCancel := context.WithTimeout(context.Background(), 15*time.Second)
	err = accessControl.Bootstrap(bootstrapContext, config.Security.AdminUsername, config.PasswordHash(), bootstrapEnvironments)
	bootstrapCancel()
	if err != nil {
		return err
	}
	if action == "refresh-resources" {
		target, lookupErr := accessControl.Environment(context.Background(), *projectName, *environmentName)
		if lookupErr != nil {
			return fmt.Errorf("resolve project environment: %w", lookupErr)
		}
		status := statusservice.NewServiceWithCache(config, environments, dataServices)
		status.SetAWSCredentialProvider(awsCredentialService)
		status.SetTerraformOutputProvider(stateBackendService)
		resources := resourcecenter.New(config, environments, status, dataServices)
		resources.SetAWSCredentialProvider(awsCredentialService)
		resources.SetTerraformOutputProvider(stateBackendService)
		refreshContext, refreshCancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer refreshCancel()
		snapshot, refreshErr := resources.Refresh(refreshContext, *projectName, *environmentName, target.TargetName)
		if refreshErr != nil {
			return fmt.Errorf("refresh environment resources: %w", refreshErr)
		}
		return json.NewEncoder(os.Stdout).Encode(snapshot.Public())
	}
	deployment := runner.NewDeployment(config, environments)
	deployment.SetAWSCredentialProvider(awsCredentialService)
	deployment.SetStateBackendProvider(stateBackendService)
	deployment.SetTLSCertificateProvider(tlsCertificateService)
	deployment.SetDataServiceCredentialProvider(dataServiceCredentialService)
	jobManager, err := jobs.NewManagerWithStores(
		config.Paths.DataDir,
		config.Jobs.MaxParallel,
		config.Jobs.HistoryLimit,
		config.Jobs.Timeout,
		deployment,
		dataServices,
		dataServices,
	)
	if err != nil {
		return err
	}

	if action == "serve" {
		return serve(config, environments, jobManager, dataServices, accessControl, awsCredentialService, stateBackendService, tlsCertificateService, dataServiceCredentialService, cicdService, gitlabService)
	}
	jobAction := jobs.Action(action)
	switch jobAction {
	case jobs.ActionValidate, jobs.ActionPlan, jobs.ActionDeploy, jobs.ActionPlatform, jobs.ActionTLS, jobs.ActionDestroy:
		target, lookupErr := accessControl.Environment(context.Background(), *projectName, *environmentName)
		if lookupErr != nil {
			return fmt.Errorf("resolve project environment: %w", lookupErr)
		}
		return runCLI(config, jobManager, target, jobAction)
	default:
		return fmt.Errorf("unknown action %q; use serve, validate, plan, deploy, platform, tls, destroy, refresh-resources, catalog, hash-password, or version", action)
	}
}

func loadDotEnv(path string) error {
	file, err := os.Open(filepath.Clean(path))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open environment file: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		key, value, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !ok || !validEnvironmentKey(key) {
			return fmt.Errorf("invalid environment assignment at %s:%d", path, lineNumber)
		}
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 && value[0] == '\'' && value[len(value)-1] == '\'' {
			value = value[1 : len(value)-1]
		} else if strings.HasPrefix(value, "\"") {
			value, err = strconv.Unquote(value)
			if err != nil {
				return fmt.Errorf("invalid quoted value at %s:%d", path, lineNumber)
			}
		}
		if err := os.Setenv(key, value); err != nil {
			return fmt.Errorf("set %s from environment file: %w", key, err)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read environment file: %w", err)
	}
	return nil
}

func validEnvironmentKey(value string) bool {
	if value == "" || !((value[0] >= 'A' && value[0] <= 'Z') || value[0] == '_') {
		return false
	}
	for _, character := range value[1:] {
		if !((character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || character == '_') {
			return false
		}
	}
	return true
}

func serve(config *appconfig.Config, environments *environment.Repository, jobManager *jobs.Manager, dataServices *persistence.Services, accessControl *access.Service, awsCredentialService *awscredentials.Service, stateBackendService *statebackend.Service, tlsCertificateService *tlscertificates.Service, dataServiceCredentialService *dataservicecredentials.Service, cicdService *cicd.Service, gitlabService *gitlab.Service) error {
	authentication, err := auth.NewServiceWithUsers(config.Security, config.PasswordHash(), accessControl)
	if err != nil {
		return err
	}
	status := statusservice.NewServiceWithCache(config, environments, dataServices)
	status.SetAWSCredentialProvider(awsCredentialService)
	status.SetTerraformOutputProvider(stateBackendService)
	api, err := httpapi.New(config, environments, jobManager, status, authentication, accessControl)
	if err != nil {
		return err
	}
	api.SetDataServices(dataServices, dataServices)
	api.SetAWSCredentialService(awsCredentialService)
	api.SetStateBackendService(stateBackendService)
	api.SetAWSCatalog(awscatalog.New(config, awsCredentialService))
	api.SetComponentCatalog(componentcatalog.New(config, dataServices))
	api.SetTLSCertificateService(tlsCertificateService)
	api.SetDataServiceCredentialService(dataServiceCredentialService)
	api.SetCICDService(cicdService)
	api.SetGitLabService(gitlabService)
	api.SetStaticCDNService(staticcdn.New(config, dataServices, awsCredentialService))
	resources := resourcecenter.New(config, environments, status, dataServices)
	resources.SetAWSCredentialProvider(awsCredentialService)
	resources.SetTerraformOutputProvider(stateBackendService)
	api.SetResourceService(resources)
	server := &http.Server{
		Addr:              config.Server.ListenAddress,
		Handler:           api.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       config.Server.ReadTimeout,
		WriteTimeout:      config.Server.WriteTimeout,
		IdleTimeout:       90 * time.Second,
		MaxHeaderBytes:    32 << 10,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	scheme := "http"
	if config.Server.TLSCertFile != "" {
		scheme = "https"
	}
	log.Printf("运维自动部署平台 %s 已启动：%s://%s", httpapi.Version, scheme, config.Server.ListenAddress)
	var serveErr error
	if config.Server.TLSCertFile != "" {
		serveErr = server.ListenAndServeTLS(config.Server.TLSCertFile, config.Server.TLSKeyFile)
	} else {
		serveErr = server.ListenAndServe()
	}
	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		return serveErr
	}
	return nil
}

func readNewAdminPasswordHash() (string, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return "", errors.New("an interactive terminal is required")
	}
	fmt.Fprint(os.Stderr, "New admin password (minimum 12 characters): ")
	password, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	defer clear(password)
	if len(password) < 12 {
		return "", errors.New("password must contain at least 12 characters")
	}
	fmt.Fprint(os.Stderr, "Confirm admin password: ")
	confirmation, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	defer clear(confirmation)
	if !bytes.Equal(password, confirmation) {
		return "", errors.New("password confirmation does not match")
	}
	encoded, err := auth.HashPassword(password)
	if err != nil {
		return "", err
	}
	return encoded, nil
}

func hashPasswordCLI() error {
	encoded, err := readNewAdminPasswordHash()
	if err != nil {
		return err
	}
	fmt.Println(encoded)
	return nil
}

func randomSecret(byteCount int, encoding *base64.Encoding) (string, error) {
	value := make([]byte, byteCount)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return encoding.EncodeToString(value), nil
}

func renderInitialEnvironment(passwordHash, mysqlPassword, mysqlRootPassword, redisPassword, credentialKey string) []byte {
	return []byte(strings.Join([]string{
		"# Generated by ops-deploy init. Keep this file private and back it up securely.",
		"OPS_MYSQL_PASSWORD='" + mysqlPassword + "'",
		"OPS_MYSQL_ROOT_PASSWORD='" + mysqlRootPassword + "'",
		"OPS_REDIS_PASSWORD='" + redisPassword + "'",
		"OPS_DEPLOY_PASSWORD_HASH='" + passwordHash + "'",
		"OPS_DEPLOY_CREDENTIAL_KEY='" + credentialKey + "'",
		"OPS_MYSQL_DSN='ops:" + mysqlPassword + "@tcp(127.0.0.1:13306)/ops_deploy?parseTime=true&charset=utf8mb4&collation=utf8mb4_unicode_ci'",
		"",
	}, "\n"))
}

func initEnvironmentCLI(configPath, outputPath string) error {
	if _, err := os.Stat(configPath); err != nil {
		return fmt.Errorf("configuration file is unavailable: %w", err)
	}
	if strings.TrimSpace(outputPath) == "" {
		outputPath = filepath.Join(filepath.Dir(configPath), ".env")
	}
	absoluteOutput, err := filepath.Abs(outputPath)
	if err != nil {
		return fmt.Errorf("resolve init output: %w", err)
	}
	if _, err := os.Stat(absoluteOutput); err == nil {
		return fmt.Errorf("refusing to overwrite existing environment file %s", absoluteOutput)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect environment file: %w", err)
	}
	passwordHash, err := readNewAdminPasswordHash()
	if err != nil {
		return err
	}
	mysqlPassword, err := randomSecret(24, base64.RawURLEncoding)
	if err != nil {
		return fmt.Errorf("generate MySQL password: %w", err)
	}
	mysqlRootPassword, err := randomSecret(24, base64.RawURLEncoding)
	if err != nil {
		return fmt.Errorf("generate MySQL root password: %w", err)
	}
	redisPassword, err := randomSecret(24, base64.RawURLEncoding)
	if err != nil {
		return fmt.Errorf("generate Redis password: %w", err)
	}
	credentialKey, err := randomSecret(32, base64.StdEncoding)
	if err != nil {
		return fmt.Errorf("generate credential encryption key: %w", err)
	}
	file, err := os.OpenFile(absoluteOutput, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return fmt.Errorf("refusing to overwrite existing environment file %s", absoluteOutput)
	}
	if err != nil {
		return fmt.Errorf("create environment file: %w", err)
	}
	payload := renderInitialEnvironment(passwordHash, mysqlPassword, mysqlRootPassword, redisPassword, credentialKey)
	if _, err = file.Write(payload); err != nil {
		_ = file.Close()
		_ = os.Remove(absoluteOutput)
		return fmt.Errorf("write environment file: %w", err)
	}
	if err = file.Close(); err != nil {
		_ = os.Remove(absoluteOutput)
		return fmt.Errorf("close environment file: %w", err)
	}
	fmt.Printf("Initialized %s with mode 0600. Back it up securely; losing OPS_DEPLOY_CREDENTIAL_KEY makes saved credentials unrecoverable.\n", absoluteOutput)
	return nil
}

func runCLI(config *appconfig.Config, manager *jobs.Manager, target access.ProjectEnvironment, action jobs.Action) error {
	job, err := manager.Submit(target.ProjectKey, target.Environment, target.TargetName, "cli", action)
	if err != nil {
		return err
	}
	fmt.Printf("job %s queued\n", job.ID)
	ctx, cancel := context.WithTimeout(context.Background(), config.Jobs.Timeout+time.Minute)
	defer cancel()
	offset := int64(0)
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		data, next, complete, err := manager.ReadLog(job.ID, offset, 256*1024)
		if err != nil {
			return err
		}
		if len(data) > 0 {
			_, _ = os.Stdout.Write(data)
			offset = next
		}
		if complete {
			result, _ := manager.Get(job.ID)
			if result.Status != jobs.StatusSucceeded {
				return fmt.Errorf("job %s ended with status %s: %s", result.ID, result.Status, result.Error)
			}
			return nil
		}
		select {
		case <-ctx.Done():
			_ = manager.Cancel(job.ID)
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func configureApplicationLogging(dataDir string) (*os.File, error) {
	dir := filepath.Join(dataDir, "logs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create application log directory: %w", err)
	}
	file, err := os.OpenFile(filepath.Join(dir, "manager.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) // #nosec G304 -- dataDir is resolved from the administrator-owned config.
	if err != nil {
		return nil, fmt.Errorf("open application log: %w", err)
	}
	if err := file.Chmod(0o600); err != nil {
		return nil, errors.Join(fmt.Errorf("secure application log: %w", err), file.Close())
	}
	log.SetOutput(io.MultiWriter(os.Stderr, file))
	log.SetFlags(log.Ldate | log.Ltime | log.LUTC | log.Lmicroseconds)
	return file, nil
}

func secureRuntimeData(dataDir string) error {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return fmt.Errorf("create runtime data directory: %w", err)
	}
	if err := os.Chmod(dataDir, 0o700); err != nil { // #nosec G302 -- directories require execute permission; mode 0700 is owner-only.
		return fmt.Errorf("secure runtime data directory: %w", err)
	}
	// Backend HCL and Terraform's cached backend configuration can temporarily
	// contain the encrypted-at-rest state-center credential. They are recreated
	// for every job and must not survive a manager restart.
	if err := os.RemoveAll(filepath.Join(dataDir, "terraform-state-runtime")); err != nil {
		return fmt.Errorf("remove stale Terraform state credential runtime: %w", err)
	}
	terraformDataRoot := filepath.Join(dataDir, "terraform")
	walkErr := filepath.WalkDir(terraformDataRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		if entry.Name() == "terraform.tfstate" || entry.Name() == "terraform.tfstate.backup" {
			return os.Remove(path)
		}
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, os.ErrNotExist) {
		return fmt.Errorf("remove stale Terraform backend cache: %w", walkErr)
	}
	planEntries, _ := filepath.Glob(filepath.Join(dataDir, "plans", "*.tfplan"))
	for _, planPath := range planEntries {
		if err := os.Remove(planPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove stale Terraform plan %s: %w", filepath.Base(planPath), err)
		}
	}
	for _, name := range []string{"jobs", "plans", "kubeconfigs", "logs"} {
		root, err := os.OpenRoot(filepath.Join(dataDir, name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("open runtime %s: %w", name, err)
		}
		walkErr := fs.WalkDir(root.FS(), ".", func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return nil
			}
			mode := os.FileMode(0o600)
			if entry.IsDir() {
				mode = 0o700
			}
			return root.Chmod(path, mode)
		})
		if err := errors.Join(walkErr, root.Close()); err != nil {
			return fmt.Errorf("secure runtime %s: %w", name, err)
		}
	}
	return nil
}
