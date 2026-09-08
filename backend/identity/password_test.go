package identity

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHashPasswordPHCFormat(t *testing.T) {
	encoded, err := HashPassword("acceptable-pass")
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(encoded, "$argon2id$v=19$m=65536,t=3,p=4$"), "PHC header with default parameters: %s", encoded)
	fields := strings.Split(strings.TrimPrefix(encoded, "$"), "$")
	require.Len(t, fields, 5)
	salt, err := phcAlphabet.DecodeString(fields[3])
	require.NoError(t, err)
	require.Len(t, salt, argon2SaltLength)
	key, err := phcAlphabet.DecodeString(fields[4])
	require.NoError(t, err)
	require.Len(t, key, argon2KeyLength)
}

func TestPasswordRoundTrip(t *testing.T) {
	encoded, err := HashPassword("correct horse battery")
	require.NoError(t, err)

	ok, err := VerifyPassword("correct horse battery", encoded)
	require.NoError(t, err)
	require.True(t, ok)

	ok, err = VerifyPassword("wrong horse battery", encoded)
	require.NoError(t, err, "well-formed hash mismatch is not an error")
	require.False(t, ok)

	// Unicode and boundary-length passwords round-trip too.
	unicode, err := HashPassword("пароль-пароль")
	require.NoError(t, err)
	ok, err = VerifyPassword("пароль-пароль", unicode)
	require.NoError(t, err)
	require.True(t, ok)
}

func TestHashPasswordUsesUniqueSalts(t *testing.T) {
	first, err := HashPassword("same-password")
	require.NoError(t, err)
	second, err := HashPassword("same-password")
	require.NoError(t, err)
	require.NotEqual(t, first, second, "per-user random salt")
	ok, err := VerifyPassword("same-password", second)
	require.NoError(t, err)
	require.True(t, ok)
}

func TestPasswordPolicy(t *testing.T) {
	tooShort, err := HashPassword(strings.Repeat("a", MinPasswordLength-1))
	require.Equal(t, 422, appErr(t, err).Status)
	require.Empty(t, tooShort)

	tooLong, err := HashPassword(strings.Repeat("a", MaxPasswordLength+1))
	require.Equal(t, 422, appErr(t, err).Status)
	require.Empty(t, tooLong)

	atMin, err := HashPassword(strings.Repeat("a", MinPasswordLength))
	require.NoError(t, err)
	require.NotEmpty(t, atMin)

	atMax, err := HashPassword(strings.Repeat("b", MaxPasswordLength))
	require.NoError(t, err)
	ok, err := VerifyPassword(strings.Repeat("b", MaxPasswordLength), atMax)
	require.NoError(t, err)
	require.True(t, ok)
}

func TestVerifyPasswordRejectsMalformedHashes(t *testing.T) {
	valid, err := HashPassword("acceptable-pass")
	require.NoError(t, err)
	fields := strings.Split(strings.TrimPrefix(valid, "$"), "$")

	cases := []struct {
		name    string
		encoded string
	}{
		{"empty", ""},
		{"not argon2", "$bcrypt$v=19$m=65536,t=3,p=4$c2FsdA$aGFzaA"},
		{"argon2i variant", "$argon2i$v=19$m=65536,t=3,p=4$c2FsdA$aGFzaA"},
		{"truncated", "$argon2id$v=19$m=65536,t=3,p=4"},
		{"missing params", "$argon2id$v=19$c2FsdA$aGFzaA"},
		{"bad version", "$argon2id$v=16$m=65536,t=3,p=4$c2FsdA$aGFzaA"},
		{"non-numeric version", "$argon2id$v=x$m=65536,t=3,p=4$c2FsdA$aGFzaA"},
		{"zero iterations", "$argon2id$v=19$m=65536,t=0,p=4$c2FsdA$aGFzaA"},
		{"bad salt encoding", "$argon2id$v=19$m=65536,t=3,p=4$!!!$aGFzaA"},
		{"bad hash encoding", "$argon2id$v=19$m=65536,t=3,p=4$c2FsdA$!!!"},
		{"empty salt", "$argon2id$v=19$m=65536,t=3,p=4$$aGFzaA"},
		{"empty key", "$argon2id$v=19$m=65536,t=3,p=4$c2FsdA$"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ok, err := VerifyPassword("acceptable-pass", tc.encoded)
			require.Error(t, err)
			require.False(t, ok)
		})
	}

	// A well-formed hash with a tampered key compares false without error.
	tampered := "$argon2id$v=19$m=65536,t=3,p=4$" + fields[3] + "$" + strings.Repeat("A", len(fields[4]))
	ok, err := VerifyPassword("acceptable-pass", tampered)
	require.NoError(t, err)
	require.False(t, ok)
}
