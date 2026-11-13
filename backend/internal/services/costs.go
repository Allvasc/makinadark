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
// an estimate, since providers can and do change pricing.
//   - OpenAI gpt-4o-mini: https://openai.com/api/pricing
//   - OpenAI gpt-image-1.5: per-image estimates by quality/size (see
//     openAIImageCost) - verify against the OpenAI pricing page.
//   - OpenAI tts-1: $0.015/1k chars
//   - Anthropic claude-sonnet-5-5: $2 / $10 per 1M input/output tokens
//   - ElevenLabs: https://elevenlabs.io/pricing (Creator tier, regular price)
//   - Gemini text (gemini-flash-latest): free tier, $0 - the per-key daily
//     request quota is GEMINI_FREE_TIER_DAILY_LIMIT (config.go).
const (
	openAIInputTokenCostPer1M  = 0.15     // gpt-4o-mini
	openAIOutputTokenCostPer1M = 0.60     // gpt-4o-mini
	claudeInputTokenCostPer1M  = 2.00     // claude-sonnet-5-5
	claudeOutputTokenCostPer1M = 10.00    // claude-sonnet-5-5
	geminiImageCostFlashLite1K = 0.0336   // gemini-3.1-flash-lite-image, 1K output
	openAITTSCostPerChar       = 0.000015 // tts-1, $0.015/1,000 chars
	elevenLabsCostPerChar      = 0.000182 // ElevenLabs Creator tier, $22/121,000 chars
)

// openAIImageCost estimates one gpt-image-1.5 render by quality and size.
func openAIImageCost(quality, size string) float64 {
	square := size == "1024x1024"
	switch quality {
	case "low":
		if square {
			return 0.009
		}
		return 0.013
	case "medium":
		if square {
			return 0.034
		}
		return 0.05
	default: // high
		if square {
			return 0.133
		}
		return 0.20
	}
}

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
// what matters here is the request count against the daily free-tier quota.
func RecordGeminiRequest(channelID, videoID string) {
	RecordUsage(channelID, videoID, "gemini", "llm_completion", 1, "requests", 0)
}

// RecordImageGeneration logs one OpenAI gpt-image call at its estimated cost.
func RecordImageGeneration(channelID, videoID string, costUSD float64) {
	RecordUsage(channelID, videoID, "openai", "image_generation", 1, "images", costUSD)
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
