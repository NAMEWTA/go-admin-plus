package files

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"time"

	audit "github.com/NAMEWTA/go-admin-plus/backend/internal/platform/auditing"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
	"github.com/google/uuid"
)

const recoveryBatchSize = 100

type Service struct {
	recorder       audit.OperationPort
	db             Database
	storage        Storage
	authorizer     Authorizer
	repository     repository
	now            func() time.Time
	observer       Observer
	capacityPolicy CapacityPolicy
	capacityProbe  CapacityProbe
}

type Option func(*Service)

func WithAuditRecorder(recorder audit.OperationPort) Option {
	return func(s *Service) { s.recorder = recorder }
}

func WithClock(clock func() time.Time) Option { return func(service *Service) { service.now = clock } }
func WithObserver(observer Observer) Option {
	return func(service *Service) { service.observer = observer }
}

func NewService(db Database, storage Storage, authorizer Authorizer, options ...Option) (*Service, error) {
	if db == nil || storage == nil || authorizer == nil || (db.Dialect() != database.DialectSQLite && db.Dialect() != database.DialectPostgres) {
		return nil, errors.New("files service dependencies are required")
	}
	service := &Service{db: db, storage: storage, authorizer: authorizer, repository: repository{dialect: db.Dialect()}, now: time.Now,
		observer: discardObserver{}, capacityPolicy: DefaultCapacityPolicy()}
	for _, option := range options {
		if option != nil {
			option(service)
		}
	}
	if service.capacityProbe == nil {
		if provider, ok := storage.(interface{ CapacityProbe() CapacityProbe }); ok {
			service.capacityProbe = provider.CapacityProbe()
		} else {
			service.capacityProbe = unavailableCapacityProbe{}
		}
	}
	if service.now == nil || service.observer == nil || service.capacityProbe == nil || !service.capacityPolicy.valid() {
		return nil, errors.New("files service options are invalid")
	}
	return service, nil
}

func (service *Service) Upload(ctx context.Context, actorID string, input UploadInput) (result Metadata, resultErr error) {
	defer func() { service.observe("upload", resultErr) }()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	name, valid := normalizeFilename(input.OriginalName)
	if actorID == "" || !valid || input.Content == nil {
		return Metadata{}, ErrValidation
	}
	reservationBytes := input.DeclaredSizeBytes
	if reservationBytes < 0 || reservationBytes > service.capacityPolicy.MaximumObjectBytes {
		return Metadata{}, ErrContentTooLarge
	}
	if reservationBytes == 0 {
		reservationBytes = service.capacityPolicy.MaximumObjectBytes
	}
	if err := service.db.WithinTx(ctx, func(ctx context.Context, tx database.Tx) error {
		scope, err := service.authorizer.RequireInTx(ctx, tx, actorID, PermissionFilesWrite)
		if err != nil {
			return err
		}
		if !validScope(scope) {
			return ErrDenied
		}
		return nil
	}); err != nil {
		return Metadata{}, service.normalize(ctx, err)
	}
	capacity, err := service.capacityProbe.Capacity(ctx)
	if err != nil {
		if ctx != nil && ctx.Err() != nil {
			return Metadata{}, ctx.Err()
		}
		return Metadata{}, ErrDiskCapacity
	}
	if !service.capacityPolicy.accepts(capacity, reservationBytes) {
		return Metadata{}, ErrDiskCapacity
	}
	reservationID := uuid.NewString()
	reservedAt := service.now().UTC()
	if err := service.db.WithinTx(ctx, func(ctx context.Context, tx database.Tx) error {
		return service.repository.reserve(ctx, tx, reservationID, actorID, reservationBytes, reservedAt,
			reservedAt.Add(service.capacityPolicy.ReservationTTL), service.capacityPolicy)
	}); err != nil {
		return Metadata{}, service.normalize(ctx, err)
	}
	reservationOwnedByRequest := true
	defer func() {
		if reservationOwnedByRequest {
			cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = service.db.WithinTx(cleanup, func(ctx context.Context, tx database.Tx) error {
				return service.repository.releaseReservation(ctx, tx, reservationID)
			})
		}
	}()
	var staged StagedContent
	if durable, ok := service.storage.(interface {
		StageReserved(context.Context, string, string, io.Reader) (StagedContent, error)
	}); ok {
		staged, err = durable.StageReserved(ctx, reservationID, input.DeclaredMediaType, input.Content)
	} else {
		staged, err = service.storage.Stage(ctx, input.DeclaredMediaType, input.Content)
	}
	if err != nil {
		return Metadata{}, service.normalize(ctx, err)
	}
	stageOwnedByRequest := true
	defer func() {
		if stageOwnedByRequest {
			cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = service.storage.Abort(cleanup, staged.TemporaryKey)
		}
	}()
	if input.DeclaredSizeBytes > 0 && staged.SizeBytes != input.DeclaredSizeBytes {
		return Metadata{}, ErrSizeMismatch
	}
	now := reservedAt
	claimUntil := reservedAt.Add(service.capacityPolicy.ReservationTTL)
	record := fileRecord{ClaimToken: &reservationID, ClaimExpiresAt: &claimUntil, ProviderID: defaultProvider(service.storage), ID: uuid.NewString(), OwnerAccountID: actorID, OriginalName: name, NameKey: nameKey(name), MediaType: staged.MediaType,
		SizeBytes: staged.SizeBytes, SHA256: staged.SHA256, StorageKey: NewStorageKey(), TemporaryKey: &staged.TemporaryKey, State: statePending,
		Revision: 1, CreatedAt: now, UpdatedAt: now}
	err = service.db.WithinTx(ctx, func(ctx context.Context, tx database.Tx) error {
		scope, err := service.authorizer.RequireInTx(ctx, tx, actorID, PermissionFilesWrite)
		if err != nil {
			return err
		}
		if !validScope(scope) {
			return ErrDenied
		}
		return service.repository.commitReservation(ctx, tx, reservationID, record)
	})
	if err != nil {
		return Metadata{}, service.normalize(ctx, err)
	}
	reservationOwnedByRequest = false
	stageOwnedByRequest = false
	if err := service.storage.Publish(ctx, staged.TemporaryKey, record.StorageKey); err != nil {
		// 请求已明确退出发布流程，释放认领供恢复任务立即重试；断电仍由超时租约接管。
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = service.db.WithinTx(cleanup, func(ctx context.Context, tx database.Tx) error {
			_, e := tx.ExecContext(ctx, `UPDATE files_objects SET claim_token=NULL,claim_expires_at=NULL WHERE id=? AND claim_token=? AND state='pending'`, record.ID, reservationID)
			return e
		})
		return Metadata{}, service.normalize(ctx, err)
	}
	record.TemporaryKey = nil
	readyAt := service.now().UTC()
	record.UpdatedAt = readyAt
	err = service.db.WithinTx(ctx, func(ctx context.Context, tx database.Tx) error {
		if err := service.repository.markReady(ctx, tx, record.ID, readyAt); err != nil {
			return err
		}
		return audit.Write(service.recorder, ctx, tx, audit.Operation{Action: "create", ResourceType: "file", ResourceID: record.ID, ActorID: actorID, Code: "UploadFile"})
	})
	if err != nil {
		return Metadata{}, service.normalize(ctx, err)
	}
	return metadata(record), nil
}

func (service *Service) List(ctx context.Context, actorID string, query ListQuery) (result Page, resultErr error) {
	defer func() { service.observe("list", resultErr) }()
	query, valid := normalizeListQuery(query)
	if actorID == "" || !valid {
		return Page{}, ErrValidation
	}
	var page Page
	err := service.db.WithinTx(ctx, func(ctx context.Context, tx database.Tx) error {
		scope, err := service.authorizer.RequireInTx(ctx, tx, actorID, PermissionFilesRead)
		if err != nil {
			return err
		}
		if !validScope(scope) {
			return ErrDenied
		}
		page, err = service.repository.list(ctx, tx, actorID, scope, query)
		return err
	})
	return page, service.normalize(ctx, err)
}

func (service *Service) Download(ctx context.Context, actorID, id string) (resultValue Download, resultErr error) {
	defer func() { service.observe("download", resultErr) }()
	if actorID == "" || uuid.Validate(id) != nil {
		return Download{}, ErrValidation
	}
	var record fileRecord
	err := service.db.WithinTx(ctx, func(ctx context.Context, tx database.Tx) error {
		scope, err := service.authorizer.RequireInTx(ctx, tx, actorID, PermissionFilesRead)
		if err != nil {
			return err
		}
		if !validScope(scope) {
			return ErrDenied
		}
		record, err = service.repository.ready(ctx, tx, id, actorID, scope)
		if err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return Download{}, service.normalize(ctx, err)
	}
	content, err := storageProvider(service.storage, record.ProviderID).Open(ctx, record.StorageKey)
	if err != nil {
		return Download{}, service.normalize(ctx, err)
	}
	return Download{Metadata: metadata(record), Content: content}, nil
}

func (service *Service) GetMetadata(ctx context.Context, actorID, id string) (resultValue Metadata, resultErr error) {
	defer func() { service.observe("metadata", resultErr) }()
	if actorID == "" || uuid.Validate(id) != nil {
		return Metadata{}, ErrValidation
	}
	var result Metadata
	err := service.db.WithinTx(ctx, func(ctx context.Context, tx database.Tx) error {
		scope, err := service.authorizer.RequireInTx(ctx, tx, actorID, PermissionFilesRead)
		if err != nil {
			return err
		}
		if !validScope(scope) {
			return ErrDenied
		}
		record, err := service.repository.ready(ctx, tx, id, actorID, scope)
		if err == nil {
			result = metadata(record)
		}
		return err
	})
	return result, service.normalize(ctx, err)
}

func (service *Service) Delete(ctx context.Context, actorID string, targets []DeleteTarget) (resultErr error) {
	defer func() { service.observe("delete", resultErr) }()
	if actorID == "" || len(targets) < 1 || len(targets) > 100 {
		return ErrValidation
	}
	seen := map[string]struct{}{}
	for _, target := range targets {
		if uuid.Validate(target.ID) != nil || target.Revision < 1 {
			return ErrValidation
		}
		if _, duplicate := seen[target.ID]; duplicate {
			return ErrValidation
		}
		seen[target.ID] = struct{}{}
	}
	var records []fileRecord
	err := service.db.WithinTx(ctx, func(ctx context.Context, tx database.Tx) error {
		scope, err := service.authorizer.RequireInTx(ctx, tx, actorID, PermissionFilesDelete)
		if err != nil {
			return err
		}
		if !validScope(scope) {
			return ErrDenied
		}
		records, err = service.repository.markDeleting(ctx, tx, targets, actorID, scope, service.now().UTC())
		return err
	})
	if err != nil {
		return service.normalize(ctx, err)
	}
	for _, record := range records {
		if err := storageProvider(service.storage, record.ProviderID).Delete(ctx, record.StorageKey); err != nil {
			return service.normalize(ctx, err)
		}
		if err := service.db.WithinTx(ctx, func(ctx context.Context, tx database.Tx) error {
			if err := service.repository.removeDeleting(ctx, tx, record); err != nil {
				return err
			}
			return audit.Write(service.recorder, ctx, tx, audit.Operation{Action: "delete", ResourceType: "file", ResourceID: record.ID, ActorID: actorID, Code: "DeleteFile"})
		}); err != nil {
			return service.normalize(ctx, err)
		}
	}
	return nil
}

// Reconcile claims incomplete metadata before touching disk so concurrent PostgreSQL replicas do
// not repair the same object. SQLite's profile contract supplies the single process owner.
func (service *Service) Reconcile(ctx context.Context) (resultErr error) {
	defer func() { service.observe("reconcile", resultErr) }()
	now := service.now().UTC()
	if err := service.cleanExpiredStages(ctx, now); err != nil {
		return err
	}
	token := uuid.NewString()
	var records []fileRecord
	err := service.db.WithinTx(ctx, func(ctx context.Context, tx database.Tx) error {
		if _, err := service.repository.releaseExpiredReservations(ctx, tx, now, service.capacityPolicy.ReconcileBatchSize); err != nil {
			return err
		}
		var err error
		records, err = service.repository.claimRecovery(ctx, tx, now, now.Add(30*time.Second), token, service.capacityPolicy.ReconcileBatchSize)
		return err
	})
	if err != nil {
		return service.normalize(ctx, err)
	}
	for _, record := range records {
		if err := service.reconcileRecord(ctx, record, token); err != nil {
			return err
		}
	}
	return nil
}

func (service *Service) reconcileRecord(ctx context.Context, record fileRecord, token string) error {
	switch record.State {
	case statePending:
		objectExists, err := storageProvider(service.storage, record.ProviderID).ObjectExists(ctx, record.StorageKey)
		if err != nil {
			return service.normalize(ctx, err)
		}
		temporaryExists := false
		if record.TemporaryKey != nil {
			temporaryExists, err = storageProvider(service.storage, record.ProviderID).TemporaryExists(ctx, *record.TemporaryKey)
			if err != nil {
				return service.normalize(ctx, err)
			}
		}
		if !objectExists && temporaryExists {
			if err := storageProvider(service.storage, record.ProviderID).Publish(ctx, *record.TemporaryKey, record.StorageKey); err != nil {
				return service.normalize(ctx, err)
			}
			objectExists = true
		}
		if objectExists && temporaryExists {
			if err := storageProvider(service.storage, record.ProviderID).Abort(ctx, *record.TemporaryKey); err != nil {
				return service.normalize(ctx, err)
			}
		}
		return service.db.WithinTx(ctx, func(ctx context.Context, tx database.Tx) error {
			if !objectExists {
				return service.repository.removeClaimed(ctx, tx, record, token)
			}
			return service.repository.finishClaimedReady(ctx, tx, record.ID, token, service.now().UTC())
		})
	case stateDeleting:
		if err := storageProvider(service.storage, record.ProviderID).Delete(ctx, record.StorageKey); err != nil {
			return service.normalize(ctx, err)
		}
		return service.db.WithinTx(ctx, func(ctx context.Context, tx database.Tx) error {
			return service.repository.removeClaimed(ctx, tx, record, token)
		})
	default:
		return ErrInternal
	}
}

func (service *Service) normalize(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	for _, stable := range []error{context.Canceled, context.DeadlineExceeded, ErrDenied, ErrValidation, ErrNotFound, ErrConflict,
		ErrAuthentication, ErrCSRF, ErrContentTooLarge, ErrMediaType, ErrQuotaExceeded, ErrDiskCapacity, ErrSizeMismatch} {
		if errors.Is(err, stable) {
			return stable
		}
	}
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, ErrStorageNotFound) {
		return ErrNotFound
	}
	return ErrInternal
}

func (service *Service) observe(operation string, err error) {
	outcome := "succeeded"
	if err != nil {
		outcome = "failed"
		for _, rejected := range []error{ErrDenied, ErrValidation, ErrNotFound, ErrConflict, ErrAuthentication, ErrCSRF, ErrContentTooLarge, ErrMediaType,
			ErrQuotaExceeded, ErrDiskCapacity, ErrSizeMismatch} {
			if errors.Is(err, rejected) {
				outcome = "rejected"
				break
			}
		}
	}
	service.observer.Observe(Observation{Operation: operation, Outcome: outcome})
}

type discardObserver struct{}

func (discardObserver) Observe(Observation) {}

// cleanExpiredStages 只清理数据库中已过期的预约，避免扫描或误删其他用户文件。
func (service *Service) cleanExpiredStages(ctx context.Context, now time.Time) error {
	var ids []string
	err := service.db.WithinTx(ctx, func(ctx context.Context, tx database.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT id FROM files_capacity_reservations WHERE expires_at <= ? ORDER BY expires_at, id LIMIT ?`, now, service.capacityPolicy.ReconcileBatchSize)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			ids = append(ids, id)
		}
		return rows.Err()
	})
	if err != nil {
		return service.normalize(ctx, err)
	}
	for _, id := range ids {
		if err := service.storage.Abort(ctx, reservationStage(id)); err != nil {
			return err
		}
	}
	return nil
}
