package config

import (
	"crypto/rand"
	"encoding/hex"
	"log"
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

// IsSet reports whether a credential/config value is actually configured.
// Empty values AND the "your_x_here" scaffold placeholders from .env.example
// both count as "not configured" - this is the single place that rule lives
// (it used to be re-implemented, slightly differently, in five files).
func IsSet(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && !strings.HasPrefix(value, "your_")
}

type Config struct {
	Port               string
	JWTSecret          string
	GroqAPIKey         string
	OpenAIAPIKey       string
	GeminiAPIKey       string
	AnthropicAPIKey    string
	ElevenLabsAPIKey   string
	PexelsAPIKey       string
	PollinationsAPIKey string
	ShotstackAPIKey    string
	ImageRankerURL     string // internal CLIP-based image-ranker microservice, e.g. http://image-ranker:8500
	YouTubeClientID    string
	YouTubeClientSec   string
	TikTokClientID     string
	TikTokClientSec    string
	RedirectURI        string

	// Postgres connection - if PostgresHost is empty the app falls back to a
	// local SQLite file, so Postgres stays fully optional for local/dev use.
	PostgresHost     string
	PostgresPort     string
	PostgresUser     string
	PostgresPassword string
	PostgresDB       string

	// Cloudflare R2 (S3-compatible) bucket for generated images/audio/video.
	// If R2Bucket is empty the app falls back to serving files from local
	// disk under ./media, so a bucket stays fully optional for local/dev use.
	R2AccountID       string
	R2AccessKeyID     string
	R2SecretAccessKey string
	R2Bucket          string
	R2PublicBaseURL   string // e.g. https://media.example.com or the r2.dev public URL

	// CORSOrigins is only relevant when the frontend is NOT served through
	// the bundled nginx proxy (same-origin needs no CORS at all).
	CORSOrigins string

	// GeminiFreeTierDailyLimit is the per-API-key daily request quota shown
	// on the cost dashboard. Google changes it per model/tier, so it's
	// configurable instead of hardcoded in two places with two values.
	GeminiFreeTierDailyLimit int

	// ShortsMaxSeconds caps the 9:16 version's length. The vertical cut is
	// what gets posted to TikTok, so it has to stay short-form (a full
	// 20-minute crop is neither a "Short" nor accepted by TikTok's API).
	// 0 disables the cap and renders the full length.
	ShortsMaxSeconds int

	// AllowRegistration gates POST /auth/register. Every account runs on the
	// operator's own paid API keys, so open sign-up on a public URL meant any
	// stranger could burn them. Off by default; set ALLOW_REGISTRATION=true.
	AllowRegistration bool

	// AdminEmail/AdminPassword seed the first account on an empty database
	// (instead of a hardcoded, publicly known default password).
	AdminEmail    string
	AdminPassword string
}

var AppConfig Config

func LoadConfig() {
	// Try loading .env, but don't fail if it's missing (can be set directly in env)
	if err := godotenv.Load(); err != nil {
		log.Println("No .env file found, reading from environment variables")
	}

	AppConfig = Config{
		Port:               getEnv("PORT", "8080"),
		JWTSecret:          getEnv("JWT_SECRET", ""),
		GroqAPIKey:         os.Getenv("GROQ_API_KEY"),
		OpenAIAPIKey:       os.Getenv("OPENAI_API_KEY"),
		GeminiAPIKey:       os.Getenv("GEMINI_API_KEY"),
		AnthropicAPIKey:    os.Getenv("ANTHROPIC_API_KEY"),
		ElevenLabsAPIKey:   os.Getenv("ELEVENLABS_API_KEY"),
		PexelsAPIKey:       os.Getenv("PEXELS_API_KEY"),
		PollinationsAPIKey: os.Getenv("POLLINATIONS_API_KEY"),
		ShotstackAPIKey:    os.Getenv("SHOTSTACK_API_KEY"),
		ImageRankerURL:     strings.TrimSuffix(os.Getenv("IMAGE_RANKER_URL"), "/"),
		YouTubeClientID:    os.Getenv("YOUTUBE_CLIENT_ID"),
		YouTubeClientSec:   os.Getenv("YOUTUBE_CLIENT_SECRET"),
		TikTokClientID:     os.Getenv("TIKTOK_CLIENT_ID"),
		TikTokClientSec:    os.Getenv("TIKTOK_CLIENT_SECRET"),
		RedirectURI:        getEnv("REDIRECT_URI", "http://localhost:8080/api/oauth/callback"),

		PostgresHost:     os.Getenv("POSTGRES_HOST"),
		PostgresPort:     getEnv("POSTGRES_PORT", "5432"),
		PostgresUser:     os.Getenv("POSTGRES_USER"),
		PostgresPassword: os.Getenv("POSTGRES_PASSWORD"),
		PostgresDB:       os.Getenv("POSTGRES_DB"),

		R2AccountID:       os.Getenv("R2_ACCOUNT_ID"),
		R2AccessKeyID:     os.Getenv("R2_ACCESS_KEY_ID"),
		R2SecretAccessKey: os.Getenv("R2_SECRET_ACCESS_KEY"),
		R2Bucket:          os.Getenv("R2_BUCKET"),
		R2PublicBaseURL:   strings.TrimSuffix(os.Getenv("R2_PUBLIC_BASE_URL"), "/"),

		CORSOrigins:              getEnv("CORS_ORIGINS", "http://localhost:5173, http://127.0.0.1:5173, http://localhost:3000"),
		GeminiFreeTierDailyLimit: getEnvInt("GEMINI_FREE_TIER_DAILY_LIMIT", 20),
		ShortsMaxSeconds:         getEnvInt("SHORTS_MAX_SECONDS", 60),

		AllowRegistration: strings.EqualFold(strings.TrimSpace(os.Getenv("ALLOW_REGISTRATION")), "true"),
		AdminEmail:        strings.ToLower(getEnv("ADMIN_EMAIL", "admin@makinadark.com")),
		AdminPassword:     os.Getenv("ADMIN_PASSWORD"),
	}

	if AppConfig.JWTSecret == "" {
		AppConfig.JWTSecret = randomSecret()
		log.Println("WARNING: JWT_SECRET not set - generated a random one for this run. " +
			"Every restart will invalidate existing login sessions. Set JWT_SECRET in .env for stable sessions.")
	}
}

func randomSecret() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		log.Fatalf("failed to generate JWT secret: %v", err)
	}
	return hex.EncodeToString(b)
}

func getEnv(key, defaultValue string) string {
	if value, exists := os.LookupEnv(key); exists && value != "" {
		return value
	}
	return defaultValue
}

func getEnvInt(key string, defaultValue int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return defaultValue
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		log.Printf("WARNING: invalid %s=%q, using default %d", key, raw, defaultValue)
		return defaultValue
	}
	return n
}

// ApplySettingsOverride overlays non-empty values from DB-stored settings on
// top of the env-derived config, without a restart. Empty strings are treated
// as "no override" and leave the current value untouched.
func ApplySettingsOverride(openAI, gemini, elevenLabs, ytClientID, ytClientSecret, ttClientID, ttClientSecret string) {
	if openAI != "" {
		AppConfig.OpenAIAPIKey = openAI
	}
	if gemini != "" {
		AppConfig.GeminiAPIKey = gemini
	}
	if elevenLabs != "" {
		AppConfig.ElevenLabsAPIKey = elevenLabs
	}
	if ytClientID != "" {
		AppConfig.YouTubeClientID = ytClientID
	}
	if ytClientSecret != "" {
		AppConfig.YouTubeClientSec = ytClientSecret
	}
	if ttClientID != "" {
		AppConfig.TikTokClientID = ttClientID
	}
	if ttClientSecret != "" {
		AppConfig.TikTokClientSec = ttClientSecret
	}
}
