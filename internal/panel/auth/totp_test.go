package auth

import (
	"strings"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"
)

func TestGenerateSecretIsUsableBase32(t *testing.T) {
	secret, err := GenerateTOTPSecret()
	if err != nil {
		t.Fatalf("GenerateTOTPSecret: %v", err)
	}
	if len(secret) < 16 {
		t.Errorf("secret %q is shorter than 16 chars", secret)
	}
	if strings.Contains(secret, "=") {
		t.Error("secret contains base32 padding; authenticator apps reject it")
	}
	if _, err := totp.GenerateCode(secret, time.Now()); err != nil {
		t.Errorf("secret is not valid base32 for TOTP: %v", err)
	}
}

func TestVerifyAcceptsCurrentCode(t *testing.T) {
	secret, _ := GenerateTOTPSecret()
	now := time.Unix(1_700_000_000, 0).UTC()
	code, err := totp.GenerateCode(secret, now)
	if err != nil {
		t.Fatalf("GenerateCode: %v", err)
	}
	if !VerifyTOTP(secret, code, now) {
		t.Error("current code rejected")
	}
}

func TestVerifyToleratesOneStepOfSkew(t *testing.T) {
	secret, _ := GenerateTOTPSecret()
	now := time.Unix(1_700_000_000, 0).UTC()
	past, _ := totp.GenerateCode(secret, now.Add(-30*time.Second))
	future, _ := totp.GenerateCode(secret, now.Add(30*time.Second))
	if !VerifyTOTP(secret, past, now) {
		t.Error("code from the previous step rejected; clock skew will lock users out")
	}
	if !VerifyTOTP(secret, future, now) {
		t.Error("code from the next step rejected")
	}
}

func TestVerifyRejectsStaleAndWrongCodes(t *testing.T) {
	secret, _ := GenerateTOTPSecret()
	now := time.Unix(1_700_000_000, 0).UTC()
	stale, _ := totp.GenerateCode(secret, now.Add(-10*time.Minute))
	if VerifyTOTP(secret, stale, now) {
		t.Error("a ten-minute-old code was accepted")
	}
	if VerifyTOTP(secret, "000000", now) && VerifyTOTP(secret, "999999", now) {
		t.Error("verification accepts arbitrary codes")
	}
}

func TestProvisioningURIShape(t *testing.T) {
	uri := TOTPProvisioningURI("JBSWY3DPEHPK3PXP", "alice", "antimage")
	for _, want := range []string{
		"otpauth://totp/", "antimage:alice", "secret=JBSWY3DPEHPK3PXP", "issuer=antimage",
	} {
		if !strings.Contains(uri, want) {
			t.Errorf("URI %q missing %q", uri, want)
		}
	}
}

func TestRecoveryCodesAreUniqueAndHashable(t *testing.T) {
	codes, err := GenerateRecoveryCodes(10)
	if err != nil {
		t.Fatalf("GenerateRecoveryCodes: %v", err)
	}
	if len(codes) != 10 {
		t.Fatalf("got %d codes, want 10", len(codes))
	}
	seen := map[string]bool{}
	for _, c := range codes {
		if seen[c] {
			t.Fatalf("duplicate recovery code %q", c)
		}
		seen[c] = true

		hashed, err := HashRecoveryCode(c)
		if err != nil {
			t.Fatalf("HashRecoveryCode: %v", err)
		}
		ok, err := VerifyPassword(hashed, c)
		if err != nil || !ok {
			t.Errorf("recovery code %q does not verify against its hash (err=%v)", c, err)
		}
	}
}
