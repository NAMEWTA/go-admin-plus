package files

import (
	"context"
	"errors"
	"io"
	"net/url"
	"strings"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/config"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
)

// S3Storage 使用私有桶保存对象；本地暂存只负责有界流式校验与失败恢复。
// Publish 不依赖 POSIX hard-link，已提交对象可以由任意服务实例读取。
type S3Storage struct {
	stage  *LocalStorage
	client *s3.Client
	bucket string
}

func newS3Storage(ctx context.Context, settings config.StorageSettings, stage *LocalStorage) (*S3Storage, error) {
	options := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(settings.S3.Region)}
	if settings.S3.AccessKey != "" || settings.S3.SecretKey != "" {
		if settings.S3.AccessKey == "" || settings.S3.SecretKey == "" {
			return nil, ErrStorage
		}
		options = append(options, awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(settings.S3.AccessKey, settings.S3.SecretKey, "")))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, options...)
	if err != nil {
		return nil, ErrStorage
	}
	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.UsePathStyle = settings.S3.PathStyle
		if settings.S3.Endpoint != "" {
			o.BaseEndpoint = aws.String(settings.S3.Endpoint)
		}
	})
	return &S3Storage{stage: stage, client: client, bucket: settings.S3.Bucket}, nil
}
func (s *S3Storage) Stage(ctx context.Context, media string, body io.Reader) (StagedContent, error) {
	return s.stage.Stage(ctx, media, body)
}
func (s *S3Storage) StageReserved(ctx context.Context, key, media string, body io.Reader) (StagedContent, error) {
	return s.stage.StageReserved(ctx, key, media, body)
}
func (s *S3Storage) Abort(ctx context.Context, key string) error { return s.stage.Abort(ctx, key) }
func (s *S3Storage) TemporaryExists(ctx context.Context, key string) (bool, error) {
	return s.stage.TemporaryExists(ctx, key)
}
func (s *S3Storage) CapacityProbe() CapacityProbe { return s.stage.CapacityProbe() }
func (s *S3Storage) Publish(ctx context.Context, temporary, key string) error {
	if !temporaryKeyPattern.MatchString(temporary) || !storageKeyPattern.MatchString(key) {
		return ErrStorage
	}
	f, err := s.stage.root.Open(temporary)
	if err != nil {
		return ErrStorageNotFound
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return ErrStorage
	}
	_, err = s.client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key), Body: f, ContentLength: aws.Int64(stat.Size()), IfNoneMatch: aws.String("*")})
	if err != nil {
		var api smithy.APIError
		if !errors.As(err, &api) || api.ErrorCode() != "PreconditionFailed" {
			return stableStorageError(ctx, err)
		}
		// 对象键由数据库唯一分配；重放只接受同一暂存内容对应的不可变对象。
		remote, openErr := s.Open(ctx, key)
		if openErr != nil {
			return openErr
		}
		defer remote.Close()
		if _, err = f.Seek(0, io.SeekStart); err != nil {
			return ErrStorage
		}
		if !sameContent(remote, f) {
			return ErrStorageConflict
		}
	}
	return s.stage.Abort(ctx, temporary)
}
func (s *S3Storage) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	if !storageKeyPattern.MatchString(key) {
		return nil, ErrStorage
	}
	r, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if s3Missing(err) {
		return nil, ErrStorageNotFound
	}
	if err != nil {
		return nil, stableStorageError(ctx, err)
	}
	return r.Body, nil
}
func (s *S3Storage) Delete(ctx context.Context, key string) error {
	if !storageKeyPattern.MatchString(key) {
		return ErrStorage
	}
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		return stableStorageError(ctx, err)
	}
	return nil
}
func (s *S3Storage) ObjectExists(ctx context.Context, key string) (bool, error) {
	if !storageKeyPattern.MatchString(key) {
		return false, ErrStorage
	}
	_, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if s3Missing(err) {
		return false, nil
	}
	if err != nil {
		return false, stableStorageError(ctx, err)
	}
	return true, nil
}
func s3Missing(err error) bool {
	var api smithy.APIError
	return errors.As(err, &api) && (api.ErrorCode() == "NoSuchKey" || api.ErrorCode() == "NotFound")
}

// ProviderStorage 保持对象所属 provider 稳定；默认 provider 只决定新上传的位置。
type ProviderStorage struct {
	Storage
	active    string
	providers map[string]Storage
	local     *LocalStorage
}

func NewConfiguredStorage(ctx context.Context, settings config.StorageSettings) (*ProviderStorage, error) {
	if settings.S3.Endpoint != "" {
		u, err := url.Parse(settings.S3.Endpoint)
		if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil {
			return nil, ErrStorage
		}
	}
	local, err := NewLocalStorage(settings.Local.Root, WithMaximumContentBytes(settings.MaxUploadBytes), WithAllowedMediaTypes(settings.AllowedTypes))
	if err != nil {
		return nil, err
	}
	result := &ProviderStorage{active: settings.DefaultProvider, providers: map[string]Storage{"local": local}, local: local}
	if settings.S3.Bucket != "" {
		remote, err := newS3Storage(ctx, settings, local)
		if err != nil {
			_ = local.Close()
			return nil, err
		}
		result.providers["s3"] = remote
	}
	result.Storage = result.providers[result.active]
	if result.Storage == nil {
		_ = local.Close()
		return nil, ErrStorage
	}
	return result, nil
}
func (s *ProviderStorage) DefaultProvider() string { return s.active }
func (s *ProviderStorage) Provider(id string) Storage {
	if id == "" {
		id = "local"
	}
	if p := s.providers[id]; p != nil {
		return p
	}
	return unavailableStorage{}
}
func (s *ProviderStorage) Close() error                 { return s.local.Close() }
func (s *ProviderStorage) CapacityProbe() CapacityProbe { return s.local.CapacityProbe() }
func (s *ProviderStorage) StageReserved(ctx context.Context, key, media string, body io.Reader) (StagedContent, error) {
	return s.local.StageReserved(ctx, key, media, body)
}
func storageProvider(storage Storage, id string) Storage {
	if p, ok := storage.(interface{ Provider(string) Storage }); ok {
		return p.Provider(id)
	}
	return storage
}
func defaultProvider(storage Storage) string {
	if p, ok := storage.(interface{ DefaultProvider() string }); ok {
		return p.DefaultProvider()
	}
	return "local"
}
func reservationStage(id string) string { return "stage-" + strings.ReplaceAll(id, "-", "") }

type unavailableStorage struct{}

func (unavailableStorage) Stage(context.Context, string, io.Reader) (StagedContent, error) {
	return StagedContent{}, ErrStorage
}
func (unavailableStorage) Publish(context.Context, string, string) error { return ErrStorage }
func (unavailableStorage) Abort(context.Context, string) error           { return ErrStorage }
func (unavailableStorage) Open(context.Context, string) (io.ReadCloser, error) {
	return nil, ErrStorage
}
func (unavailableStorage) Delete(context.Context, string) error { return ErrStorage }
func (unavailableStorage) ObjectExists(context.Context, string) (bool, error) {
	return false, ErrStorage
}
func (unavailableStorage) TemporaryExists(context.Context, string) (bool, error) {
	return false, ErrStorage
}
