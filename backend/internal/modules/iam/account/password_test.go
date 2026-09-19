package account

import "testing"

func TestInitialCredentialMeetsMinimumPasswordPolicy(t *testing.T) {
	const initialPassword = "1234567890"
	hash, err := HashPassword(initialPassword)
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}
	if !VerifyPassword(hash, initialPassword) {
		t.Fatal("initial credential was not accepted by password verification")
	}
	if _, err := HashPassword("123456789"); err != ErrInvalidPassword {
		t.Fatalf("short password error = %v", err)
	}
}
