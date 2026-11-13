package auth

import (
	"strings"
	"testing"

	"makinadark/backend/internal/config"
)

func TestOAuthStateRoundTrip(t *testing.T) {
	config.AppConfig.JWTSecret = "test-secret"
	in := OAuthState{Provider: "youtube", ChannelID: "ch-1", UserID: "user-1"}

	got, err := VerifyOAuthState(SignOAuthState(in))
	if err != nil {
		t.Fatalf("verify failed: %v", err)
	}
	if got != in {
		t.Fatalf("got %+v, want %+v", got, in)
	}
}

func TestOAuthStateRejectsTampering(t *testing.T) {
	config.AppConfig.JWTSecret = "test-secret"
	state := SignOAuthState(OAuthState{Provider: "tiktok", ChannelID: "ch-1", UserID: "user-1"})
	enc, sig, _ := strings.Cut(state, ".")

	forged := SignOAuthState(OAuthState{Provider: "tiktok", ChannelID: "someone-elses-channel", UserID: "user-1"})
	forgedEnc, _, _ := strings.Cut(forged, ".")

	cases := map[string]string{
		"legacy plain state":  "youtube:ch-1",
		"payload swapped":     forgedEnc + "." + sig,
		"signature truncated": enc + "." + sig[:len(sig)-2],
		"signed by other key": func() string {
			config.AppConfig.JWTSecret = "other"
			s := SignOAuthState(OAuthState{"tiktok", "ch-1", "user-1"})
			config.AppConfig.JWTSecret = "test-secret"
			return s
		}(),
		"empty": "",
	}
	for name, s := range cases {
		if _, err := VerifyOAuthState(s); err == nil {
			t.Errorf("%s: expected rejection", name)
		}
	}
}

func TestPKCEVerifierShape(t *testing.T) {
	config.AppConfig.JWTSecret = "test-secret"
	v := PKCEVerifier("some-state")
	if len(v) < 43 || len(v) > 128 {
		t.Fatalf("verifier length %d outside RFC 7636 range", len(v))
	}
	if v != PKCEVerifier("some-state") {
		t.Fatal("verifier must be deterministic per state")
	}
	if v == PKCEVerifier("other-state") {
		t.Fatal("verifier must differ per state")
	}
}
