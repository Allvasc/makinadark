package services

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"

	"makinadark/backend/internal/config"
)

var apiTestClient = &http.Client{Timeout: 10 * time.Second}

// TestAPIConnection makes one minimal, free-or-negligible-cost real call to
// the named provider using whatever credentials are currently configured
// (env or the operator-saved Settings override - see
// config.ApplySettingsOverride) and reports whether it succeeded. On
// failure the message is the provider's own error response, not a generic
// "failed" - so an expired key, a wrong key, and a quota/billing problem are
// distinguishable from each other and from a plain network error without
// digging through server logs.
func TestAPIConnection(provider string) (bool, string) {
	switch provider {
	case "groq":
		return testBearerGET("Groq", config.AppConfig.GroqAPIKey, "https://api.groq.com/openai/v1/models")
	case "openai":
		return testBearerGET("OpenAI", config.AppConfig.OpenAIAPIKey, "https://api.openai.com/v1/models")
	case "gemini":
		return testGemini()
	case "claude":
		return testClaude()
	case "elevenlabs":
		return testCustomHeaderGET("ElevenLabs", config.AppConfig.ElevenLabsAPIKey, "https://api.elevenlabs.io/v1/user", "xi-api-key", "")
	case "pexels":
		return testCustomHeaderGET("Pexels", config.AppConfig.PexelsAPIKey, "https://api.pexels.com/v1/search?query=test&per_page=1", "Authorization", "")
	case "pollinations":
		return testPollinations()
	case "shotstack":
		return testCustomHeaderGET("Shotstack", config.AppConfig.ShotstackAPIKey, "https://api.shotstack.io/edit/stage/templates", "x-api-key", "")
	case "r2":
		return testR2()
	case "image_ranker":
		return testImageRanker()
	case "youtube":
		return testOAuthConfigured("YouTube", config.AppConfig.YouTubeClientID, config.AppConfig.YouTubeClientSec)
	case "tiktok":
		return testOAuthConfigured("TikTok", config.AppConfig.TikTokClientID, config.AppConfig.TikTokClientSec)
	default:
		return false, fmt.Sprintf("provedor desconhecido: %q", provider)
	}
}

// isKeyConfigured mirrors handlers.isConfigured (unexported there, and this
// package can't import handlers without a cycle) - unset and the
// ".env.example" placeholder scaffold ("your_x_here") both count as "not
// configured".
func isKeyConfigured(value string) bool {
	return value != "" && !strings.HasPrefix(value, "your_")
}

// doTestRequest runs the request and turns any non-2xx response into a
// message quoting the provider's own response body (trimmed), so the actual
// reason (invalid key, quota exceeded, etc.) reaches the operator verbatim.
func doTestRequest(providerLabel string, req *http.Request) (bool, string) {
	resp, err := apiTestClient.Do(req)
	if err != nil {
		return false, fmt.Sprintf("%s: falha de rede - %v", providerLabel, err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return true, "OK"
	}
	return false, fmt.Sprintf("%s: HTTP %d - %s", providerLabel, resp.StatusCode, strings.TrimSpace(string(body)))
}

func testBearerGET(label, apiKey, url string) (bool, string) {
	if !isKeyConfigured(apiKey) {
		return false, label + ": chave não configurada"
	}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return false, label + ": " + err.Error()
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	return doTestRequest(label, req)
}

// testCustomHeaderGET covers providers that don't use the "Bearer " scheme
// (ElevenLabs' xi-api-key, Pexels' and Shotstack's raw-value Authorization/
// x-api-key headers).
func testCustomHeaderGET(label, apiKey, url, headerName, headerPrefix string) (bool, string) {
	if !isKeyConfigured(apiKey) {
		return false, label + ": chave não configurada"
	}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return false, label + ": " + err.Error()
	}
	req.Header.Set(headerName, headerPrefix+apiKey)
	return doTestRequest(label, req)
}

func testGemini() (bool, string) {
	apiKey := config.AppConfig.GeminiAPIKey
	if !isKeyConfigured(apiKey) {
		return false, "Gemini: chave não configurada"
	}
	req, err := http.NewRequest("GET", fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models?key=%s", apiKey), nil)
	if err != nil {
		return false, "Gemini: " + err.Error()
	}
	return doTestRequest("Gemini", req)
}

func testClaude() (bool, string) {
	apiKey := config.AppConfig.AnthropicAPIKey
	if !isKeyConfigured(apiKey) {
		return false, "Claude (Anthropic): chave não configurada"
	}
	req, err := http.NewRequest("GET", "https://api.anthropic.com/v1/models", nil)
	if err != nil {
		return false, "Claude (Anthropic): " + err.Error()
	}
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	return doTestRequest("Claude (Anthropic)", req)
}

// testPollinations only checks reachability - image generation for
// logo/banner/thumbnail routes exclusively through Gemini (see GenerateImage
// in ai.go); Pollinations is used anonymously for scene images with no API
// key sent (a paid key previously attached caused 15-45s timeouts vs <1s
// anonymous), so there's no credential to actually validate here.
func testPollinations() (bool, string) {
	req, err := http.NewRequest("GET", "https://image.pollinations.ai/models", nil)
	if err != nil {
		return false, "Pollinations: " + err.Error()
	}
	return doTestRequest("Pollinations", req)
}

func testR2() (bool, string) {
	if !storageConfigured() {
		return false, "Cloudflare R2: bucket/credenciais não configurados (mídia usando disco local)"
	}
	client := getR2Client()
	if client == nil {
		return false, "Cloudflare R2: falha ao montar cliente S3 a partir das credenciais configuradas"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: &config.AppConfig.R2Bucket})
	if err != nil {
		return false, fmt.Sprintf("Cloudflare R2: %v", err)
	}
	return true, "OK"
}

func testImageRanker() (bool, string) {
	if config.AppConfig.ImageRankerURL == "" {
		return false, "Image Ranker: IMAGE_RANKER_URL não configurada (seleção de mídia cai no primeiro resultado do Pexels)"
	}
	req, err := http.NewRequest("GET", config.AppConfig.ImageRankerURL+"/health", nil)
	if err != nil {
		return false, "Image Ranker: " + err.Error()
	}
	return doTestRequest("Image Ranker", req)
}

// testOAuthConfigured can only confirm the app credentials are present -
// actually validating them requires completing a real user OAuth consent
// flow (Channel > Conectar YouTube/TikTok), which isn't something a
// one-click connectivity test can do on its own.
func testOAuthConfigured(label, clientID, clientSecret string) (bool, string) {
	if !isKeyConfigured(clientID) || !isKeyConfigured(clientSecret) {
		return false, label + ": client ID/secret não configurados"
	}
	return true, label + ": credenciais presentes (validação completa requer login OAuth em um canal)"
}
