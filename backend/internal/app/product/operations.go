package product

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/application/operations/doctor"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/audit"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/bootstrap"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/recovery"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/config"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
	platformdesktop "github.com/NAMEWTA/go-admin-plus/backend/internal/platform/desktop"
)

type BootstrapInput struct {
	Username, DisplayName, Email string
	Secret                       io.Reader
	DataRoot                     string
}

type RecoveryInput struct {
	AccountID string
	Reason    string
	Secret    io.Reader
	DataRoot  string
}

func BootstrapAdmin(ctx context.Context, snapshot config.Snapshot, input BootstrapInput) (string, error) {
	release, err := acquireSQLiteOperationLock(snapshot, input.DataRoot)
	if err != nil {
		return "", errors.New("bootstrap requires an offline runtime")
	}
	defer release()
	db, err := openOperationalDatabase(ctx, snapshot)
	if err != nil {
		return "", err
	}
	defer db.Close()
	secret, err := bootstrap.ReadSecret(input.Secret)
	if err != nil {
		return "", errors.New("bootstrap secret is invalid")
	}
	service, err := bootstrap.NewService(db, operationalAudit{})
	if err != nil {
		return "", errors.New("bootstrap service failed")
	}
	result, err := service.Bootstrap(ctx, bootstrap.Command{
		Username: input.Username, DisplayName: input.DisplayName, Email: input.Email, Secret: secret,
	})
	if err != nil {
		return "", errors.New("bootstrap command failed")
	}
	return result.AccountID, nil
}

func RecoverAdmin(ctx context.Context, snapshot config.Snapshot, input RecoveryInput) (string, error) {
	release, err := acquireSQLiteOperationLock(snapshot, input.DataRoot)
	if err != nil {
		return "", errors.New("recovery requires an offline runtime")
	}
	defer release()
	db, err := openOperationalDatabase(ctx, snapshot)
	if err != nil {
		return "", err
	}
	defer db.Close()
	secret, err := recovery.ReadSecret(input.Secret)
	if err != nil {
		return "", errors.New("recovery secret is invalid")
	}
	guard, err := recovery.NewDatabaseOfflineGuard(db)
	if err != nil {
		return "", errors.New("recovery guard failed")
	}
	service, err := recovery.NewService(db, guard, operationalAudit{})
	if err != nil {
		return "", errors.New("recovery service failed")
	}
	result, err := service.RecoverAdmin(ctx, recovery.Command{AccountID: input.AccountID, Reason: recovery.Reason(input.Reason), Secret: secret})
	if err != nil {
		return "", errors.New("recovery command failed")
	}
	return result.AccountID, nil
}

func acquireSQLiteOperationLock(snapshot config.Snapshot, dataRoot string) (func() error, error) {
	if snapshot.Profile() != config.ProfileServerSQLite {
		return func() error { return nil }, nil
	}
	lock, err := platformdesktop.AcquireInstanceLock(dataRoot)
	if err != nil {
		return nil, err
	}
	return lock.Close, nil
}

func openOperationalDatabase(ctx context.Context, snapshot config.Snapshot) (*database.Database, error) {
	databaseConfig, err := migrationDatabaseConfig(snapshot)
	if err != nil {
		return nil, errors.New("operation profile is invalid")
	}
	db, err := database.NewProcess().Open(ctx, databaseConfig)
	if err != nil {
		return nil, errors.New("operation database startup failed")
	}
	if err := PrepareRuntimeSchema(ctx, db, db.Dialect() == database.DialectSQLite); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

type operationalAudit struct{}

func (operationalAudit) RecordBootstrap(ctx context.Context, tx database.Tx, fact bootstrap.Fact) error {
	payload, _ := json.Marshal(map[string]string{"source": string(audit.SourceServer)})
	_, err := tx.ExecContext(ctx, `INSERT INTO audit_facts(topic, business_key, payload, occurred_at)
		VALUES (?, ?, ?, ?)`, "operation.created", "resource:iam_bootstrap:"+fact.AccountID, payload, fact.OccurredAt)
	return err
}

func (operationalAudit) RecordRecovery(ctx context.Context, tx database.Tx, fact recovery.Fact) error {
	payload, _ := json.Marshal(map[string]string{"source": string(audit.SourceServer)})
	key := fmt.Sprintf("resource:iam_recovery:%s", fact.AccountID)
	actorRef := "account:" + fact.AccountID
	_, err := tx.ExecContext(ctx, `INSERT INTO audit_facts(topic, business_key, actor_ref, payload, occurred_at)
		VALUES (?, ?, ?, ?, ?)`, "operation.updated", key, actorRef, payload, fact.OccurredAt)
	return err
}

func RunDoctor(ctx context.Context, snapshot config.Snapshot, dataRoot, version string) doctor.Report {
	profile := string(snapshot.Profile())
	databaseConfig, configErr := migrationDatabaseConfig(snapshot)
	var db *database.Database
	var databaseErr error
	if configErr == nil {
		openCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		db, databaseErr = database.NewProcess().Open(openCtx, databaseConfig)
	}
	if db != nil {
		defer db.Close()
	}
	filesRoot := filepath.Join(dataRoot, "files")
	checkDB := func(run func(context.Context, *database.Database) error) func(context.Context) error {
		return func(checkCtx context.Context) error {
			if configErr != nil || databaseErr != nil || db == nil {
				return errors.New("database unavailable")
			}
			return run(checkCtx, db)
		}
	}
	runner, err := doctor.New(doctor.Config{Profile: profile, Version: version, Timeout: 10 * time.Second},
		doctor.Check{Name: doctor.CheckConfiguration, Critical: true, FailureClass: doctor.ClassInvalidConfiguration, Run: func(context.Context) error { return configErr }},
		doctor.Check{Name: doctor.CheckSecretReference, Critical: true, FailureClass: doctor.ClassInvalidConfiguration, Run: func(context.Context) error { return configErr }},
		doctor.Check{Name: doctor.CheckDatabase, Critical: true, Run: checkDB(func(checkCtx context.Context, db *database.Database) error { return db.SQL().PingContext(checkCtx) })},
		doctor.Check{Name: doctor.CheckSchema, Critical: true, FailureClass: doctor.ClassIncompatible, Run: checkDB(func(checkCtx context.Context, db *database.Database) error {
			return PrepareRuntimeSchema(checkCtx, db, false)
		})},
		doctor.Check{Name: doctor.CheckSetup, Critical: false, Run: checkDB(func(checkCtx context.Context, db *database.Database) error {
			var count int
			if err := db.SQL().QueryRowContext(checkCtx, `SELECT COUNT(*) FROM iam_bootstrap_state`).Scan(&count); err != nil {
				return err
			}
			if count != 1 {
				return errors.New("setup is incomplete")
			}
			return nil
		})},
		doctor.Check{Name: doctor.CheckFilesRoot, Critical: false, Run: func(context.Context) error {
			info, err := os.Stat(filesRoot)
			if err != nil || !info.IsDir() {
				return errors.New("files root unavailable")
			}
			return nil
		}},
		doctor.Check{Name: doctor.CheckDisk, Critical: false, Run: func(context.Context) error {
			_, err := os.Stat(filepath.Clean(dataRoot))
			return err
		}},
		doctor.Check{Name: doctor.CheckWorker, Critical: false, Run: checkDB(func(checkCtx context.Context, db *database.Database) error { return db.SQL().PingContext(checkCtx) })},
		doctor.Check{Name: doctor.CheckVersion, Critical: true, FailureClass: doctor.ClassIncompatible, Run: func(context.Context) error {
			if version == "" {
				return fmt.Errorf("version unavailable")
			}
			return nil
		}},
	)
	if err != nil {
		return doctor.Report{Profile: profile, Version: version, Exit: doctor.ExitInvalid}
	}
	return runner.Run(ctx)
}

func InvalidDoctorReport(profile, version string) doctor.Report {
	return doctor.Report{
		Profile: profile, Version: version, Exit: doctor.ExitInvalid,
		Checks: []doctor.Result{{
			Name: doctor.CheckConfiguration, Status: doctor.StatusFailed, Class: doctor.ClassInvalidConfiguration,
		}},
	}
}
