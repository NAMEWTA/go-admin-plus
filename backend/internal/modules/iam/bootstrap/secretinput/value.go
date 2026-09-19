// Package secretinput owns bounded password ingestion and redacted rendering for trusted IAM hosts.
package secretinput

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/account"
)

var ErrInvalid = errors.New("secret input is invalid")

type Value struct{ value []byte }

// Read accepts one password from a trusted TTY or protected-file adapter. It strips only the line
// terminator, enforces the account password size limit, and never exposes the bytes when rendered.
func Read(reader io.Reader) (Value, error) {
	if reader == nil {
		return Value{}, ErrInvalid
	}
	payload, err := io.ReadAll(io.LimitReader(bufio.NewReader(reader), 130))
	if err != nil || len(payload) > 129 {
		return Value{}, ErrInvalid
	}
	payload = []byte(strings.TrimSuffix(strings.TrimSuffix(string(payload), "\n"), "\r"))
	if len(payload) < account.MinimumPasswordLength || len(payload) > 128 || strings.IndexByte(string(payload), 0) >= 0 {
		return Value{}, ErrInvalid
	}
	return Value{value: append([]byte(nil), payload...)}, nil
}

func (Value) String() string   { return "iam.Secret{[redacted]}" }
func (Value) GoString() string { return "iam.Secret{[redacted]}" }
func (Value) MarshalJSON() ([]byte, error) {
	return json.Marshal("[redacted]")
}

func (value Value) Valid() bool {
	return len(value.value) >= account.MinimumPasswordLength && len(value.value) <= 128
}

// PasswordHash performs the only permitted conversion of secret bytes into persistent material.
func (value Value) PasswordHash() (string, error) {
	if !value.Valid() {
		return "", ErrInvalid
	}
	return account.HashPassword(string(value.value))
}
