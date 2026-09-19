// Package config loads one validated runtime profile into an immutable value.
// It never reads process globals; bootstraps must pass every source explicitly.
package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Profile identifies one independently validated runtime configuration shape.
type Profile string

const (
	ProfileServerPostgres Profile = "server-postgres"
	ProfileServerSQLite   Profile = "server-sqlite"
	ProfileDesktopSQLite  Profile = "desktop-sqlite"
)

// Input contains the complete startup-time configuration source set. CLI keys
// are configuration paths and may contain only non-sensitive overrides.
type Input struct {
	Profile     Profile
	File        string
	Environment map[string]string
	CLI         map[string]string
	Desktop     DesktopMaterial
}

// String intentionally omits every source value.
func (input Input) String() string {
	return fmt.Sprintf("config input profile=%s values=redacted", input.Profile)
}

// GoString intentionally omits every source value.
func (input Input) GoString() string {
	return fmt.Sprintf("config.Input{profile:%q, values:redacted}", input.Profile)
}

// MarshalJSON prevents generic diagnostics from traversing configuration
// sources. Callers must use the typed accessors after Load for actual values.
func (input Input) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Profile Profile `json:"profile"`
		Values  string  `json:"values"`
	}{Profile: input.Profile, Values: "redacted"})
}

// LogValue prevents structured log handlers from reflecting sensitive fields.
func (input Input) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("profile", string(input.Profile)),
		slog.String("values", "redacted"),
	)
}

// DesktopMaterial is supplied by the native host for one sidecar invocation.
// It is deliberately separate from file, environment, and CLI configuration.
type DesktopMaterial struct {
	DataDirectory string
	LogDirectory  string
	LoopbackPort  uint16
	StartupToken  string
}

// String intentionally omits paths and the one-time token.
func (material DesktopMaterial) String() string { return "desktop launch material redacted" }

// GoString intentionally omits paths and the one-time token.
func (material DesktopMaterial) GoString() string { return "config.DesktopMaterial{redacted}" }

// MarshalJSON prevents paths and launch material from entering JSON output.
func (material DesktopMaterial) MarshalJSON() ([]byte, error) {
	return []byte(`{"values":"redacted"}`), nil
}

// LogValue prevents structured log handlers from reflecting native material.
func (material DesktopMaterial) LogValue() slog.Value {
	return slog.GroupValue(slog.String("values", "redacted"))
}

// Snapshot owns one profile value. Its fields are intentionally unexported so
// callers cannot mutate runtime configuration after construction.
type Snapshot struct {
	settings       *RuntimeSettings
	profile        Profile
	serverSQLite   ServerSQLite
	serverPostgres ServerPostgres
	desktopSQLite  DesktopSQLite
}

// Profile returns the one profile owned by this snapshot.
func (snapshot Snapshot) Profile() Profile { return snapshot.profile }

func (snapshot Snapshot) String() string {
	return fmt.Sprintf("config snapshot profile=%s values=redacted", snapshot.profile)
}

func (snapshot Snapshot) GoString() string {
	return fmt.Sprintf("config.Snapshot{profile:%q, values:redacted}", snapshot.profile)
}

// ServerSQLite returns an owned value only for ProfileServerSQLite.
func (snapshot Snapshot) ServerSQLite() (ServerSQLite, bool) {
	return snapshot.serverSQLite, snapshot.profile == ProfileServerSQLite
}

// ServerPostgres returns an owned value only for ProfileServerPostgres.
func (snapshot Snapshot) ServerPostgres() (ServerPostgres, bool) {
	return snapshot.serverPostgres, snapshot.profile == ProfileServerPostgres
}

// DesktopSQLite returns an owned value only for ProfileDesktopSQLite.
func (snapshot Snapshot) DesktopSQLite() (DesktopSQLite, bool) {
	return snapshot.desktopSQLite, snapshot.profile == ProfileDesktopSQLite
}

// ServerSQLite is the immutable Server SQLite runtime profile.
type ServerSQLite struct {
	httpListen   string
	logLevel     string
	databasePath string
	session      SessionPolicy
}

// HTTPListen returns the validated server listen address.
func (profile ServerSQLite) HTTPListen() string { return profile.httpListen }

// LogLevel returns the validated log level.
func (profile ServerSQLite) LogLevel() string { return profile.logLevel }

// DatabasePath returns the Server SQLite database path.
func (profile ServerSQLite) DatabasePath() string         { return profile.databasePath }
func (profile ServerSQLite) SessionPolicy() SessionPolicy { return profile.session }
func (profile ServerSQLite) String() string               { return "server-sqlite configuration redacted" }
func (profile ServerSQLite) GoString() string             { return "config.ServerSQLite{redacted}" }

// ServerPostgres is the immutable Server PostgreSQL runtime profile.
type ServerPostgres struct {
	httpListen  string
	logLevel    string
	databaseDSN string
	session     SessionPolicy
}

// HTTPListen returns the validated server listen address.
func (profile ServerPostgres) HTTPListen() string { return profile.httpListen }

// LogLevel returns the validated log level.
func (profile ServerPostgres) LogLevel() string { return profile.logLevel }

// DatabaseDSN returns the resolved in-memory secret for dependency construction.
// Callers must never log, format, persist, or expose the returned value.
func (profile ServerPostgres) DatabaseDSN() string          { return profile.databaseDSN }
func (profile ServerPostgres) SessionPolicy() SessionPolicy { return profile.session }
func (profile ServerPostgres) String() string               { return "server-postgres configuration redacted" }
func (profile ServerPostgres) GoString() string             { return "config.ServerPostgres{redacted}" }

// DesktopSQLite is the immutable native-host-provided Desktop SQLite profile.
type DesktopSQLite struct {
	logLevel        string
	dataDirectory   string
	logDirectory    string
	loopbackAddress string
	startupToken    string
	session         SessionPolicy
}

// LogLevel returns the validated log level.
func (profile DesktopSQLite) LogLevel() string { return profile.logLevel }

// DataDirectory returns the absolute data directory supplied by Tauri.
func (profile DesktopSQLite) DataDirectory() string { return profile.dataDirectory }

// LogDirectory returns the distinct absolute log directory supplied by Tauri.
func (profile DesktopSQLite) LogDirectory() string { return profile.logDirectory }

// LoopbackAddress returns the fixed-loopback address using Tauri's random port.
func (profile DesktopSQLite) LoopbackAddress() string { return profile.loopbackAddress }

// StartupToken returns the one-time Tauri launch token. Callers must not log,
// persist, place in a URL, or expose this value to WebView JavaScript.
func (profile DesktopSQLite) StartupToken() string         { return profile.startupToken }
func (profile DesktopSQLite) SessionPolicy() SessionPolicy { return profile.session }
func (profile DesktopSQLite) String() string               { return "desktop-sqlite configuration redacted" }
func (profile DesktopSQLite) GoString() string             { return "config.DesktopSQLite{redacted}" }

type serverSQLiteFile struct {
	Profile  Profile        `json:"profile"`
	HTTP     httpConfig     `json:"http"`
	Log      logConfig      `json:"log"`
	Database databaseConfig `json:"database"`
	Session  sessionConfig  `json:"session"`
}

type serverPostgresFile struct {
	Profile Profile       `json:"profile"`
	HTTP    httpConfig    `json:"http"`
	Log     logConfig     `json:"log"`
	Session sessionConfig `json:"session"`
}

type desktopSQLiteFile struct {
	Profile Profile       `json:"profile"`
	Log     logConfig     `json:"log"`
	Session sessionConfig `json:"session"`
}

type httpConfig struct {
	Listen *string `json:"listen"`
}

type logConfig struct {
	Level *string `json:"level"`
}

type databaseConfig struct {
	Path *string `json:"path"`
}

// SessionPolicy is an immutable, non-sensitive runtime security policy.
type SessionPolicy struct {
	idleTimeout      time.Duration
	absoluteTimeout  time.Duration
	rotationInterval time.Duration
}

func (policy SessionPolicy) IdleTimeout() time.Duration      { return policy.idleTimeout }
func (policy SessionPolicy) AbsoluteTimeout() time.Duration  { return policy.absoluteTimeout }
func (policy SessionPolicy) RotationInterval() time.Duration { return policy.rotationInterval }

func NewSessionPolicy(idle, absolute, rotation time.Duration) (SessionPolicy, error) {
	if idle < time.Minute || idle > 24*time.Hour {
		return SessionPolicy{}, fmt.Errorf("configuration session.idleTimeoutSeconds: must be between 60 and 86400")
	}
	if absolute < 5*time.Minute || absolute > 30*24*time.Hour {
		return SessionPolicy{}, fmt.Errorf("configuration session.absoluteTimeoutSeconds: must be between 300 and 2592000")
	}
	if rotation < time.Minute || rotation > idle {
		return SessionPolicy{}, fmt.Errorf("configuration session.rotationIntervalSeconds: must be between 60 and idle timeout")
	}
	if idle > absolute {
		return SessionPolicy{}, fmt.Errorf("configuration session.idleTimeoutSeconds: must not exceed absolute timeout")
	}
	return SessionPolicy{idleTimeout: idle, absoluteTimeout: absolute, rotationInterval: rotation}, nil
}

func DefaultSessionPolicy() SessionPolicy {
	policy, err := NewSessionPolicy(30*time.Minute, 12*time.Hour, 15*time.Minute)
	if err != nil {
		panic("invalid built-in session policy")
	}
	return policy
}

type sessionConfig struct {
	IdleTimeoutSeconds      *int64 `json:"idleTimeoutSeconds"`
	AbsoluteTimeoutSeconds  *int64 `json:"absoluteTimeoutSeconds"`
	RotationIntervalSeconds *int64 `json:"rotationIntervalSeconds"`
}

// Load applies defaults, file, environment, then CLI and returns an owned
// immutable snapshot. It performs no listener, window, or dependency actions.
func Load(input Input) (Snapshot, error) {
	if err := validateSourceKeys(input); err != nil {
		return Snapshot{}, err
	}
	switch input.Profile {
	case ProfileServerPostgres:
		return loadServerPostgres(input)
	case ProfileServerSQLite:
		return loadServerSQLite(input)
	case ProfileDesktopSQLite:
		return loadDesktopSQLite(input)
	default:
		return Snapshot{}, fmt.Errorf("configuration profile: unsupported profile")
	}
}

func validateSourceKeys(input Input) error {
	commonEnvironment := map[string]struct{}{
		"GO_ADMIN_HTTP_LISTEN":              {},
		"GO_ADMIN_LOG_LEVEL":                {},
		"GO_ADMIN_SESSION_IDLE_SECONDS":     {},
		"GO_ADMIN_SESSION_ABSOLUTE_SECONDS": {},
		"GO_ADMIN_SESSION_ROTATION_SECONDS": {},
	}
	commonCLI := map[string]struct{}{
		"http.listen":                     {},
		"log.level":                       {},
		"session.idleTimeoutSeconds":      {},
		"session.absoluteTimeoutSeconds":  {},
		"session.rotationIntervalSeconds": {},
	}
	allowedEnvironment := commonEnvironment
	allowedCLI := commonCLI
	switch input.Profile {
	case ProfileServerPostgres:
		allowedEnvironment["GO_ADMIN_DATABASE_DSN"] = struct{}{}
		allowedEnvironment["GO_ADMIN_DATABASE_DSN_FILE"] = struct{}{}
	case ProfileServerSQLite:
		allowedEnvironment["GO_ADMIN_SQLITE_PATH"] = struct{}{}
		allowedCLI["database.path"] = struct{}{}
	case ProfileDesktopSQLite:
		allowedEnvironment = map[string]struct{}{
			"GO_ADMIN_LOG_LEVEL": {}, "GO_ADMIN_SESSION_IDLE_SECONDS": {},
			"GO_ADMIN_SESSION_ABSOLUTE_SECONDS": {}, "GO_ADMIN_SESSION_ROTATION_SECONDS": {},
		}
		allowedCLI = map[string]struct{}{
			"log.level": {}, "session.idleTimeoutSeconds": {},
			"session.absoluteTimeoutSeconds": {}, "session.rotationIntervalSeconds": {},
		}
	default:
		return fmt.Errorf("configuration profile: unsupported profile")
	}
	for key := range input.Environment {
		if !strings.HasPrefix(key, "GO_ADMIN_") {
			continue
		}
		if _, ok := allowedEnvironment[key]; !ok {
			return fmt.Errorf("configuration environment.%s: unknown field", key)
		}
	}
	for key := range input.CLI {
		if _, ok := allowedCLI[key]; !ok {
			return fmt.Errorf("configuration cli.%s: override is not permitted", key)
		}
	}
	return nil
}

func loadDesktopSQLite(input Input) (Snapshot, error) {
	if !filepath.IsAbs(input.Desktop.DataDirectory) {
		return Snapshot{}, fmt.Errorf("configuration desktop.dataDirectory: absolute Tauri host path is required")
	}
	if !filepath.IsAbs(input.Desktop.LogDirectory) {
		return Snapshot{}, fmt.Errorf("configuration desktop.logDirectory: absolute Tauri host path is required")
	}
	if filepath.Clean(input.Desktop.DataDirectory) == filepath.Clean(input.Desktop.LogDirectory) {
		return Snapshot{}, fmt.Errorf("configuration desktop.logDirectory: must not conflict with data directory")
	}
	if input.Desktop.LoopbackPort == 0 {
		return Snapshot{}, fmt.Errorf("configuration desktop.loopbackPort: Tauri host port is required")
	}
	if len(input.Desktop.StartupToken) < 32 {
		return Snapshot{}, fmt.Errorf("configuration desktop.startupToken: Tauri host token is required")
	}
	value := DesktopSQLite{
		logLevel:        "info",
		dataDirectory:   filepath.Clean(input.Desktop.DataDirectory),
		logDirectory:    filepath.Clean(input.Desktop.LogDirectory),
		loopbackAddress: net.JoinHostPort("127.0.0.1", strconv.Itoa(int(input.Desktop.LoopbackPort))),
		startupToken:    input.Desktop.StartupToken,
		session:         DefaultSessionPolicy(),
	}
	var sessionFile sessionConfig
	if input.File != "" {
		file, err := decodeFile[desktopSQLiteFile](input.File)
		if err != nil {
			return Snapshot{}, err
		}
		if file.Profile != input.Profile {
			return Snapshot{}, fmt.Errorf("configuration profile: conflicts with selected profile")
		}
		if file.Log.Level != nil {
			value.logLevel = *file.Log.Level
		}
		sessionFile = file.Session
	}
	if candidate, exists := input.Environment["GO_ADMIN_LOG_LEVEL"]; exists {
		value.logLevel = candidate
	}
	if candidate, exists := input.CLI["log.level"]; exists {
		value.logLevel = candidate
	}
	if err := validateLogLevel(value.logLevel); err != nil {
		return Snapshot{}, err
	}
	policy, err := resolveSessionPolicy(value.session, sessionFile, input.Environment, input.CLI)
	if err != nil {
		return Snapshot{}, err
	}
	value.session = policy
	return Snapshot{profile: input.Profile, desktopSQLite: value}, nil
}

func loadServerPostgres(input Input) (Snapshot, error) {
	direct, hasDirect := input.Environment["GO_ADMIN_DATABASE_DSN"]
	secretPath, hasFile := input.Environment["GO_ADMIN_DATABASE_DSN_FILE"]
	if hasDirect && hasFile {
		return Snapshot{}, fmt.Errorf("configuration database.dsn: conflicting secret sources")
	}
	dsn := direct
	if hasFile {
		contents, err := os.ReadFile(secretPath)
		if err != nil {
			return Snapshot{}, fmt.Errorf("configuration database.dsn: secret file is unreadable")
		}
		dsn = strings.TrimRight(string(contents), "\r\n")
	}
	if strings.TrimSpace(dsn) == "" {
		return Snapshot{}, fmt.Errorf("configuration database.dsn: secret is required")
	}
	value := ServerPostgres{
		httpListen:  "127.0.0.1:8080",
		logLevel:    "info",
		databaseDSN: dsn,
		session:     DefaultSessionPolicy(),
	}
	var sessionFile sessionConfig
	if input.File != "" {
		file, err := decodeFile[serverPostgresFile](input.File)
		if err != nil {
			return Snapshot{}, err
		}
		if file.Profile != input.Profile {
			return Snapshot{}, fmt.Errorf("configuration profile: conflicts with selected profile")
		}
		if file.HTTP.Listen != nil {
			value.httpListen = *file.HTTP.Listen
		}
		if file.Log.Level != nil {
			value.logLevel = *file.Log.Level
		}
		sessionFile = file.Session
	}
	if candidate, exists := input.Environment["GO_ADMIN_HTTP_LISTEN"]; exists {
		value.httpListen = candidate
	}
	if candidate, exists := input.Environment["GO_ADMIN_LOG_LEVEL"]; exists {
		value.logLevel = candidate
	}
	if candidate, exists := input.CLI["http.listen"]; exists {
		value.httpListen = candidate
	}
	if candidate, exists := input.CLI["log.level"]; exists {
		value.logLevel = candidate
	}
	if err := validateServer(value.httpListen, value.logLevel); err != nil {
		return Snapshot{}, err
	}
	policy, err := resolveSessionPolicy(value.session, sessionFile, input.Environment, input.CLI)
	if err != nil {
		return Snapshot{}, err
	}
	value.session = policy
	return Snapshot{profile: input.Profile, serverPostgres: value}, nil
}

func loadServerSQLite(input Input) (Snapshot, error) {
	value := ServerSQLite{
		httpListen:   "127.0.0.1:8080",
		logLevel:     "info",
		databasePath: "go-admin-plus.sqlite3",
		session:      DefaultSessionPolicy(),
	}
	var sessionFile sessionConfig
	if input.File != "" {
		file, err := decodeFile[serverSQLiteFile](input.File)
		if err != nil {
			return Snapshot{}, err
		}
		if file.Profile != input.Profile {
			return Snapshot{}, fmt.Errorf("configuration profile: conflicts with selected profile")
		}
		if file.HTTP.Listen != nil {
			value.httpListen = *file.HTTP.Listen
		}
		if file.Log.Level != nil {
			value.logLevel = *file.Log.Level
		}
		if file.Database.Path != nil {
			value.databasePath = *file.Database.Path
		}
		sessionFile = file.Session
	}
	if candidate, exists := input.Environment["GO_ADMIN_HTTP_LISTEN"]; exists {
		value.httpListen = candidate
	}
	if candidate, exists := input.Environment["GO_ADMIN_LOG_LEVEL"]; exists {
		value.logLevel = candidate
	}
	if candidate, exists := input.Environment["GO_ADMIN_SQLITE_PATH"]; exists {
		value.databasePath = candidate
	}
	if candidate, exists := input.CLI["http.listen"]; exists {
		value.httpListen = candidate
	}
	if candidate, exists := input.CLI["log.level"]; exists {
		value.logLevel = candidate
	}
	if candidate, exists := input.CLI["database.path"]; exists {
		value.databasePath = candidate
	}
	if err := validateServer(value.httpListen, value.logLevel); err != nil {
		return Snapshot{}, err
	}
	if strings.TrimSpace(value.databasePath) == "" || strings.ContainsRune(value.databasePath, '\x00') {
		return Snapshot{}, fmt.Errorf("configuration database.path: non-empty filesystem path is required")
	}
	policy, err := resolveSessionPolicy(value.session, sessionFile, input.Environment, input.CLI)
	if err != nil {
		return Snapshot{}, err
	}
	value.session = policy
	return Snapshot{profile: input.Profile, serverSQLite: value}, nil
}

func resolveSessionPolicy(base SessionPolicy, file sessionConfig, environment, cli map[string]string) (SessionPolicy, error) {
	idle := int64(base.IdleTimeout() / time.Second)
	absolute := int64(base.AbsoluteTimeout() / time.Second)
	rotation := int64(base.RotationInterval() / time.Second)
	if file.IdleTimeoutSeconds != nil {
		idle = *file.IdleTimeoutSeconds
	}
	if file.AbsoluteTimeoutSeconds != nil {
		absolute = *file.AbsoluteTimeoutSeconds
	}
	if file.RotationIntervalSeconds != nil {
		rotation = *file.RotationIntervalSeconds
	}
	var err error
	if value, ok := environment["GO_ADMIN_SESSION_IDLE_SECONDS"]; ok {
		idle, err = parseSessionSeconds("session.idleTimeoutSeconds", value)
		if err != nil {
			return SessionPolicy{}, err
		}
	}
	if value, ok := environment["GO_ADMIN_SESSION_ABSOLUTE_SECONDS"]; ok {
		absolute, err = parseSessionSeconds("session.absoluteTimeoutSeconds", value)
		if err != nil {
			return SessionPolicy{}, err
		}
	}
	if value, ok := environment["GO_ADMIN_SESSION_ROTATION_SECONDS"]; ok {
		rotation, err = parseSessionSeconds("session.rotationIntervalSeconds", value)
		if err != nil {
			return SessionPolicy{}, err
		}
	}
	if value, ok := cli["session.idleTimeoutSeconds"]; ok {
		idle, err = parseSessionSeconds("session.idleTimeoutSeconds", value)
		if err != nil {
			return SessionPolicy{}, err
		}
	}
	if value, ok := cli["session.absoluteTimeoutSeconds"]; ok {
		absolute, err = parseSessionSeconds("session.absoluteTimeoutSeconds", value)
		if err != nil {
			return SessionPolicy{}, err
		}
	}
	if value, ok := cli["session.rotationIntervalSeconds"]; ok {
		rotation, err = parseSessionSeconds("session.rotationIntervalSeconds", value)
		if err != nil {
			return SessionPolicy{}, err
		}
	}
	if err := validateSessionSeconds(idle, absolute, rotation); err != nil {
		return SessionPolicy{}, err
	}
	return NewSessionPolicy(time.Duration(idle)*time.Second, time.Duration(absolute)*time.Second, time.Duration(rotation)*time.Second)
}

func parseSessionSeconds(field, value string) (int64, error) {
	seconds, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("configuration %s: must be an integer number of seconds", field)
	}
	return seconds, nil
}

func validateSessionSeconds(idle, absolute, rotation int64) error {
	if idle < 60 || idle > 86400 {
		return fmt.Errorf("configuration session.idleTimeoutSeconds: must be between 60 and 86400")
	}
	if absolute < 300 || absolute > 2592000 {
		return fmt.Errorf("configuration session.absoluteTimeoutSeconds: must be between 300 and 2592000")
	}
	if rotation < 60 || rotation > idle {
		return fmt.Errorf("configuration session.rotationIntervalSeconds: must be between 60 and idle timeout")
	}
	if idle > absolute {
		return fmt.Errorf("configuration session.idleTimeoutSeconds: must not exceed absolute timeout")
	}
	return nil
}

func validateServer(listen, logLevel string) error {
	_, port, err := net.SplitHostPort(listen)
	if err != nil {
		return fmt.Errorf("configuration http.listen: valid host and port are required")
	}
	portNumber, err := strconv.ParseUint(port, 10, 16)
	if err != nil || portNumber == 0 {
		return fmt.Errorf("configuration http.listen: port must be between 1 and 65535")
	}
	return validateLogLevel(logLevel)
}

func validateLogLevel(level string) error {
	switch level {
	case "debug", "info", "warn", "error":
		return nil
	default:
		return fmt.Errorf("configuration log.level: must be debug, info, warn, or error")
	}
}

func decodeFile[T any](path string) (T, error) {
	var value T
	contents, err := os.ReadFile(path)
	if err != nil {
		return value, fmt.Errorf("configuration file: unreadable")
	}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		const unknownPrefix = `json: unknown field "`
		if strings.HasPrefix(err.Error(), unknownPrefix) {
			field := strings.TrimSuffix(strings.TrimPrefix(err.Error(), unknownPrefix), `"`)
			return value, fmt.Errorf("configuration file.%s: unknown field", field)
		}
		return value, fmt.Errorf("configuration file: invalid JSON")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return value, fmt.Errorf("configuration file: must contain one JSON object")
	}
	return value, nil
}
