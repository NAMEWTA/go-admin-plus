package product

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/account"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/session"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/config"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
)

// 验收真实 PostgreSQL、Redis、S3 组合；不以内存替身代替基础设施行为。
func TestFoundationStorageCacheMatrix(t *testing.T) {
	dsn, redisAddress, endpoint := os.Getenv("GO_ADMIN_TEST_POSTGRES_DISPOSABLE_DSN"), os.Getenv("GO_ADMIN_TEST_REDIS_ADDRESS"), os.Getenv("GO_ADMIN_TEST_S3_ENDPOINT")
	if dsn == "" || redisAddress == "" || endpoint == "" {
		t.Skip("matrix requires disposable PostgreSQL, Redis and S3 endpoints")
	}
	ctx := context.Background()
	hash, err := account.HashPassword("matrix administrator password")
	if err != nil {
		t.Fatal(err)
	}
	for _, driver := range []string{"sqlite", "postgres"} {
		for _, redisEnabled := range []bool{false, true} {
			for _, provider := range []string{"local", "s3"} {
				t.Run(fmt.Sprintf("%s/redis-%v/%s", driver, redisEnabled, provider), func(t *testing.T) {
					root := t.TempDir()
					dbConfig := database.Config{Profile: config.ProfileServerSQLite, SQLitePath: filepath.Join(root, "app.db")}
					if driver == "postgres" {
						admin, err := database.NewProcess().Open(ctx, database.Config{Profile: config.ProfileServerPostgres, PostgresDSN: dsn})
						if err != nil {
							t.Fatal(err)
						}
						name := "matrix_" + uuid.New().String()[:8]
						if _, err := admin.SQL().ExecContext(ctx, `CREATE DATABASE `+name); err != nil {
							t.Fatal(err)
						}
						t.Cleanup(func() { _, _ = admin.SQL().ExecContext(ctx, `DROP DATABASE `+name+` WITH (FORCE)`); _ = admin.Close() })
						parsed, err := url.Parse(dsn)
						if err != nil {
							t.Fatal(err)
						}
						parsed.Path = "/" + name
						dbConfig = database.Config{Profile: config.ProfileServerPostgres, PostgresDSN: parsed.String()}
					}
					db, err := database.NewProcess().Open(ctx, dbConfig)
					if err != nil {
						t.Fatal(err)
					}
					defer db.Close()
					runner, err := NewMigrationRunner()
					if err != nil {
						t.Fatal(err)
					}
					if _, err := runner.Up(ctx, db); err != nil {
						t.Fatal(err)
					}
					settings := config.DefaultRuntimeSettings()
					settings.Redis.Enabled = redisEnabled
					settings.Redis.Address = redisAddress
					settings.Redis.Namespace = "matrix-" + uuid.NewString()
					settings.Storage.DefaultProvider = provider
					settings.Storage.Local.Root = filepath.Join(root, "files")
					settings.Storage.MaxUploadBytes = 1024
					if provider == "s3" {
						settings.Storage.S3.Endpoint = endpoint
						settings.Storage.S3.Bucket = "matrix-" + uuid.NewString()
						settings.Storage.S3.PathStyle = true
						settings.Storage.S3.AccessKey = os.Getenv("GO_ADMIN_TEST_S3_ACCESS_KEY")
						settings.Storage.S3.SecretKey = os.Getenv("GO_ADMIN_TEST_S3_SECRET_KEY")
						cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion("us-east-1"), awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(settings.Storage.S3.AccessKey, settings.Storage.S3.SecretKey, "")))
						if err != nil {
							t.Fatal(err)
						}
						client := s3.NewFromConfig(cfg, func(o *s3.Options) { o.BaseEndpoint = aws.String(endpoint); o.UsePathStyle = true })
						if _, err := client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(settings.Storage.S3.Bucket)}); err != nil {
							t.Fatal(err)
						}
						t.Cleanup(func() {
							_, _ = client.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: aws.String(settings.Storage.S3.Bucket)})
						})
					}
					runtime, err := BuildPrepared(ctx, db, Options{Settings: &settings, SessionPolicy: config.DefaultSessionPolicy(), FilesRoot: settings.Storage.Local.Root, WorkerOwner: "matrix-worker", WorkerInterval: time.Second, AuditRetentionAge: 30 * 24 * time.Hour}, false)
					if err != nil {
						t.Fatal(err)
					}
					if err := runtime.Application.Start(ctx); err != nil {
						t.Fatal(err)
					}
					defer runtime.Application.Stop(ctx)
					if err := db.WithinTx(ctx, func(ctx context.Context, tx database.Tx) error {
						if err := account.NewRepository(db.Dialect()).Create(ctx, tx, account.Credential{Profile: account.Profile{ID: "matrix-admin-account", Username: "matrix-admin", DisplayName: "矩阵管理员", Email: "matrix@example.test"}, PasswordHash: hash}, time.Now().UTC()); err != nil {
							return err
						}
						_, err := tx.ExecContext(ctx, `INSERT INTO iam_account_roles(account_id,role_id) VALUES (?,?)`, "matrix-admin-account", "role-system-admin")
						return err
					}); err != nil {
						t.Fatal(err)
					}
					issued, err := runtime.Sessions.Login(ctx, "matrix-admin", "matrix administrator password")
					if err != nil {
						t.Fatal(err)
					}
					ifMatch := ""
					request := func(method, path, contentType string, body io.Reader) *httptest.ResponseRecorder {
						req := httptest.NewRequest(method, path, body)
						req.AddCookie(&http.Cookie{Name: session.CookieName, Value: issued.Token})
						req.Header.Set("X-CSRF-Token", issued.CSRF)
						req.Header.Set("If-Match", ifMatch)
						if contentType != "" {
							req.Header.Set("Content-Type", contentType)
						}
						rec := httptest.NewRecorder()
						runtime.Application.Handler().ServeHTTP(rec, req)
						return rec
					}
					for range 2 {
						if rec := request("GET", "/api/runtime/navigation", "", nil); rec.Code != 200 {
							t.Fatalf("navigation %d %s", rec.Code, rec.Body)
						}
					}
					profile := request("PATCH", "/api/iam/account/profile", "application/json", bytes.NewBufferString(`{"displayName":"新的管理员","email":"matrix@example.test"}`))
					if profile.Code != 200 {
						t.Fatalf("profile %d %s", profile.Code, profile.Body)
					}
					roleResponse := request("POST", "/api/iam/administration/roles", "application/json", bytes.NewBufferString(`{"key":"matrix-role","name":"矩阵角色","dataScope":"self"}`))
					var role struct {
						ID       string `json:"id"`
						Revision int64  `json:"revision"`
					}
					if roleResponse.Code != 201 || json.Unmarshal(roleResponse.Body.Bytes(), &role) != nil || role.Revision < 1 {
						t.Fatalf("role create: %d", roleResponse.Code)
					}
					ifMatch = fmt.Sprint(role.Revision)
					for _, expected := range []int{204, 409} {
						response := request("PATCH", "/api/iam/administration/roles/"+role.ID, "application/json", bytes.NewBufferString(`{"key":"matrix-role","name":"已更新","dataScope":"self","enabled":true}`))
						if response.Code != expected {
							t.Fatalf("optimistic update got %d want %d: %s", response.Code, expected, response.Body)
						}
					}
					ifMatch = ""
					body := new(bytes.Buffer)
					form := multipart.NewWriter(body)
					part, err := form.CreatePart(textproto.MIMEHeader{"Content-Disposition": {`form-data; name="file"; filename="matrix.txt"`}, "Content-Type": {"text/plain"}})
					if err != nil {
						t.Fatal(err)
					}
					_, _ = part.Write([]byte("matrix file content\n"))
					_ = form.Close()
					rec := request("POST", "/api/files/objects", form.FormDataContentType(), body)
					if rec.Code != 201 {
						t.Fatalf("upload %d %s", rec.Code, rec.Body)
					}
					var file struct {
						ID       string `json:"id"`
						Revision int    `json:"revision"`
					}
					if err := json.Unmarshal(rec.Body.Bytes(), &file); err != nil {
						t.Fatal(err)
					}
					var storedProvider string
					if err := db.WithinTx(ctx, func(ctx context.Context, tx database.Tx) error {
						return tx.QueryRowContext(ctx, `SELECT provider_id FROM files_objects WHERE id=?`, file.ID).Scan(&storedProvider)
					}); err != nil || storedProvider != provider {
						t.Fatalf("provider=%s %v", storedProvider, err)
					}
					rec = request("GET", "/api/files/objects/"+file.ID+"/content", "", nil)
					if rec.Code != 200 || rec.Body.String() != "matrix file content\n" {
						t.Fatalf("download %d %s", rec.Code, rec.Body)
					}
					rec = request("POST", "/api/files/objects/batch-delete", "application/json", bytes.NewBufferString(fmt.Sprintf(`{"files":[{"id":%q,"revision":%d}]}`, file.ID, file.Revision)))
					if rec.Code != 204 {
						t.Fatalf("delete %d %s", rec.Code, rec.Body)
					}
					if err := db.WithinTx(ctx, func(ctx context.Context, tx database.Tx) error {
						_, err := tx.ExecContext(ctx, `DELETE FROM iam_role_permissions WHERE role_id=? AND permission_code=?`, "role-system-admin", "iam.users.read")
						return err
					}); err != nil {
						t.Fatal(err)
					}
					rec = request("GET", "/api/iam/administration/users", "", nil)
					if rec.Code != 403 {
						t.Fatalf("cached permission bypassed DB: %d", rec.Code)
					}
					if response := request("GET", "/api/audit/records?page=1&pageSize=20", "", nil); response.Code != 200 {
						t.Fatalf("audit projection: %d %s", response.Code, response.Body)
					}
					var facts int
					if err := db.SQL().QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_facts WHERE event_id IS NOT NULL`).Scan(&facts); err != nil || facts < 3 {
						t.Fatalf("audit writes=%d %v", facts, err)
					}
				})
			}
		}
	}
}
