package config

import (
	"crypto/rand"
	"encoding/hex"
	"log"
	"os"
	"strings"

	"github.com/joho/godotenv"
)

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
	YouTubeClientID  string
	YouTubeClientSec string
	TikTokClientID   string
	TikTokClientSec  string
	RedirectURI      string

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
}

var AppConfig Config

func LoadConfig() {
	// Try loading .env, but don't fail if it's missing (can be set directly in env)
	if err := godotenv.Load(); err != nil {
		log.Println("No .env file found, reading from environment variables")
	}

	AppConfig = Config{
		Port:             getEnv("PORT", "8080"),
		JWTSecret:        getEnv("JWT_SECRET", ""),
		GroqAPIKey:       os.Getenv("GROQ_API_KEY"),
		OpenAIAPIKey:     os.Getenv("OPENAI_API_KEY"),
		GeminiAPIKey:     os.Getenv("GEMINI_API_KEY"),
		AnthropicAPIKey:  os.Getenv("ANTHROPIC_API_KEY"),
		ElevenLabsAPIKey: os.Getenv("ELEVENLABS_API_KEY"),
		PexelsAPIKey:       os.Getenv("PEXELS_API_KEY"),
		PollinationsAPIKey: os.Getenv("POLLINATIONS_API_KEY"),
		ShotstackAPIKey:    os.Getenv("SHOTSTACK_API_KEY"),
		ImageRankerURL:     strings.TrimSuffix(os.Getenv("IMAGE_RANKER_URL"), "/"),
		YouTubeClientID:  os.Getenv("YOUTUBE_CLIENT_ID"),
		YouTubeClientSec: os.Getenv("YOUTUBE_CLIENT_SECRET"),
		TikTokClientID:   os.Getenv("TIKTOK_CLIENT_ID"),
		TikTokClientSec:  os.Getenv("TIKTOK_CLIENT_SECRET"),
		RedirectURI:      getEnv("REDIRECT_URI", "http://localhost:8080/api/oauth/callback"),

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
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return defaultValue
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
