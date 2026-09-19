package account

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

const (
	// MinimumPasswordLength is the shared lower bound for all IAM passwords.
	MinimumPasswordLength = 10
	argonMemory           = 64 * 1024
	argonTime             = 3
	argonThreads          = 4
	argonSaltLen          = 16
	argonKeyLen           = 32
)

var ErrInvalidPassword = errors.New("password does not satisfy policy")

// HashPassword applies the single accepted greenfield password format.
func HashPassword(password string) (string, error) {
	if len(password) < MinimumPasswordLength || len(password) > 128 {
		return "", ErrInvalidPassword
	}
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", errors.New("password hashing unavailable")
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

// VerifyPassword rejects every legacy or parameter-substituted encoding.
func VerifyPassword(encoded, password string) bool {
	if len(password) < MinimumPasswordLength || len(password) > 128 {
		return false
	}
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != "v=19" || parts[3] != "m=65536,t=3,p=4" {
		return false
	}
	salt, err := base64.RawStdEncoding.Strict().DecodeString(parts[4])
	if err != nil || len(salt) != argonSaltLen {
		return false
	}
	want, err := base64.RawStdEncoding.Strict().DecodeString(parts[5])
	if err != nil || len(want) != argonKeyLen {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return subtle.ConstantTimeCompare(got, want) == 1
}
