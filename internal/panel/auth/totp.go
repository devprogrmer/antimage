package auth

import (
	"crypto/rand"
	"encoding/base32"
	"fmt"
	"io"
	"net/url"
	"time"

	"github.com/pquerna/otp/totp"
)

const (
	totpSecretBytes = 20 // 160 bits, the RFC 4226 recommendation
	recoveryBytes   = 10
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// GenerateTOTPSecret returns an unpadded base32 secret. Padding breaks
// several popular authenticator apps, so it is stripped.
func GenerateTOTPSecret() (string, error) {
	raw := make([]byte, totpSecretBytes)
	if _, err := io.ReadFull(rand.Reader, raw); err != nil {
		return "", fmt.Errorf("generate TOTP secret: %w", err)
	}
	return b32.EncodeToString(raw), nil
}

// TOTPProvisioningURI builds an otpauth:// URI suitable for a QR code, so an
// authenticator app can be provisioned with secret, issuer, and account.
func TOTPProvisioningURI(secret, username, issuer string) string {
	label := url.PathEscape(issuer + ":" + username)
	q := url.Values{}
	q.Set("secret", secret)
	q.Set("issuer", issuer)
	q.Set("algorithm", "SHA1")
	q.Set("digits", "6")
	q.Set("period", "30")
	return "otpauth://totp/" + label + "?" + q.Encode()
}

// VerifyTOTP allows one step of skew in each direction (±30s), which covers
// ordinary clock drift without meaningfully widening the guess window. A
// malformed or wrong code is a failed attempt, not an error, so this
// returns a bare bool.
func VerifyTOTP(secret, code string, now time.Time) bool {
	ok, err := totp.ValidateCustom(code, secret, now, totp.ValidateOpts{
		Period: 30,
		Skew:   1,
		Digits: 6,
	})
	return err == nil && ok
}

// GenerateRecoveryCodes returns n single-use codes. Callers store only the
// argon2id hashes from HashRecoveryCode and show the plaintext once.
func GenerateRecoveryCodes(n int) ([]string, error) {
	codes := make([]string, 0, n)
	for i := 0; i < n; i++ {
		raw := make([]byte, recoveryBytes)
		if _, err := io.ReadFull(rand.Reader, raw); err != nil {
			return nil, fmt.Errorf("generate recovery code: %w", err)
		}
		codes = append(codes, b32.EncodeToString(raw))
	}
	return codes, nil
}

// HashRecoveryCode reuses the password hasher so recovery codes get the same
// argon2id resistance as passwords rather than a weaker hash.
func HashRecoveryCode(code string) (string, error) {
	return HashPassword(code)
}
