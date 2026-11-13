package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"

	"makinadark/backend/internal/config"
)

const tokenTTL = 7 * 24 * time.Hour

// HashPassword hashes a plaintext password for storage.
func HashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(hash), err
}

// CheckPassword compares a plaintext password against a stored bcrypt hash.
func CheckPassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

type claims struct {
	UserID string `json:"user_id"`
	jwt.RegisteredClaims
}

// GenerateToken issues a signed JWT for the given user id.
func GenerateToken(userID string) (string, error) {
	c := claims{
		UserID: userID,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(tokenTTL)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, c)
	return token.SignedString([]byte(config.AppConfig.JWTSecret))
}

func parseToken(tokenStr string) (string, error) {
	token, err := jwt.ParseWithClaims(tokenStr, &claims{}, func(t *jwt.Token) (interface{}, error) {
		return []byte(config.AppConfig.JWTSecret), nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}))
	if err != nil || !token.Valid {
		return "", fiber.ErrUnauthorized
	}
	c, ok := token.Claims.(*claims)
	if !ok || c.UserID == "" {
		return "", fiber.ErrUnauthorized
	}
	return c.UserID, nil
}

// RequireAuth is Fiber middleware that validates the Bearer token and stores
// the authenticated user id in the request context (key "user_id").
func RequireAuth(c *fiber.Ctx) error {
	header := c.Get("Authorization")
	if !strings.HasPrefix(header, "Bearer ") {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "Missing or invalid Authorization header"})
	}
	tokenStr := strings.TrimPrefix(header, "Bearer ")

	userID, err := parseToken(tokenStr)
	if err != nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "Invalid or expired token"})
	}

	c.Locals("user_id", userID)
	return c.Next()
}

// ---------------------------------------------------------------------------
// OAuth "state" signing
// ---------------------------------------------------------------------------
//
// The OAuth callback is hit by Google/TikTok redirecting the browser, so it
// can't carry our Bearer token. It used to trust a plain "youtube:<channelID>"
// state, which let anyone who knew (or guessed) a channel ID bind their own
// YouTube/TikTok account to someone else's channel. The state is now an
// HMAC-signed, expiring token naming the provider, channel AND the user who
// started the flow; the callback rejects anything it didn't issue itself.

const oauthStateTTL = 15 * time.Minute

// OAuthState is the verified payload carried through an OAuth round trip.
type OAuthState struct {
	Provider  string
	ChannelID string
	UserID    string
}

func stateMAC(data string) []byte {
	mac := hmac.New(sha256.New, []byte("oauth-state:"+config.AppConfig.JWTSecret))
	mac.Write([]byte(data))
	return mac.Sum(nil)
}

// SignOAuthState builds the opaque state value for an authorization URL.
func SignOAuthState(s OAuthState) string {
	payload := strings.Join([]string{s.Provider, s.ChannelID, s.UserID, strconv.FormatInt(time.Now().Add(oauthStateTTL).Unix(), 10)}, "|")
	enc := base64.RawURLEncoding.EncodeToString([]byte(payload))
	return enc + "." + base64.RawURLEncoding.EncodeToString(stateMAC(enc))
}

// VerifyOAuthState checks signature and expiry and returns the payload.
func VerifyOAuthState(state string) (OAuthState, error) {
	enc, sig, found := strings.Cut(state, ".")
	if !found {
		return OAuthState{}, errors.New("state malformado")
	}
	gotMAC, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || !hmac.Equal(gotMAC, stateMAC(enc)) {
		return OAuthState{}, errors.New("assinatura do state inválida")
	}
	raw, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil {
		return OAuthState{}, errors.New("state malformado")
	}
	parts := strings.Split(string(raw), "|")
	if len(parts) != 4 {
		return OAuthState{}, errors.New("state malformado")
	}
	exp, err := strconv.ParseInt(parts[3], 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return OAuthState{}, errors.New("link de autorização expirado - inicie a conexão novamente")
	}
	return OAuthState{Provider: parts[0], ChannelID: parts[1], UserID: parts[2]}, nil
}

// PKCEVerifier derives a per-flow PKCE code_verifier from the signed state,
// so it never has to be stored server-side between the redirect and the
// callback (TikTok previously used one hardcoded verifier for every user,
// which defeats the point of PKCE). 64 hex chars fits RFC 7636's 43-128.
func PKCEVerifier(state string) string {
	mac := hmac.New(sha256.New, []byte("pkce:"+config.AppConfig.JWTSecret))
	mac.Write([]byte(state))
	return hex.EncodeToString(mac.Sum(nil))
}
