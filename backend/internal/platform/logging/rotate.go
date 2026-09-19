package logging

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type rotatingWriter struct {
	mu           sync.Mutex
	root         *os.Root
	name         string
	maximumBytes int64
	backups      int
	file         *os.File
	size         int64
}

func newRotatingWriter(sink Sink) (*rotatingWriter, error) {
	directory := filepath.Clean(strings.TrimSpace(sink.Directory))
	filename := strings.TrimSpace(sink.Filename)
	if !filepath.IsAbs(directory) || filename == "" || filepath.Base(filename) != filename || sink.MaximumBytes < 128 || sink.Backups < 1 || sink.Backups > 100 {
		return nil, errors.New("logging rotating sink is invalid")
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, errors.New("logging directory is unavailable")
	}
	canonical, err := filepath.EvalSymlinks(directory)
	if err != nil || canonical != directory {
		return nil, errors.New("logging directory is invalid")
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return nil, errors.New("logging directory permissions are unavailable")
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, errors.New("logging directory is unavailable")
	}
	if info, statErr := root.Lstat(filename); statErr == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
		_ = root.Close()
		return nil, errors.New("logging file is invalid")
	} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		_ = root.Close()
		return nil, errors.New("logging file is unavailable")
	}
	file, err := root.OpenFile(filename, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		_ = root.Close()
		return nil, errors.New("logging file is unavailable")
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		_ = root.Close()
		return nil, errors.New("logging file is invalid")
	}
	if err := root.Chmod(filename, 0o600); err != nil {
		_ = file.Close()
		_ = root.Close()
		return nil, errors.New("logging file permissions are unavailable")
	}
	return &rotatingWriter{root: root, name: filename, maximumBytes: sink.MaximumBytes, backups: sink.Backups, file: file, size: info.Size()}, nil
}

func (writer *rotatingWriter) Write(value []byte) (int, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if writer.file == nil {
		return 0, errors.New("logging file is closed")
	}
	if writer.size > 0 && writer.size+int64(len(value)) > writer.maximumBytes {
		if err := writer.rotate(); err != nil {
			return 0, err
		}
	}
	written, err := writer.file.Write(value)
	writer.size += int64(written)
	return written, err
}

func (writer *rotatingWriter) rotate() error {
	if err := writer.file.Sync(); err != nil {
		return errors.New("logging file sync failed")
	}
	if err := writer.file.Close(); err != nil {
		return errors.New("logging file close failed")
	}
	writer.file = nil
	_ = writer.root.Remove(writer.name + "." + integerString(writer.backups))
	for index := writer.backups - 1; index >= 1; index-- {
		from := writer.name + "." + integerString(index)
		to := writer.name + "." + integerString(index+1)
		if err := writer.root.Rename(from, to); err != nil && !errors.Is(err, os.ErrNotExist) {
			return errors.New("logging rotation failed")
		}
	}
	if err := writer.root.Rename(writer.name, writer.name+".1"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.New("logging rotation failed")
	}
	file, err := writer.root.OpenFile(writer.name, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return errors.New("logging file is unavailable")
	}
	if err := writer.root.Chmod(writer.name, 0o600); err != nil {
		_ = file.Close()
		return errors.New("logging file permissions are unavailable")
	}
	writer.file = file
	writer.size = 0
	return nil
}

func (writer *rotatingWriter) Close() error {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if writer.file == nil {
		if writer.root != nil {
			err := writer.root.Close()
			writer.root = nil
			return err
		}
		return nil
	}
	err := writer.file.Sync()
	closeErr := writer.file.Close()
	writer.file = nil
	rootErr := writer.root.Close()
	writer.root = nil
	if err != nil || closeErr != nil || rootErr != nil {
		return errors.New("logging file close failed")
	}
	return nil
}

func integerString(value int) string {
	if value == 0 {
		return "0"
	}
	buffer := [20]byte{}
	index := len(buffer)
	for value > 0 {
		index--
		buffer[index] = byte('0' + value%10)
		value /= 10
	}
	return string(buffer[index:])
}
