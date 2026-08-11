package services

import (
	"log"

	"github.com/google/uuid"
	"makinadark/backend/internal/db"
	"makinadark/backend/internal/models"
)

// Pricing rates below are the operator-facing estimates used to compute
// EstimatedCostUSD on ApiUsageLog rows. Units (tokens/characters/images) are
// always the exact values reported by the provider - only the $ conversion is
// an estimate, since providers can and do change pricing. Verified 2026-08-09:
//   - OpenAI gpt-4o-mini: https://openai.com/api/pricing (search results)
//   - OpenAI gpt-image-1.5: per-image pricing pages (langcopilot/pricepertoken)
//   - OpenAI tts-1: https://www.burnwise.io/ai-pricing/model/tts-1 ($0.015/1k chars)
//   - ElevenLabs: https://elevenlabs.io/pricing (Creator tier, regular price)
//   - Gemini (gemini-flash-latest): free tier, 1500 requests/day - $0. A paid/
//     prepay-linked project loses free-tier access permanently for that
//     project (see ai.go callGemini) - use a fresh AI-Studio-only key.
const (
	openAIInputTokenCostPer1M  = 0.15     // gpt-4o-mini
	openAIOutputTokenCostPer1M = 0.60     // gpt-4o-mini
	claudeInputTokenCostPer1M  = 3.00     // claude-3-5-sonnet-20241022
	claudeOutputTokenCostPer1M = 15.00    // claude-3-5-sonnet-20241022
	openAIImageCostGptImage15  = 0.034    // gpt-image-1.5, 1024x1024, medium quality
	geminiImageCostFlashLite1K = 0.0336   // gemini-3.1-flash-lite-image, 1K output
	openAITTSCostPerChar       = 0.000015 // tts-1, $0.015/1,000 chars
	elevenLabsCostPerChar      = 0.000182 // ElevenLabs Creator tier, $22/121,000 chars
)

// RecordUsage logs one external API call for the cost dashboard. channelID is
// required; videoID may be empty for channel-level calls (branding, intro/outro
// setup) that aren't tied to a single video.
func RecordUsage(channelID, videoID, provider, operation string, units float64, unitType string, costUSD float64) {
	if channelID == "" {
		return
	}
	entry := models.ApiUsageLog{
		ID:               uuid.New().String(),
		ChannelID:        channelID,
		VideoID:          videoID,
		Provider:         provider,
		Operation:        operation,
		Units:            units,
		UnitType:         unitType,
		EstimatedCostUSD: costUSD,
	}
	if err := db.DB.Create(&entry).Error; err != nil {
		log.Printf("Warning: failed to record API usage (%s/%s): %v", provider, operation, err)
	}
}

// RecordOpenAITokens logs an OpenAI chat completion call using the exact token
// counts from the API response's "usage" field.
func RecordOpenAITokens(channelID, videoID string, promptTokens, completionTokens int) {
	cost := float64(promptTokens)/1_000_000*openAIInputTokenCostPer1M + float64(completionTokens)/1_000_000*openAIOutputTokenCostPer1M
	RecordUsage(channelID, videoID, "openai", "llm_completion", float64(promptTokens+completionTokens), "tokens", cost)
}

// RecordClaudeTokens logs an Anthropic Claude completion call using exact token counts.
func RecordClaudeTokens(channelID, videoID string, inputTokens, outputTokens int) {
	cost := float64(inputTokens)/1_000_000*claudeInputTokenCostPer1M + float64(outputTokens)/1_000_000*claudeOutputTokenCostPer1M
	RecordUsage(channelID, videoID, "anthropic", "llm_completion", float64(inputTokens+outputTokens), "tokens", cost)
}

// RecordGroqTokens logs a Groq completion call (free tier, $0).
func RecordGroqTokens(channelID, videoID string, promptTokens, completionTokens int) {
	RecordUsage(channelID, videoID, "groq", "llm_completion", float64(promptTokens+completionTokens), "tokens", 0)
}

// RecordGeminiRequest logs a Gemini call. Currently always $0 (free tier) -
// what matters here is the request count, since the free tier caps at 1500/day.
func RecordGeminiRequest(channelID, videoID string) {
	RecordUsage(channelID, videoID, "gemini", "llm_completion", 1, "requests", 0)
}

// RecordImageGeneration logs one gpt-image-1.5 call.
func RecordImageGeneration(channelID, videoID string) {
	RecordUsage(channelID, videoID, "openai", "image_generation", 1, "images", openAIImageCostGptImage15)
}

// RecordGeminiImageGeneration logs one gemini-3.1-flash-lite-image call
// (logo/banner/thumbnail generation).
func RecordGeminiImageGeneration(channelID, videoID string) {
	RecordUsage(channelID, videoID, "gemini", "image_generation", 1, "images", geminiImageCostFlashLite1K)
}

// RecordTTS logs one text-to-speech call, in characters synthesized.
func RecordTTS(channelID, videoID, provider string, characters int) {
	var cost float64
	switch provider {
	case "elevenlabs":
		cost = float64(characters) * elevenLabsCostPerChar
	case "openai":
		cost = float64(characters) * openAITTSCostPerChar
	}
	RecordUsage(channelID, videoID, provider, "tts", float64(characters), "characters", cost)
}

// RecordStockMediaRequest logs a Pexels (or similar free provider) call.
func RecordStockMediaRequest(channelID, videoID, provider string) {
	RecordUsage(channelID, videoID, provider, "stock_media", 1, "requests", 0)
}
