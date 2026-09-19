package product

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/app/adapters"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/application"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/application/health"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/audit"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/files"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/administration"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/authorization"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/session"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/scheduler"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/cache"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/config"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/outbox"
)

type Options struct {
	Settings          *config.RuntimeSettings
	SessionPolicy     config.SessionPolicy
	FilesRoot         string
	WorkerOwner       string
	WorkerInterval    time.Duration
	AuditRetentionAge time.Duration
}

type Runtime struct {
	Application *application.Application
	Readiness   []health.Checker
	Sessions    *session.Service
}

type schedulerParameters struct{}

func Build(ctx context.Context, db *database.Database, options Options) (Runtime, error) {
	return buildRuntime(ctx, db, options, true, true)
}

// BuildPrepared assembles a schema-prepared Server runtime with an explicit
// worker role. Desktop continues to use Build and owns automatic migration.
func BuildPrepared(ctx context.Context, db *database.Database, options Options, workersEnabled bool) (Runtime, error) {
	return buildRuntime(ctx, db, options, false, workersEnabled)
}

func buildRuntime(ctx context.Context, db *database.Database, options Options, migrate, workersEnabled bool) (Runtime, error) {
	if ctx == nil || db == nil || options.SessionPolicy == (config.SessionPolicy{}) ||
		options.FilesRoot == "" || options.WorkerOwner == "" ||
		options.WorkerInterval <= 0 || options.AuditRetentionAge <= 0 {
		return Runtime{}, errors.New("product runtime options are invalid")
	}
	if err := os.MkdirAll(options.FilesRoot, 0o700); err != nil {
		return Runtime{}, errors.New("product files root is unavailable")
	}
	if migrate {
		if err := PrepareRuntimeSchema(ctx, db, true); err != nil {
			return Runtime{}, errors.New("product migration failed")
		}
	}
	capabilities, err := authorization.NewCapabilityRegistry(db)
	if err != nil {
		return Runtime{}, errors.New("product capability registry failed")
	}
	if err := RegisterCapabilities(ctx, capabilities); err != nil {
		return Runtime{}, err
	}
	authorizationAdapters, err := adapters.NewAuthorization(db)
	if err != nil {
		return Runtime{}, errors.New("product authorization adapter failed")
	}

	trace := secureTraceID
	loginRecorder, err := audit.NewLoginRecorder(db)
	if err != nil {
		return Runtime{}, errors.New("product login audit recorder failed")
	}
	sessions, err := session.NewService(db, options.SessionPolicy, session.WithLoginFactPort(adapters.NewLoginFact(loginRecorder)), session.WithOperationAudit(audit.OperationRecorder{}))
	if err != nil {
		return Runtime{}, errors.New("product session service failed")
	}
	sessionAdapters, err := adapters.NewSession(sessions)
	if err != nil {
		return Runtime{}, errors.New("product session adapter failed")
	}
	sessionHandler, err := session.NewHTTPHandler(sessions, trace)
	if err != nil {
		return Runtime{}, errors.New("product session transport failed")
	}
	adminService, err := administration.NewService(db, administration.WithAuditRecorder(audit.OperationRecorder{}))
	if err != nil {
		return Runtime{}, errors.New("product administration service failed")
	}
	store, err := outbox.NewStore(db, append(audit.TopicSchemas(), administration.AccountDeletionRequestedTopicSchema())...)
	if err != nil {
		return Runtime{}, errors.New("product outbox store failed")
	}
	deletions, err := administration.NewDeletionService(db, store)
	if err != nil {
		return Runtime{}, errors.New("product deletion service failed")
	}
	adminHandler, err := administration.NewHTTPHandler(adminService, sessions, trace, administration.WithHTTPDeletionService(deletions))
	if err != nil {
		return Runtime{}, errors.New("product administration transport failed")
	}
	runtimeSettings := config.DefaultRuntimeSettings()
	if options.Settings != nil {
		runtimeSettings = *options.Settings
	}
	projectionCache := cache.NewRedis[authorization.Manifest](runtimeSettings.Redis)
	shellHandler := newShellRuntimeHandler(sessions, authorization.NewService(db))
	shellHandler.(*shellRuntimeHandler).projection = projectionCache
	shellHandler.(*shellRuntimeHandler).settings = runtimeSettings

	auditService, err := audit.NewService(db, authorizationAdapters.Audit(), audit.RetentionPolicy{MinimumAge: options.AuditRetentionAge, CleanupLimit: 100})
	if err != nil {
		return Runtime{}, errors.New("product audit service failed")
	}
	auditHandler, err := audit.NewHTTPHandler(auditService, sessionAdapters.Audit(), trace)
	if err != nil {
		return Runtime{}, errors.New("product audit transport failed")
	}

	registration, err := scheduler.NewTaskRegistration(
		"audit.retention",
		"清理超过配置保留期的审计记录",
		[]scheduler.ParameterField{},
		func(ctx context.Context, tx database.Tx, _ schedulerParameters) error {
			return auditService.CleanupExpiredInTx(ctx, tx)
		},
	)
	if err != nil {
		return Runtime{}, errors.New("product scheduler task registration failed")
	}
	schedulerRegistry, err := scheduler.NewRegistry(registration)
	if err != nil {
		return Runtime{}, errors.New("product scheduler registry failed")
	}
	schedulerClock := scheduler.ClockFunc(func() time.Time { return time.Now().UTC() })
	schedulerService, err := scheduler.NewService(db, authorizationAdapters.Scheduler(), schedulerRegistry, schedulerClock, scheduler.WithAuditRecorder(audit.OperationRecorder{}))
	if err != nil {
		return Runtime{}, errors.New("product scheduler service failed")
	}
	if err := schedulerService.EnsureBuiltinDefinition(ctx, "00000000-0000-4000-8000-000000000001", scheduler.DefinitionInput{Name: "审计保留期清理", TaskType: "audit.retention", Schedule: scheduler.Schedule{Cron: "*/10 * * * *", Timezone: "Asia/Shanghai"}, Parameters: map[string]any{}}); err != nil {
		return Runtime{}, errors.New("product maintenance initialization failed")
	}
	schedulerHandler, err := scheduler.NewHTTPHandler(schedulerService, sessionAdapters.Scheduler(), trace)
	if err != nil {
		return Runtime{}, errors.New("product scheduler transport failed")
	}
	schedulerExecutor, err := scheduler.NewExecutor(db, schedulerRegistry, scheduler.ExecutorConfig{
		Owner: options.WorkerOwner, BatchSize: 100, TaskTimeout: time.Minute, Clock: schedulerClock,
	})
	if err != nil {
		return Runtime{}, errors.New("product scheduler executor failed")
	}

	settings := config.DefaultRuntimeSettings()
	if options.Settings != nil {
		settings = *options.Settings
	}
	settings.Storage.Local.Root = options.FilesRoot
	storage, err := files.NewConfiguredStorage(ctx, settings.Storage)
	if err != nil {
		return Runtime{}, errors.New("product files storage failed")
	}
	storageOwnedByBuild := true
	defer func() {
		if storageOwnedByBuild {
			_ = storage.Close()
		}
	}()
	capacityPolicy := files.DefaultCapacityPolicy()
	capacityPolicy.MaximumObjectBytes = settings.Storage.MaxUploadBytes
	filesService, err := files.NewService(db, storage, authorizationAdapters.Files(), files.WithCapacityPolicy(capacityPolicy), files.WithAuditRecorder(audit.OperationRecorder{}))
	if err != nil {
		return Runtime{}, errors.New("product files service failed")
	}
	if workersEnabled {
		if err := filesService.Reconcile(ctx); err != nil {
			return Runtime{}, errors.New("product files reconciliation failed")
		}
	}
	filesHandler, err := files.NewHTTPHandler(filesService, sessionAdapters.Files(), trace)
	if err != nil {
		return Runtime{}, errors.New("product files transport failed")
	}

	consumers, err := audit.TransactionalConsumers()
	if err != nil {
		return Runtime{}, errors.New("product audit consumers failed")
	}
	deletionConsumer, err := files.NewAccountDeletionRequestedConsumer()
	if err != nil {
		return Runtime{}, errors.New("product deletion consumer failed")
	}
	consumers[files.AccountDeletionRequestedTopic] = deletionConsumer
	dispatcher, err := outbox.NewDispatcher(store, outbox.DispatcherConfig{
		Owner: options.WorkerOwner, LeaseDuration: time.Minute, RetryDelay: time.Minute, BatchSize: 100,
	}, consumers)
	if err != nil {
		return Runtime{}, errors.New("product outbox dispatcher failed")
	}
	lifecycle, err := files.NewAccountLifecycle(db, storage, deletions)
	if err != nil {
		return Runtime{}, errors.New("product account lifecycle worker failed")
	}
	workers := newWorkerGroup(db, options.WorkerOwner, options.WorkerInterval, schedulerExecutor, dispatcher, lifecycle, filesService.Reconcile)
	var schedulerWorkers *workerGroup
	if workersEnabled {
		schedulerWorkers = workers
	}

	root := http.NewServeMux()
	modules := []application.Module{
		newRouteModule(ModuleIAM, nil, route{"/api/iam/session/", withTrustedProxies(sessionHandler, runtimeSettings.HTTP.TrustedProxies)}, route{"/api/iam/account/", sessionHandler}, route{"/api/iam/administration/", adminHandler}, route{"/api/runtime/", shellHandler}),
		newRouteModule(ModuleAudit, nil, route{"/api/audit/", auditHandler}),
		newRouteModule(ModuleScheduler, schedulerWorkers, route{"/api/scheduler/", schedulerHandler}),
		newRouteModule(ModuleFiles, nil, route{"/api/files/", filesHandler}),
	}
	modules[len(modules)-1].(*routeModule).stop = func(context.Context) error { return errors.Join(workers.Stop(context.Background()), storage.Close()) }
	app, err := application.Build(application.Config{Name: "go-admin-plus"}, application.Dependencies{Handler: root}, application.NewModuleSet(modules...))
	if err != nil {
		return Runtime{}, errors.New("product application assembly failed")
	}
	storageOwnedByBuild = false
	readiness := []health.Checker{{Name: "database", Check: func(ctx context.Context) error { return db.SQL().PingContext(ctx) }}}
	if workersEnabled {
		readiness = append(readiness, health.Checker{Name: "workers", Check: workers.Check})
	}
	return Runtime{
		Application: app,
		Sessions:    sessions,
		Readiness:   readiness,
	}, nil
}

type traceContextKey struct{}

func secureTraceID(request *http.Request) string {
	if value, ok := request.Context().Value(traceContextKey{}).(string); ok {
		return value
	}
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "0000000000000000"
	}
	return hex.EncodeToString(value[:])
}

type shellRuntimeHandler struct {
	sessions      *session.Service
	authorization *authorization.Service
	projection    cache.Cache[string, authorization.Manifest]
	settings      config.RuntimeSettings
}

func newShellRuntimeHandler(sessions *session.Service, service *authorization.Service) http.Handler {
	return &shellRuntimeHandler{sessions: sessions, authorization: service}
}

func (handler *shellRuntimeHandler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	if request.Method != http.MethodGet {
		response.Header().Set("Allow", http.MethodGet)
		response.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if request.URL.Path == "/runtime/info" {
		response.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(response).Encode(struct {
			APIVersion     int      `json:"apiVersion"`
			Modules        []string `json:"modules"`
			MaxUploadBytes int64    `json:"maxUploadBytes"`
			AllowedTypes   []string `json:"allowedTypes"`
		}{1, []string{"iam", "files", "audit", "scheduler"}, handler.settings.Storage.MaxUploadBytes, handler.settings.Storage.AllowedTypes})
		return
	}
	cookie, _ := request.Cookie(session.CookieName)
	if cookie == nil {
		response.WriteHeader(http.StatusUnauthorized)
		return
	}
	issued, err := handler.sessions.Current(request.Context(), cookie.Value)
	if errors.Is(err, session.ErrAuthentication) {
		response.WriteHeader(http.StatusUnauthorized)
		return
	}
	if err != nil {
		response.WriteHeader(http.StatusInternalServerError)
		return
	}
	revision, err := handler.authorization.ProjectionRevision(request.Context())
	if err != nil {
		response.WriteHeader(http.StatusInternalServerError)
		return
	}
	manifest, err := cache.Resolve(request.Context(), handler.projection, issued.Profile.ID+":"+strconv.FormatInt(revision, 10), func(ctx context.Context) (authorization.Manifest, error) {
		return handler.authorization.Manifest(ctx, issued.Profile.ID)
	})
	if errors.Is(err, authorization.ErrDenied) {
		response.WriteHeader(http.StatusForbidden)
		return
	}
	if err != nil {
		response.WriteHeader(http.StatusInternalServerError)
		return
	}
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("X-CSRF-Token", issued.CSRF)
	if issued.Rotated {
		http.SetCookie(response, &http.Cookie{Name: session.CookieName, Value: issued.Token, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	}
	switch request.URL.Path {
	case "/runtime/identity":
		_ = json.NewEncoder(response).Encode(struct {
			Kind        string   `json:"kind"`
			SubjectID   string   `json:"subjectId"`
			Permissions []string `json:"permissions"`
			DataScope   string   `json:"dataScope"`
		}{Kind: "authenticated", SubjectID: issued.Profile.ID, Permissions: manifest.Permissions, DataScope: string(manifest.Scope)})
	case "/runtime/navigation":
		type navigation struct {
			Path       string `json:"path"`
			Permission string `json:"permission,omitempty"`
			ID         string `json:"id"`
			ParentID   string `json:"parentId"`
			Kind       string `json:"kind"`
			Label      string `json:"label"`
			RouteKey   string `json:"routeKey"`
			Icon       string `json:"icon"`
			SortOrder  int    `json:"sortOrder"`
		}
		entries := make([]navigation, len(manifest.Menus))
		for index, menu := range manifest.Menus {
			entries[index] = navigation{Path: menu.Path, Permission: menu.PermissionCode, ID: menu.ID, ParentID: menu.ParentID, Kind: menu.Kind, Label: menu.Label, RouteKey: menu.RouteKey, Icon: menu.Icon, SortOrder: menu.SortOrder}
		}
		_ = json.NewEncoder(response).Encode(entries)
	default:
		response.WriteHeader(http.StatusNotFound)
	}
}

type route struct {
	pattern string
	handler http.Handler
}

type routeModule struct {
	id         ModuleID
	migrations []application.Migration
	routes     []route
	workers    *workerGroup
	stop       func(context.Context) error
}

func newRouteModule(id ModuleID, workers *workerGroup, routes ...route) *routeModule {
	definition := moduleDefinition(id)
	migrations := make([]application.Migration, len(definition.MigrationModules))
	for index, name := range definition.MigrationModules {
		migrations[index] = application.Migration{ID: name}
	}
	return &routeModule{id: id, migrations: migrations, routes: append([]route(nil), routes...), workers: workers}
}

func (module *routeModule) ID() string { return string(module.id) }

func (module *routeModule) RegisterRoutes(handler http.Handler) error {
	mux, ok := handler.(*http.ServeMux)
	if !ok {
		return errors.New("product route target is invalid")
	}
	for _, entry := range module.routes {
		mux.Handle(entry.pattern, http.StripPrefix("/api", withAuditMetadata(entry.handler)))
	}
	return nil
}

func (module *routeModule) Migrations() []application.Migration {
	return append([]application.Migration(nil), module.migrations...)
}

func (module *routeModule) Start(ctx context.Context) error {
	if module.workers == nil {
		return nil
	}
	return module.workers.Start(ctx)
}

func (module *routeModule) Stop(ctx context.Context) error {
	var result error
	if module.workers != nil {
		result = module.workers.Stop(ctx)
	}
	if module.stop != nil {
		result = errors.Join(result, module.stop(ctx))
	}
	return result
}

func moduleDefinition(id ModuleID) ModuleDefinition {
	for _, definition := range moduleDefinitions {
		if definition.ID == id {
			return definition
		}
	}
	return ModuleDefinition{ID: id}
}

func withAuditMetadata(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		source := audit.SourceWeb
		if r.Header.Get("X-Client-Platform") == "desktop" {
			source = audit.SourceDesktop
		}
		trace := secureTraceID(r)
		w.Header().Set("X-Trace-Id", trace)
		ctx := audit.WithRequestMetadata(context.WithValue(r.Context(), traceContextKey{}, trace), source, trace)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
