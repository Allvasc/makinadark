package models

import (
	"time"
)

type User struct {
	ID           string    `gorm:"primaryKey" json:"id"`
	Email        string    `gorm:"uniqueIndex" json:"email"`
	PasswordHash string    `json:"-"`
	Name         string    `json:"name"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	Channels     []Channel `gorm:"foreignKey:UserID" json:"channels,omitempty"`
}

type Channel struct {
	ID                 string           `gorm:"primaryKey" json:"id"`
	UserID             string           `json:"user_id"`
	Name               string           `json:"name"`
	Niche              string           `json:"niche"`
	Concept            string           `json:"concept"`
	Persona            string           `json:"persona"`
	VoiceType          string           `json:"voice_type"`
	NarratorVoiceID    string           `json:"narrator_voice_id"` // ElevenLabs voice ID selected for this channel's narration - empty uses the app default (Adam)
	NarrationTone      string           `json:"narration_tone"`
	LogoPath           string           `json:"logo_path"`
	BannerPath         string           `json:"banner_path"`
	AutoPilotEnabled   bool             `gorm:"default:false" json:"autopilot_enabled"`
	YTChannelID        string           `json:"yt_channel_id"`
	YTAccessToken      string           `json:"-"`
	YTRefreshToken     string           `json:"-"`
	YTTokenExpiry      time.Time        `json:"yt_token_expiry"`
	YTConnected        bool             `gorm:"-" json:"yt_connected"`
	TikTokAccountID    string           `json:"tiktok_account_id"`
	TikTokAccessToken  string           `json:"-"`
	TikTokRefreshToken string           `json:"-"`
	TikTokTokenExpiry  time.Time        `json:"tiktok_token_expiry"`
	TikTokConnected    bool             `gorm:"-" json:"tiktok_connected"`
	InstaAccountID     string           `json:"insta_account_id"`
	InstaAccessToken   string           `json:"-"`
	IntroVideoPath     string           `json:"intro_video_path"`                    // generated once, prepended to every rendered video
	OutroVideoPath     string           `json:"outro_video_path"`                    // generated once, appended to every rendered video
	IntroScript        string           `json:"intro_script"`                        // narration text, needed to caption the intro during render
	OutroScript        string           `json:"outro_script"`                        // narration text, needed to caption the outro during render
	BookendsReady      bool             `gorm:"default:false" json:"bookends_ready"` // true once intro/outro generation has finished (success or permanent failure)
	IntroStatus        string           `json:"intro_status"`                        // "idle", "generating", "ready", "failed"
	IntroProgress      int              `gorm:"default:0" json:"intro_progress"`     // 0-100
	IntroStage         string           `json:"intro_stage,omitempty"`
	IntroError         string           `json:"intro_error,omitempty"`
	OutroStatus        string           `json:"outro_status"`                        // "idle", "generating", "ready", "failed"
	OutroProgress      int              `gorm:"default:0" json:"outro_progress"`     // 0-100
	OutroStage         string           `json:"outro_stage,omitempty"`
	OutroError         string           `json:"outro_error,omitempty"`
	CreatedAt          time.Time        `json:"created_at"`
	UpdatedAt          time.Time        `json:"updated_at"`
	Videos             []Video          `gorm:"foreignKey:ChannelID" json:"videos,omitempty"`
	AutoPilotConfig    *AutoPilotConfig `gorm:"foreignKey:ChannelID" json:"autopilot_config,omitempty"`
}

// SettingsRowID is the fixed primary key of the single AppSettings row.
const SettingsRowID = "singleton"

// AppSettings is a single-row table holding operator-configured API keys that
// override the .env values at runtime (edited from the Settings tab). The raw
// secret values are never serialized back to the client - see handlers.GetSettings.
type AppSettings struct {
	ID               string    `gorm:"primaryKey" json:"id"`
	OpenAIAPIKey     string    `json:"-"`
	GeminiAPIKey     string    `json:"-"`
	ElevenLabsAPIKey string    `json:"-"`
	YouTubeClientID  string    `json:"-"`
	YouTubeClientSec string    `json:"-"`
	TikTokClientID   string    `json:"-"`
	TikTokClientSec  string    `json:"-"`
	UpdatedAt        time.Time `json:"updated_at"`
}

type AutoPilotConfig struct {
	ID                  string     `gorm:"primaryKey" json:"id"`
	ChannelID           string     `gorm:"uniqueIndex" json:"channel_id"`
	VideosPerWeek       int        `gorm:"default:7" json:"videos_per_week"`
	PreferredPostHour   int        `gorm:"default:18" json:"preferred_post_hour"` // 18:00
	TargetCTR           float64    `gorm:"default:7.5" json:"target_ctr"`         // 7.5%
	TargetRetention     float64    `gorm:"default:60.0" json:"target_retention"`  // 60%
	CurrentStrategy     string     `json:"current_strategy"`                      // Strategy notes generated by AI
	LastOptimizationAt  time.Time  `json:"last_optimization_at"`
	ConsecutiveFailures int        `gorm:"default:0" json:"consecutive_failures"` // resets to 0 on any successful cycle; drives backoff so a stuck channel doesn't retry every 10 minutes forever
	LastFailureAt       *time.Time `json:"last_failure_at,omitempty"`
	LastFailureReason   string     `json:"last_failure_reason,omitempty"`
}

type Video struct {
	ID                 string         `gorm:"primaryKey" json:"id"`
	ChannelID          string         `json:"channel_id"`
	Title              string         `json:"title"`
	Description        string         `json:"description"`
	Tags               string         `json:"tags"` // comma-separated
	Script             string         `json:"script"`
	Status             string         `json:"status"` // "draft", "generating", "ready", "scheduled", "posted", "failed"
	VideoPath          string         `json:"video_path"`
	ShortsPath         string         `json:"shorts_path"`
	SubtitlesPath      string         `json:"subtitles_path"`
	ThumbnailPath      string         `json:"thumbnail_path"`
	BurnSubtitles      bool           `gorm:"default:true" json:"burn_subtitles"`
	RenderJobID        string         `json:"render_job_id"` // Shotstack/Creatomate Cloud Job ID
	ScheduledFor       *time.Time     `json:"scheduled_for,omitempty"`
	PostedAt           *time.Time     `json:"posted_at,omitempty"`
	MediaPurged        bool           `gorm:"default:false" json:"media_purged"` // true once the rendered files were auto-deleted from storage after posting
	RenderProgress     int            `gorm:"default:0" json:"render_progress"`  // 0-100, only meaningful while status == "generating"
	RenderStage        string         `json:"render_stage,omitempty"`            // human-readable current step, e.g. "Renderizando cena 3 de 7"
	ErrorMessage       string         `json:"error_message,omitempty"`
	DegradedSceneCount int            `gorm:"default:0" json:"degraded_scene_count"` // scenes that fell back to silent audio and/or a generic stock clip during render - set once render finishes, so a "ready" video is never indistinguishable from a hollow one
	TotalSceneCount    int            `gorm:"default:0" json:"total_scene_count"`    // scene count at render time, for computing the degraded percentage even after purgeSceneAssets clears Scenes
	ViralScore         int            `gorm:"default:88" json:"viral_score"`
	HookPowerScore     int            `gorm:"default:90" json:"hook_power_score"`
	RetentionScore     int            `gorm:"default:85" json:"retention_score"`
	CTRScore           int            `gorm:"default:92" json:"ctr_score"`
	OriginalityScore   int            `gorm:"default:95" json:"originality_score"`
	ScoreNotes         string         `json:"score_notes,omitempty"`
	MusicMood          string         `json:"music_mood,omitempty"`                    // "suspense", "business", or "ambient" - decided by the Sonoplastia agent, overrides niche keyword-matching
	MusicMixLevel      float64        `gorm:"default:0.12" json:"music_mix_level"`     // background music volume relative to narration (0.05-0.25), decided by the Sonoplastia agent
	UseFadeTransitions bool           `gorm:"default:true" json:"use_fade_transitions"` // whether scenes fade in/out at cuts for a more dramatic feel - decided by the Diretor agent
	AgentNotes         string         `json:"agent_notes,omitempty"`                   // last supervisor feedback per stage, semicolon-joined, for operator visibility
	CreatedAt          time.Time      `json:"created_at"`
	UpdatedAt          time.Time      `json:"updated_at"`
	Scenes             []Scene        `gorm:"foreignKey:VideoID" json:"scenes,omitempty"`
	Analytics          *AnalyticsData `gorm:"foreignKey:VideoID" json:"analytics,omitempty"`
}

type Scene struct {
	ID            string    `gorm:"primaryKey" json:"id"`
	VideoID       string    `json:"video_id"`
	Index         int       `json:"index"`
	NarrationText string    `json:"narration_text"`
	ImagePath     string    `json:"image_path"`
	VideoClipURL  string    `json:"video_clip_url"` // Watermark-free stock video clip (Pexels), downloaded and persisted to storage
	AudioPath     string    `json:"audio_path"`
	Duration      float64   `json:"duration"`
	Degraded      bool      `gorm:"default:false" json:"degraded"` // true if this scene fell back to silent audio and/or a generic stock clip during render
	CreatedAt     time.Time `json:"createdAt"`
}

// ApiUsageLog records one billable (or free-tier) external API call so spend
// can be broken down per video, per channel, and per provider/day. Units are
// always the real, precise value reported by the provider (tokens, characters,
// images, bytes); EstimatedCostUSD is derived from a documented rate table in
// services/costs.go and is explicitly an estimate - provider pricing can change.
type ApiUsageLog struct {
	ID               string    `gorm:"primaryKey" json:"id"`
	ChannelID        string    `gorm:"index" json:"channel_id"`
	VideoID          string    `gorm:"index" json:"video_id,omitempty"`
	Provider         string    `json:"provider"`  // "openai", "gemini", "elevenlabs", "pexels"
	Operation        string    `json:"operation"` // "llm_completion", "image_generation", "tts", "stock_media"
	Units            float64   `json:"units"`     // meaning depends on UnitType
	UnitType         string    `json:"unit_type"` // "input_tokens+output_tokens", "characters", "images", "requests"
	EstimatedCostUSD float64   `json:"estimated_cost_usd"`
	CreatedAt        time.Time `json:"created_at"`
}

type AnalyticsData struct {
	ID             string    `gorm:"primaryKey" json:"id"`
	VideoID        string    `gorm:"uniqueIndex" json:"video_id"`
	Views          int64     `json:"views"`
	Likes          int64     `json:"likes"`
	Comments       int64     `json:"comments"`
	CTR            float64   `json:"ctr"`            // Click-Through Rate %
	RetentionRate  float64   `json:"retention_rate"` // % Average view duration
	EstimatedRPM   float64   `json:"estimated_rpm"`  // RPM in USD
	LastRecordedAt time.Time `json:"last_recorded_at"`
}
