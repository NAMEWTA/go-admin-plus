package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/app/product"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/config"
)

const version = "0.0.3"

type commonOptions struct {
	profile, configFile, sqlitePath string
}

type runtimeOptions struct {
	commonOptions
	listen, dataRoot string
	withWorker       bool
	development      bool
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], environment(), os.Stdout); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "go-admin-plus command failed")
		os.Exit(1)
	}
}

func run(ctx context.Context, arguments []string, environmentValues map[string]string, output io.Writer) error {
	if ctx == nil || len(arguments) == 0 {
		return errors.New("subcommand is required")
	}
	command, arguments := arguments[0], arguments[1:]
	switch command {
	case "serve":
		options, err := parseRuntimeOptions(command, arguments, true)
		if err != nil {
			return err
		}
		snapshot, err := loadSnapshot(options.commonOptions, options.listen, environmentValues)
		if err != nil {
			return errors.New("serve configuration failed")
		}
		if options.dataRoot == "" {
			options.dataRoot = snapshot.Settings().Runtime.DataDir
		}
		if snapshot.Settings().Runtime.Role == "worker" {
			return product.RunServerWorker(ctx, product.ServerLaunch{Snapshot: snapshot, DataRoot: options.dataRoot, Version: version, Development: options.development})
		}
		options.withWorker = options.withWorker && snapshot.Settings().Runtime.Role != "api"
		if snapshot.Profile() == config.ProfileServerSQLite && !options.withWorker {
			return errors.New("SQLite requires workers in the API process")
		}
		host, err := product.NewServerHost(product.ServerLaunch{
			Snapshot: snapshot, DataRoot: options.dataRoot,
			Version: version, WithWorker: options.withWorker, Development: options.development,
		})
		if err != nil {
			return err
		}
		return host.Run(ctx)
	case "worker":
		options, err := parseRuntimeOptions(command, arguments, false)
		if err != nil {
			return err
		}
		snapshot, err := loadSnapshot(options.commonOptions, "", environmentValues)
		if err != nil {
			return errors.New("worker configuration failed")
		}
		if options.dataRoot == "" {
			options.dataRoot = snapshot.Settings().Runtime.DataDir
		}
		return product.RunServerWorker(ctx, product.ServerLaunch{
			Snapshot: snapshot, DataRoot: options.dataRoot, Version: version, Development: options.development,
		})
	case "migrate":
		options, err := parseRuntimeOptions(command, arguments, false)
		if err != nil {
			return err
		}
		snapshot, err := loadSnapshot(options.commonOptions, "", environmentValues)
		if err != nil {
			return errors.New("migration configuration failed")
		}
		if options.dataRoot == "" {
			options.dataRoot = snapshot.Settings().Runtime.DataDir
		}
		result, err := product.MigrateOffline(ctx, snapshot, options.dataRoot)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(output, "migration complete: applied=%d current_version=%d\n", result.Applied, result.CurrentVersion)
		return err
	case "bootstrap":
		return runBootstrap(ctx, arguments, environmentValues, output)
	case "recover-admin":
		return runRecovery(ctx, arguments, environmentValues, output)
	case "doctor":
		options, err := parseRuntimeOptions(command, arguments, false)
		if err != nil {
			return err
		}
		snapshot, err := loadSnapshot(options.commonOptions, "", environmentValues)
		if err != nil {
			if encodeErr := json.NewEncoder(output).Encode(product.InvalidDoctorReport(options.profile, version)); encodeErr != nil {
				return errors.New("doctor output failed")
			}
			return errors.New("doctor configuration failed")
		}
		if options.dataRoot == "" {
			options.dataRoot = snapshot.Settings().Runtime.DataDir
		}
		report := product.RunDoctor(ctx, snapshot, options.dataRoot, version)
		if err := json.NewEncoder(output).Encode(report); err != nil {
			return errors.New("doctor output failed")
		}
		if string(report.Exit) != "healthy" && string(report.Exit) != "degraded" {
			return errors.New("doctor checks failed")
		}
		return nil
	case "version":
		if len(arguments) != 0 {
			return errors.New("version accepts no arguments")
		}
		_, err := fmt.Fprintln(output, version)
		return err
	default:
		return errors.New("unknown subcommand")
	}
}

func runBootstrap(ctx context.Context, arguments []string, environmentValues map[string]string, output io.Writer) error {
	set := newFlagSet("bootstrap")
	var common commonOptions
	bindCommon(set, &common)
	username := set.String("username", "", "administrator username")
	displayName := set.String("display-name", "", "administrator display name")
	email := set.String("email", "", "administrator email")
	secretFile := set.String("secret-file", "", "permission-restricted password file")
	dataRoot := set.String("data-root", "", "runtime data root")
	if err := parseFlags(set, arguments); err != nil {
		return err
	}
	snapshot, err := loadSnapshot(common, "", environmentValues)
	if err != nil {
		return errors.New("bootstrap configuration failed")
	}
	secret, closeSecret, err := openSecret(*secretFile)
	if err != nil {
		return err
	}
	defer closeSecret()
	accountID, err := product.BootstrapAdmin(ctx, snapshot, product.BootstrapInput{
		Username: *username, DisplayName: *displayName, Email: *email, Secret: secret, DataRoot: configuredDataRoot(*dataRoot, snapshot),
	})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(output, "bootstrap complete: account_id=%s\n", accountID)
	return err
}

func runRecovery(ctx context.Context, arguments []string, environmentValues map[string]string, output io.Writer) error {
	set := newFlagSet("recover-admin")
	var common commonOptions
	bindCommon(set, &common)
	accountID := set.String("account-id", "", "existing account identifier")
	reason := set.String("reason", "", "lost-access, credential-compromise, or disabled-administrator")
	secretFile := set.String("secret-file", "", "permission-restricted password file")
	dataRoot := set.String("data-root", "", "runtime data root")
	if err := parseFlags(set, arguments); err != nil {
		return err
	}
	snapshot, err := loadSnapshot(common, "", environmentValues)
	if err != nil {
		return errors.New("recovery configuration failed")
	}
	secret, closeSecret, err := openSecret(*secretFile)
	if err != nil {
		return err
	}
	defer closeSecret()
	resultID, err := product.RecoverAdmin(ctx, snapshot, product.RecoveryInput{
		AccountID: *accountID, Reason: *reason, Secret: secret, DataRoot: configuredDataRoot(*dataRoot, snapshot),
	})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(output, "recover-admin complete: account_id=%s\n", resultID)
	return err
}

func parseRuntimeOptions(command string, arguments []string, serve bool) (runtimeOptions, error) {
	set := newFlagSet(command)
	options := runtimeOptions{}
	bindCommon(set, &options.commonOptions)
	set.StringVar(&options.dataRoot, "data-root", options.dataRoot, "runtime data root")
	set.BoolVar(&options.development, "development", false, "use development console logging")
	if serve {
		set.StringVar(&options.listen, "listen", "", "HTTP listen override")
		set.BoolVar(&options.withWorker, "with-worker", true, "run workers in the API process")
	}
	if err := parseFlags(set, arguments); err != nil {
		return runtimeOptions{}, err
	}
	return options, nil
}

func parseCommonOptions(command string, arguments []string) (commonOptions, error) {
	set := newFlagSet(command)
	var options commonOptions
	bindCommon(set, &options)
	if err := parseFlags(set, arguments); err != nil {
		return commonOptions{}, err
	}
	return options, nil
}

func bindCommon(set *flag.FlagSet, options *commonOptions) {
	set.StringVar(&options.profile, "profile", "", "optional database profile override")
	set.StringVar(&options.configFile, "config", "", "runtime YAML configuration file")
	set.StringVar(&options.sqlitePath, "sqlite-path", "", "SQLite database path override")
}

func newFlagSet(name string) *flag.FlagSet {
	set := flag.NewFlagSet(name, flag.ContinueOnError)
	set.SetOutput(io.Discard)
	return set
}

func parseFlags(set *flag.FlagSet, arguments []string) error {
	if err := set.Parse(arguments); err != nil {
		return err
	}
	if set.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	return nil
}

func loadSnapshot(options commonOptions, listen string, environmentValues map[string]string) (config.Snapshot, error) {
	profile := config.Profile(options.profile)
	if profile != "" && profile != config.ProfileServerSQLite && profile != config.ProfileServerPostgres {
		return config.Snapshot{}, errors.New("server profile is unsupported")
	}
	if profile == config.ProfileServerPostgres && options.sqlitePath != "" {
		return config.Snapshot{}, errors.New("sqlite path cannot be used by the postgres profile")
	}
	cli := map[string]string{}
	if listen != "" {
		cli["http.listen"] = listen
	}
	if options.sqlitePath != "" {
		cli["database.path"] = options.sqlitePath
	}
	return config.LoadRuntime(config.Input{Profile: profile, File: options.configFile, Environment: environmentValues, CLI: cli})
}

func openSecret(path string) (io.Reader, func() error, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil, errors.New("secret file is required")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, errors.New("secret file is unavailable")
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0) || info.Size() > 4096 {
		_ = file.Close()
		return nil, nil, errors.New("secret file permissions or size are invalid")
	}
	return io.LimitReader(file, 4097), file.Close, nil
}

func environment() map[string]string {
	result := make(map[string]string)
	for _, entry := range os.Environ() {
		key, value, found := strings.Cut(entry, "=")
		if found && strings.HasPrefix(key, "GO_ADMIN_") {
			result[key] = value
		}
	}
	return result
}

func configuredDataRoot(value string, snapshot config.Snapshot) string {
	if value != "" {
		return value
	}
	return snapshot.Settings().Runtime.DataDir
}
