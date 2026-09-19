package files

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/google/uuid"
)

const DefaultMaximumContentBytes int64 = 10 * 1024 * 1024

var (
	ErrStorageRoot     = errors.New("files storage root invalid")
	ErrStorage         = errors.New("files storage unavailable")
	ErrStorageConflict = errors.New("files storage object conflict")
	ErrStorageNotFound = errors.New("files storage object not found")
	ErrContentTooLarge = errors.New("files content too large")
	ErrMediaType       = errors.New("files media type rejected")

	storageKeyPattern   = regexp.MustCompile(`^object-[a-f0-9]{32}$`)
	temporaryKeyPattern = regexp.MustCompile(`^stage-[a-f0-9]{32}$`)
	allowedMediaTypes   = map[string]struct{}{
		"application/pdf": {},
		"image/jpeg":      {},
		"image/png":       {},
		"text/plain":      {},
	}
)

type StorageOption func(*LocalStorage)

func WithMaximumContentBytes(maximum int64) StorageOption {
	return func(storage *LocalStorage) { storage.maximumContentBytes = maximum }
}

type StagedContent struct {
	TemporaryKey string
	MediaType    string
	SizeBytes    int64
	SHA256       string
}

type LocalStorage struct {
	root                *os.Root
	rootPath            string
	maximumContentBytes int64
	mediaTypes          map[string]struct{}
}

// NewLocalStorage opens a dedicated private root. The caller owns Close and must pass an absolute
// path; the constructor resolves ancestors once and retains an anchored directory handle.
func NewLocalStorage(rootPath string, options ...StorageOption) (*LocalStorage, error) {
	rootPath = filepath.Clean(strings.TrimSpace(rootPath))
	if rootPath == "." || !filepath.IsAbs(rootPath) {
		return nil, ErrStorageRoot
	}
	parentPath, leaf := filepath.Dir(rootPath), filepath.Base(rootPath)
	canonicalParent, err := filepath.EvalSymlinks(parentPath)
	if err != nil || canonicalParent != parentPath || leaf == "." || leaf == string(filepath.Separator) {
		return nil, ErrStorageRoot
	}
	parentInfo, err := os.Lstat(parentPath)
	if err != nil || !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 {
		return nil, ErrStorageRoot
	}
	parent, err := os.OpenRoot(parentPath)
	if err != nil {
		return nil, ErrStorageRoot
	}
	defer parent.Close()
	info, err := parent.Lstat(leaf)
	if errors.Is(err, os.ErrNotExist) {
		if err := parent.Mkdir(leaf, 0o700); err != nil {
			return nil, ErrStorageRoot
		}
		info, err = parent.Lstat(leaf)
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrStorageRoot
	}
	root, err := parent.OpenRoot(leaf)
	if err != nil {
		return nil, ErrStorageRoot
	}
	openedInfo, err := root.Stat(".")
	if err != nil || !os.SameFile(info, openedInfo) {
		_ = root.Close()
		return nil, ErrStorageRoot
	}
	if err := root.Chmod(".", 0o700); err != nil {
		_ = root.Close()
		return nil, ErrStorageRoot
	}
	storage := &LocalStorage{root: root, rootPath: rootPath, maximumContentBytes: DefaultMaximumContentBytes, mediaTypes: allowedMediaTypes}
	for _, option := range options {
		if option != nil {
			option(storage)
		}
	}
	if storage.maximumContentBytes < 1 {
		_ = root.Close()
		return nil, ErrStorageRoot
	}
	return storage, nil
}

// CapacityProbe reports the free space of this storage root. Keeping the probe
// bound to the opened root prevents quota checks from observing another mount.
func (s *LocalStorage) CapacityProbe() CapacityProbe {
	if s == nil || s.rootPath == "" {
		return unavailableCapacityProbe{}
	}
	return NewDiskCapacityProbe(s.rootPath)
}

func NewStorageKey() string { return "object-" + strings.ReplaceAll(uuid.NewString(), "-", "") }

// Stage streams one bounded body into a private temporary file and durably records its bytes before
// returning. Any error removes the temporary object.
func (s *LocalStorage) Stage(ctx context.Context, declaredMediaType string, source io.Reader) (StagedContent, error) {
	return s.stage(ctx, "stage-"+strings.ReplaceAll(uuid.NewString(), "-", ""), declaredMediaType, source)
}

// StageReserved 使用已登记的预约 ID，崩溃后可由数据库找回临时文件。
func (s *LocalStorage) StageReserved(ctx context.Context, reservationID, declaredMediaType string, source io.Reader) (StagedContent, error) {
	if uuid.Validate(reservationID) != nil {
		return StagedContent{}, ErrValidation
	}
	return s.stage(ctx, reservationStage(reservationID), declaredMediaType, source)
}

func (s *LocalStorage) stage(ctx context.Context, temporaryKey, declaredMediaType string, source io.Reader) (StagedContent, error) {
	if err := validContext(ctx); err != nil {
		return StagedContent{}, err
	}
	if s == nil || s.root == nil || source == nil {
		return StagedContent{}, ErrStorage
	}
	declaredMediaType = normalizedMediaType(declaredMediaType)
	if _, allowed := s.mediaTypes[declaredMediaType]; !allowed {
		return StagedContent{}, ErrMediaType
	}
	file, err := s.root.OpenFile(temporaryKey, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return StagedContent{}, ErrStorage
	}
	keep := false
	defer func() {
		_ = file.Close()
		if !keep {
			_ = s.root.Remove(temporaryKey)
		}
	}()

	hash := sha256.New()
	limited := io.LimitReader(&contextReader{ctx: ctx, source: source}, s.maximumContentBytes+1)
	buffer := make([]byte, min(int64(512), s.maximumContentBytes+1))
	n, readErr := io.ReadFull(limited, buffer)
	if readErr != nil && !errors.Is(readErr, io.EOF) && !errors.Is(readErr, io.ErrUnexpectedEOF) {
		return StagedContent{}, stableStorageError(ctx, readErr)
	}
	buffer = buffer[:n]
	if int64(n) > s.maximumContentBytes {
		return StagedContent{}, ErrContentTooLarge
	}
	detected := normalizedMediaType(http.DetectContentType(buffer))
	if detected != declaredMediaType {
		return StagedContent{}, ErrMediaType
	}
	written, err := io.Copy(io.MultiWriter(file, hash), io.MultiReader(bytes.NewReader(buffer), limited))
	if err != nil {
		return StagedContent{}, stableStorageError(ctx, err)
	}
	if written > s.maximumContentBytes {
		return StagedContent{}, ErrContentTooLarge
	}
	if err := ctx.Err(); err != nil {
		return StagedContent{}, err
	}
	if err := file.Sync(); err != nil {
		return StagedContent{}, ErrStorage
	}
	if err := file.Close(); err != nil {
		return StagedContent{}, ErrStorage
	}
	keep = true
	return StagedContent{TemporaryKey: temporaryKey, MediaType: detected, SizeBytes: written, SHA256: hex.EncodeToString(hash.Sum(nil))}, nil
}

// Publish atomically links staged bytes into an immutable object key. It never replaces an existing
// file and leaves the stage intact on failure so callers can abort or retry safely.
func (s *LocalStorage) Publish(ctx context.Context, temporaryKey, storageKey string) error {
	if err := validContext(ctx); err != nil {
		return err
	}
	if s == nil || s.root == nil || !temporaryKeyPattern.MatchString(temporaryKey) || !storageKeyPattern.MatchString(storageKey) {
		return ErrStorage
	}
	if err := s.root.Link(temporaryKey, storageKey); err != nil {
		if _, statErr := s.root.Lstat(storageKey); statErr == nil {
			return ErrStorageConflict
		}
		return ErrStorage
	}
	if err := s.syncRoot(); err != nil {
		_ = s.root.Remove(storageKey)
		return ErrStorage
	}
	if err := s.root.Remove(temporaryKey); err != nil {
		return ErrStorage
	}
	return s.syncRoot()
}

func (s *LocalStorage) Abort(ctx context.Context, temporaryKey string) error {
	if err := validContext(ctx); err != nil {
		return err
	}
	if s == nil || s.root == nil || !temporaryKeyPattern.MatchString(temporaryKey) {
		return ErrStorage
	}
	if err := s.root.Remove(temporaryKey); err != nil && !errors.Is(err, os.ErrNotExist) {
		return ErrStorage
	}
	return nil
}

func (s *LocalStorage) Open(ctx context.Context, storageKey string) (io.ReadCloser, error) {
	if err := validContext(ctx); err != nil {
		return nil, err
	}
	if s == nil || s.root == nil || !storageKeyPattern.MatchString(storageKey) {
		return nil, ErrStorageNotFound
	}
	info, err := s.root.Lstat(storageKey)
	if errors.Is(err, os.ErrNotExist) || err == nil && !info.Mode().IsRegular() {
		return nil, ErrStorageNotFound
	}
	if err != nil {
		return nil, ErrStorage
	}
	file, err := s.root.Open(storageKey)
	if err != nil {
		// A leaf may have been replaced between Lstat and Open. os.Root refuses escape; expose the
		// same not-found result as any other non-regular object without leaking host details.
		return nil, ErrStorageNotFound
	}
	info, err = file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, ErrStorageNotFound
	}
	return file, nil
}

func (s *LocalStorage) Delete(ctx context.Context, storageKey string) error {
	if err := validContext(ctx); err != nil {
		return err
	}
	if s == nil || s.root == nil || !storageKeyPattern.MatchString(storageKey) {
		return ErrStorage
	}
	if err := s.root.Remove(storageKey); err != nil && !errors.Is(err, os.ErrNotExist) {
		return ErrStorage
	}
	return s.syncRoot()
}

func (s *LocalStorage) ObjectExists(ctx context.Context, storageKey string) (bool, error) {
	return s.exists(ctx, storageKey, storageKeyPattern)
}

func (s *LocalStorage) TemporaryExists(ctx context.Context, temporaryKey string) (bool, error) {
	return s.exists(ctx, temporaryKey, temporaryKeyPattern)
}

func (s *LocalStorage) exists(ctx context.Context, key string, pattern *regexp.Regexp) (bool, error) {
	if err := validContext(ctx); err != nil {
		return false, err
	}
	if s == nil || s.root == nil || !pattern.MatchString(key) {
		return false, ErrStorage
	}
	info, err := s.root.Lstat(key)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, ErrStorage
	}
	return info.Mode().IsRegular(), nil
}

func (s *LocalStorage) Close() error {
	if s == nil || s.root == nil {
		return nil
	}
	err := s.root.Close()
	s.root = nil
	return err
}

func (s *LocalStorage) syncRoot() error {
	if runtime.GOOS == "windows" {
		// Windows does not expose portable directory fsync through os.File. The immutable link and
		// file Sync still provide the durability boundary available to this process.
		return nil
	}
	directory, err := s.root.Open(".")
	if err != nil {
		return ErrStorage
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return ErrStorage
	}
	return nil
}

func normalizedMediaType(value string) string {
	mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(value))
	if err != nil {
		return ""
	}
	return strings.ToLower(mediaType)
}

func stableStorageError(ctx context.Context, err error) error {
	if ctx != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return contextErr
		}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var bodyTooLarge *http.MaxBytesError
	if errors.As(err, &bodyTooLarge) {
		return ErrContentTooLarge
	}
	if errors.Is(err, ErrValidation) || errors.Is(err, ErrContentTooLarge) || errors.Is(err, ErrMediaType) {
		return err
	}
	return ErrStorage
}

func validContext(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("%w: context required", ErrStorage)
	}
	return ctx.Err()
}

type contextReader struct {
	ctx    context.Context
	source io.Reader
}

func (reader *contextReader) Read(buffer []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	return reader.source.Read(buffer)
}

// WithAllowedMediaTypes 复制配置白名单，避免调用方修改正在运行的存储策略。
func WithAllowedMediaTypes(values []string) StorageOption {
	return func(s *LocalStorage) {
		s.mediaTypes = make(map[string]struct{}, len(values))
		for _, v := range values {
			s.mediaTypes[normalizedMediaType(v)] = struct{}{}
		}
	}
}

func sameContent(a, b io.Reader) bool {
	ah, bh := sha256.New(), sha256.New()
	an, ae := io.Copy(ah, a)
	bn, be := io.Copy(bh, b)
	return ae == nil && be == nil && an == bn && bytes.Equal(ah.Sum(nil), bh.Sum(nil))
}
