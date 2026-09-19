package files

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/config"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
)

func TestS3StorageStreamingReplayAndProviderSwitch(t *testing.T) {
	endpoint := os.Getenv("GO_ADMIN_TEST_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("GO_ADMIN_TEST_S3_ENDPOINT is not configured")
	}
	settings := config.DefaultRuntimeSettings().Storage
	settings.DefaultProvider = "s3"
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	settings.Local.Root = filepath.Join(root, "files")
	settings.S3.Endpoint, settings.S3.Bucket, settings.S3.PathStyle = endpoint, "gap-test-"+uuid.NewString(), true
	settings.S3.AccessKey = os.Getenv("GO_ADMIN_TEST_S3_ACCESS_KEY")
	settings.S3.SecretKey = os.Getenv("GO_ADMIN_TEST_S3_SECRET_KEY")
	ctx := context.Background()
	storage, err := NewConfiguredStorage(ctx, settings)
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	remote := storage.Provider("s3").(*S3Storage)
	if _, err := remote.client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(settings.S3.Bucket)}); err != nil {
		t.Fatal(err)
	}
	defer remote.client.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: aws.String(settings.S3.Bucket)})
	key := NewStorageKey()
	defer remote.Delete(ctx, key)
	body := "streamed file content\n"
	for range 2 {
		staged, err := storage.StageReserved(ctx, uuid.NewString(), "text/plain", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if err := storage.Publish(ctx, staged.TemporaryKey, key); err != nil {
			t.Fatal(err)
		}
	}
	settings.DefaultProvider = "local"
	next, err := NewConfiguredStorage(ctx, settings)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	content, err := storageProvider(next, "s3").Open(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(content)
	_ = content.Close()
	if err != nil || string(data) != body {
		t.Fatal("provider switch lost existing content")
	}
	if err := remote.Delete(ctx, key); err != nil {
		t.Fatal(err)
	}
	if err := remote.Delete(ctx, key); err != nil {
		t.Fatal("delete must be idempotent")
	}
	if exists, err := remote.ObjectExists(ctx, key); err != nil || exists {
		t.Fatal("deleted object still exists")
	}
}
