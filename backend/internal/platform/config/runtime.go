package config

import (
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// RuntimeSettings 是跨数据库一致的运行配置。密钥不得直接写入日志。
type RuntimeSettings struct {
	HTTP struct {
		Listen         string   `yaml:"listen"`
		TrustedProxies []string `yaml:"trustedProxies"`
	} `yaml:"http"`
	Log struct {
		Level string `yaml:"level"`
	} `yaml:"log"`
	Database struct {
		Driver string `yaml:"driver"`
		SQLite struct {
			Path string `yaml:"path"`
		} `yaml:"sqlite"`
		Postgres struct {
			DSN     string `yaml:"dsn"`
			DSNFile string `yaml:"dsnFile"`
		} `yaml:"postgres"`
	} `yaml:"database"`
	Redis   RedisSettings   `yaml:"redis"`
	Storage StorageSettings `yaml:"storage"`
	Runtime struct {
		Role    string `yaml:"role"`
		DataDir string `yaml:"dataDir"`
	} `yaml:"runtime"`
	Session struct {
		Idle     int64 `yaml:"idleTimeoutSeconds"`
		Absolute int64 `yaml:"absoluteTimeoutSeconds"`
		Rotation int64 `yaml:"rotationIntervalSeconds"`
	} `yaml:"session"`
	Audit struct {
		RetentionDays int `yaml:"retentionDays"`
	} `yaml:"audit"`
}

type RedisSettings struct {
	Enabled      bool   `yaml:"enabled"`
	Address      string `yaml:"address"`
	Username     string `yaml:"username"`
	Password     string `yaml:"password"`
	PasswordFile string `yaml:"passwordFile"`
	Database     int    `yaml:"database"`
	TLS          bool   `yaml:"tls"`
	Namespace    string `yaml:"namespace"`
}

type StorageSettings struct {
	DefaultProvider string `yaml:"defaultProvider"`
	Local           struct {
		Root string `yaml:"root"`
	} `yaml:"local"`
	S3 struct {
		Endpoint      string `yaml:"endpoint"`
		Region        string `yaml:"region"`
		Bucket        string `yaml:"bucket"`
		AccessKey     string `yaml:"accessKey"`
		SecretKey     string `yaml:"secretKey"`
		SecretKeyFile string `yaml:"secretKeyFile"`
		PathStyle     bool   `yaml:"pathStyle"`
	} `yaml:"s3"`
	MaxUploadBytes int64    `yaml:"maxUploadBytes"`
	AllowedTypes   []string `yaml:"allowedTypes"`
}

func (RuntimeSettings) String() string               { return "runtime settings [redacted]" }
func (RuntimeSettings) GoString() string             { return "config.RuntimeSettings{redacted}" }
func (RuntimeSettings) MarshalJSON() ([]byte, error) { return []byte(`{"values":"redacted"}`), nil }
func (RedisSettings) String() string                 { return "redis settings [redacted]" }
func (RedisSettings) GoString() string               { return "config.RedisSettings{redacted}" }
func (StorageSettings) String() string               { return "storage settings [redacted]" }
func (StorageSettings) GoString() string             { return "config.StorageSettings{redacted}" }

func DefaultRuntimeSettings() RuntimeSettings {
	var c RuntimeSettings
	c.HTTP.Listen = "127.0.0.1:8080"
	c.Log.Level = "info"
	c.Database.Driver = "sqlite"
	c.Database.SQLite.Path = "app.db"
	c.Redis.Address, c.Redis.Namespace = "127.0.0.1:6379", "go-admin-plus"
	c.Storage.DefaultProvider, c.Storage.Local.Root = "local", "files"
	c.Storage.S3.Region = "us-east-1"
	c.Storage.MaxUploadBytes = 10 << 20
	c.Storage.AllowedTypes = []string{"application/pdf", "image/jpeg", "image/png", "text/plain"}
	c.Runtime.Role, c.Runtime.DataDir = "all", ".data/server"
	c.Session.Idle, c.Session.Absolute, c.Session.Rotation = 1800, 43200, 900
	c.Audit.RetentionDays = 30
	return c
}

// Settings 返回独立副本；旧的内部 profile 构造器也得到同样的默认技术配置。
func (snapshot Snapshot) Settings() RuntimeSettings {
	if snapshot.settings == nil {
		return DefaultRuntimeSettings()
	}
	c := *snapshot.settings
	c.HTTP.TrustedProxies = append([]string(nil), c.HTTP.TrustedProxies...)
	c.Storage.AllowedTypes = append([]string(nil), c.Storage.AllowedTypes...)
	return c
}

// LoadRuntime 使用统一 YAML，按默认值、文件、环境变量、显式 CLI 的顺序解析。
// 数据库类型在读取文件后确定，用户无需同时维护 profile 与数据库配置。
func LoadRuntime(input Input) (Snapshot, error) {
	c := DefaultRuntimeSettings()
	path := input.File
	if path == "" {
		path = input.Environment["GO_ADMIN_CONFIG_FILE"]
	}
	if path != "" {
		f, err := os.Open(path)
		if err != nil {
			return Snapshot{}, fmt.Errorf("configuration file: unreadable")
		}
		defer f.Close()
		d := yaml.NewDecoder(io.LimitReader(f, 1<<20))
		d.KnownFields(true)
		if err := d.Decode(&c); err != nil {
			return Snapshot{}, fmt.Errorf("configuration file: invalid YAML or unknown field")
		}
		var extra any
		if err := d.Decode(&extra); err != io.EOF {
			return Snapshot{}, fmt.Errorf("configuration file: expected one YAML document")
		}
	}
	values := map[string]*string{
		"GO_ADMIN_DATABASE_DRIVER":   &c.Database.Driver,
		"GO_ADMIN_DATABASE_DSN":      &c.Database.Postgres.DSN,
		"GO_ADMIN_DATABASE_DSN_FILE": &c.Database.Postgres.DSNFile,
		"GO_ADMIN_SQLITE_PATH":       &c.Database.SQLite.Path,
		"GO_ADMIN_HTTP_LISTEN":       &c.HTTP.Listen, "GO_ADMIN_LOG_LEVEL": &c.Log.Level,
		"GO_ADMIN_DATA_DIR": &c.Runtime.DataDir, "GO_ADMIN_RUNTIME_ROLE": &c.Runtime.Role,
		"GO_ADMIN_REDIS_ADDRESS": &c.Redis.Address, "GO_ADMIN_REDIS_PASSWORD": &c.Redis.Password,
		"GO_ADMIN_REDIS_PASSWORD_FILE": &c.Redis.PasswordFile,
		"GO_ADMIN_STORAGE_PROVIDER":    &c.Storage.DefaultProvider,
		"GO_ADMIN_S3_ACCESS_KEY":       &c.Storage.S3.AccessKey, "GO_ADMIN_S3_SECRET_KEY": &c.Storage.S3.SecretKey,
		"GO_ADMIN_S3_SECRET_KEY_FILE": &c.Storage.S3.SecretKeyFile,
	}
	for key, target := range values {
		if v, ok := input.Environment[key]; ok {
			*target = v
		}
	}
	if v, ok := input.Environment["GO_ADMIN_REDIS_ENABLED"]; ok {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return Snapshot{}, fmt.Errorf("configuration redis.enabled: invalid boolean")
		}
		c.Redis.Enabled = b
	}
	for key, target := range map[string]*int64{"GO_ADMIN_SESSION_IDLE_SECONDS": &c.Session.Idle, "GO_ADMIN_SESSION_ABSOLUTE_SECONDS": &c.Session.Absolute, "GO_ADMIN_SESSION_ROTATION_SECONDS": &c.Session.Rotation} {
		if v, ok := input.Environment[key]; ok {
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				return Snapshot{}, fmt.Errorf("configuration session: invalid duration")
			}
			*target = n
		}
	}
	if input.Profile != "" {
		switch input.Profile {
		case ProfileServerSQLite:
			c.Database.Driver = "sqlite"
		case ProfileServerPostgres:
			c.Database.Driver = "postgres"
		default:
			return Snapshot{}, fmt.Errorf("configuration profile: unsupported server profile")
		}
	}
	for key, value := range input.CLI {
		switch key {
		case "database.path":
			c.Database.SQLite.Path = value
		case "http.listen":
			c.HTTP.Listen = value
		case "log.level":
			c.Log.Level = value
		case "runtime.dataDir":
			c.Runtime.DataDir = value
		case "runtime.role":
			c.Runtime.Role = value
		default:
			return Snapshot{}, fmt.Errorf("configuration CLI: unknown key %s", key)
		}
	}
	if c.Runtime.Role != "all" && c.Runtime.Role != "api" && c.Runtime.Role != "worker" {
		return Snapshot{}, fmt.Errorf("configuration runtime.role: invalid role")
	}
	if c.Database.Driver == "sqlite" && c.Runtime.Role != "all" {
		return Snapshot{}, fmt.Errorf("configuration runtime.role: SQLite requires all")
	}
	for _, kind := range c.Storage.AllowedTypes {
		switch kind {
		case "application/pdf", "image/jpeg", "image/png", "text/plain":
		default:
			return Snapshot{}, fmt.Errorf("configuration storage.allowedTypes: unsupported content validator")
		}
	}
	if c.Runtime.DataDir == "" || c.Audit.RetentionDays < 1 || c.Audit.RetentionDays > 3650 || c.Storage.MaxUploadBytes < 1 || c.Storage.MaxUploadBytes > 1<<30 || len(c.Storage.AllowedTypes) == 0 {
		return Snapshot{}, fmt.Errorf("configuration runtime, audit or storage: invalid limits")
	}
	if c.Storage.DefaultProvider != "local" && c.Storage.DefaultProvider != "s3" {
		return Snapshot{}, fmt.Errorf("configuration storage.defaultProvider: unsupported provider")
	}
	if c.Storage.DefaultProvider == "s3" && c.Storage.S3.Bucket == "" {
		return Snapshot{}, fmt.Errorf("configuration storage.s3.bucket: required")
	}
	for _, cidr := range c.HTTP.TrustedProxies {
		if _, err := netip.ParsePrefix(cidr); err != nil {
			return Snapshot{}, fmt.Errorf("configuration http.trustedProxies: invalid CIDR")
		}
	}
	if c.Redis.Enabled && (c.Redis.Address == "" || c.Redis.Namespace == "" || c.Redis.Database < 0) {
		return Snapshot{}, fmt.Errorf("configuration redis: invalid address, namespace or database")
	}
	for _, pair := range []struct {
		value *string
		file  string
	}{{&c.Database.Postgres.DSN, c.Database.Postgres.DSNFile}, {&c.Redis.Password, c.Redis.PasswordFile}, {&c.Storage.S3.SecretKey, c.Storage.S3.SecretKeyFile}} {
		if pair.file == "" {
			continue
		}
		if *pair.value != "" {
			return Snapshot{}, fmt.Errorf("configuration secret: direct value and file are mutually exclusive")
		}
		data, err := os.ReadFile(pair.file)
		if err != nil || len(data) > 8192 {
			return Snapshot{}, fmt.Errorf("configuration secret file: unreadable or too large")
		}
		*pair.value = strings.TrimRight(string(data), "\r\n")
	}
	var err error
	c.Runtime.DataDir, err = filepath.Abs(c.Runtime.DataDir)
	if err != nil {
		return Snapshot{}, fmt.Errorf("configuration runtime.dataDir: invalid path")
	}
	if !filepath.IsAbs(c.Database.SQLite.Path) {
		c.Database.SQLite.Path = filepath.Join(c.Runtime.DataDir, c.Database.SQLite.Path)
	}
	if !filepath.IsAbs(c.Storage.Local.Root) {
		c.Storage.Local.Root = filepath.Join(c.Runtime.DataDir, c.Storage.Local.Root)
	}
	legacy := Input{CLI: map[string]string{"http.listen": c.HTTP.Listen, "log.level": c.Log.Level}, Environment: map[string]string{
		"GO_ADMIN_SESSION_IDLE_SECONDS": strconv.FormatInt(c.Session.Idle, 10), "GO_ADMIN_SESSION_ABSOLUTE_SECONDS": strconv.FormatInt(c.Session.Absolute, 10), "GO_ADMIN_SESSION_ROTATION_SECONDS": strconv.FormatInt(c.Session.Rotation, 10),
	}}
	switch c.Database.Driver {
	case "sqlite":
		legacy.Profile = ProfileServerSQLite
		legacy.CLI["database.path"] = c.Database.SQLite.Path
	case "postgres":
		legacy.Profile = ProfileServerPostgres
		legacy.Environment["GO_ADMIN_DATABASE_DSN"] = c.Database.Postgres.DSN
	default:
		return Snapshot{}, fmt.Errorf("configuration database.driver: expected sqlite or postgres")
	}
	snapshot, err := Load(legacy)
	if err != nil {
		return Snapshot{}, err
	}
	snapshot.settings = &c
	return snapshot, nil
}
