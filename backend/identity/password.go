package identity

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"

	"github.com/Roy-Wanyoike/Motivra/backend/platform"
)

// Password policy and argon2id parameters (ADR-0004: memory 64 MB,
// iterations within 1-3, per-user random salt). MinPasswordLength is the
// length-first policy floor; ADR-0004 targets a floor of 10 once existing
// signup flows are migrated — raise the constant, nothing else changes.
const (
	MinPasswordLength = 8
	MaxPasswordLength = 128

	argon2MemoryKiB  = 64 * 1024
	argon2Time       = 3
	argon2Threads    = 4
	argon2KeyLength  = 32
	argon2SaltLength = 16
)

// phcAlphabet is the unpadded base64 variant the PHC string format uses.
var phcAlphabet = base64.RawStdEncoding

// HashPassword derives an argon2id PHC-encoded hash of password with a fresh
// 16-byte random salt and returns it in the form
// $argon2id$v=19$m=65536,t=3,p=4$<salt-b64>$<hash-b64>.
// Passwords shorter than MinPasswordLength or longer than MaxPasswordLength
// are rejected with a validation error.
func HashPassword(password string) (string, error) {
	if len(password) < MinPasswordLength {
		return "", platform.ErrValidation(
			fmt.Sprintf("password must be at least %d characters", MinPasswordLength),
			platform.FieldError{Field: "password", Issue: "too_short"},
		)
	}
	if len(password) > MaxPasswordLength {
		return "", platform.ErrValidation(
			fmt.Sprintf("password must be at most %d characters", MaxPasswordLength),
			platform.FieldError{Field: "password", Issue: "too_long"},
		)
	}
	salt := make([]byte, argon2SaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate password salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, argon2Time, argon2MemoryKiB, argon2Threads, argon2KeyLength)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argon2MemoryKiB, argon2Time, argon2Threads,
		phcAlphabet.EncodeToString(salt), phcAlphabet.EncodeToString(key),
	), nil
}

// VerifyPassword reports whether password matches the PHC-encoded argon2id
// hash. Unknown or malformed hash formats return an error; a well-formed
// hash that does not match returns (false, nil). The comparison is
// constant-time.
func VerifyPassword(password, encoded string) (bool, error) {
	if !strings.HasPrefix(encoded, "$argon2id$") {
		return false, fmt.Errorf("unknown hash format")
	}
	parts := strings.Split(strings.TrimPrefix(encoded, "$"), "$")
	if len(parts) != 5 {
		return false, fmt.Errorf("malformed hash: expected 5 PHC fields, got %d", len(parts))
	}
	if parts[0] != "argon2id" {
		return false, fmt.Errorf("unknown hash format: %q", parts[0])
	}
	version, err := parsePHCField(parts[1], "v")
	if err != nil {
		return false, err
	}
	if version != argon2.Version {
		return false, fmt.Errorf("unsupported argon2 version %d", version)
	}
	params := strings.Split(parts[2], ",")
	if len(params) != 3 {
		return false, fmt.Errorf("malformed hash: expected m,t,p parameters, got %q", parts[2])
	}
	memory, err := parsePHCField(params[0], "m")
	if err != nil {
		return false, err
	}
	timeParam, err := parsePHCField(params[1], "t")
	if err != nil {
		return false, err
	}
	threads, err := parsePHCField(params[2], "p")
	if err != nil {
		return false, err
	}
	if memory <= 0 || timeParam <= 0 || threads <= 0 {
		return false, fmt.Errorf("malformed hash: non-positive argon2 parameters")
	}
	salt, err := phcAlphabet.DecodeString(parts[3])
	if err != nil {
		return false, fmt.Errorf("malformed hash: bad salt encoding: %w", err)
	}
	if len(salt) == 0 {
		return false, fmt.Errorf("malformed hash: empty salt")
	}
	want, err := phcAlphabet.DecodeString(parts[4])
	if err != nil {
		return false, fmt.Errorf("malformed hash: bad hash encoding: %w", err)
	}
	if len(want) == 0 || len(want) > MaxPasswordLength {
		return false, fmt.Errorf("malformed hash: invalid key length %d", len(want))
	}
	got := argon2.IDKey([]byte(password), salt, uint32(timeParam), uint32(memory), uint8(threads), uint32(len(want)))
	if subtle.ConstantTimeCompare(got, want) == 1 {
		return true, nil
	}
	return false, nil
}

// parsePHCField parses a "key=value" PHC parameter into an int.
func parsePHCField(field, key string) (int, error) {
	if !strings.HasPrefix(field, key+"=") {
		return 0, fmt.Errorf("malformed hash: expected %q parameter, got %q", key, field)
	}
	value, err := strconv.Atoi(strings.TrimPrefix(field, key+"="))
	if err != nil {
		return 0, fmt.Errorf("malformed hash: bad %s value: %w", key, err)
	}
	return value, nil
}
