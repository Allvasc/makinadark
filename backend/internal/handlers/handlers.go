package handlers

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"makinadark/backend/internal/auth"
	"makinadark/backend/internal/config"
	"makinadark/backend/internal/db"
	"makinadark/backend/internal/models"
	"makinadark/backend/internal/services"
)

// SetupRoutes registers all endpoints of the REST API
func SetupRoutes(app *fiber.App) {
	api := app.Group("/api")

	// Public auth endpoints
	api.Post("/auth/register", Register)
	api.Post("/auth/login", Login)

	// OAuth linking/callback are hit via plain browser navigation (new tab /
	// redirect from Google/TikTok), so they can't carry an Authorization
	// header and stay outside the protected group.
	api.Get("/channels/:id/auth/youtube", AuthYouTube)
	api.Get("/channels/:id/auth/tiktok", AuthTikTok)
	api.Get("/oauth/callback", OAuthCallback)

	// Everything else requires a valid Bearer token
	protected := api.Group("", auth.RequireAuth)

	protected.Get("/channels", ListChannels)
	protected.Post("/channels", CreateChannel)
	protected.Get("/channels/:id", GetChannel)
	protected.Post("/channels/:id/generate-branding", GenerateBranding)
	protected.Post("/channels/:id/generate-logo", GenerateLogo)
	protected.Post("/channels/:id/generate-banner", GenerateBanner)
	protected.Post("/channels/:id/generate-intro", GenerateIntro)
	protected.Post("/channels/:id/generate-outro", GenerateOutro)
	protected.Delete("/channels/:id/intro", DeleteIntro)
	protected.Delete("/channels/:id/outro", DeleteOutro)
	protected.Post("/channels/:id/toggle-autopilot", ToggleAutoPilot)
	protected.Get("/channels/:id/autopilot-status", GetAutoPilotStatus)
	protected.Get("/narrator-voices", GetNarratorVoices)
	protected.Put("/channels/:id/narrator-voice", SetNarratorVoice)
	protected.Get("/channels/:id/analytics", GetChannelAnalytics)
	protected.Get("/channels/:id/costs", GetChannelCosts)

	protected.Get("/trends", GetTrends)

	protected.Get("/channels/:channelId/videos", ListVideos)
	protected.Post("/channels/:channelId/videos", CreateVideo)
	protected.Get("/videos/:id", GetVideo)
	protected.Post("/videos/:id/render", TriggerRender)
	protected.Post("/videos/:id/schedule", ScheduleVideo)
	protected.Delete("/videos/:id", DeleteVideo)

	protected.Get("/settings", GetSettings)
	protected.Put("/settings", UpdateSettings)
}

// currentUserID reads the authenticated user id stashed by auth.RequireAuth.
func currentUserID(c *fiber.Ctx) string {
	uid, _ := c.Locals("user_id").(string)
	return uid
}

// setConnectionFlags derives the public yt_connected/tiktok_connected booleans
// from the (non-serialized) OAuth tokens so the frontend never receives raw secrets.
func setConnectionFlags(ch *models.Channel) {
	ch.YTConnected = ch.YTRefreshToken != ""
	ch.TikTokConnected = ch.TikTokRefreshToken != ""
}

// ---------------------------------------------------------------------------
// Auth
// ---------------------------------------------------------------------------

type RegisterInput struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	Name     string `json:"name"`
}

func Register(c *fiber.Ctx) error {
	var input RegisterInput
	if err := c.BodyParser(&input); err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "Invalid request body"})
	}

	input.Email = strings.ToLower(strings.TrimSpace(input.Email))
	if input.Email == "" || len(input.Password) < 8 {
		return c.Status(400).JSON(fiber.Map{"error": "Email is required and password must be at least 8 characters"})
	}

	var existing models.User
	if err := db.DB.First(&existing, "email = ?", input.Email).Error; err == nil {
		return c.Status(409).JSON(fiber.Map{"error": "Email already registered"})
	}

	hash, err := auth.HashPassword(input.Password)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": "Failed to process password"})
	}

	user := models.User{
		ID:           uuid.New().String(),
		Email:        input.Email,
		PasswordHash: hash,
		Name:         input.Name,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
	if err := db.DB.Create(&user).Error; err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}

	token, err := auth.GenerateToken(user.ID)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": "Failed to generate token"})
	}

	return c.Status(201).JSON(fiber.Map{
		"token": token,
		"user":  fiber.Map{"id": user.ID, "email": user.Email, "name": user.Name},
	})
}

type LoginInput struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func Login(c *fiber.Ctx) error {
	var input LoginInput
	if err := c.BodyParser(&input); err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "Invalid request body"})
	}
	input.Email = strings.ToLower(strings.TrimSpace(input.Email))

	var user models.User
	if err := db.DB.First(&user, "email = ?", input.Email).Error; err != nil {
		return c.Status(401).JSON(fiber.Map{"error": "Invalid email or password"})
	}

	if !auth.CheckPassword(user.PasswordHash, input.Password) {
		return c.Status(401).JSON(fiber.Map{"error": "Invalid email or password"})
	}

	token, err := auth.GenerateToken(user.ID)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": "Failed to generate token"})
	}

	return c.JSON(fiber.Map{
		"token": token,
		"user":  fiber.Map{"id": user.ID, "email": user.Email, "name": user.Name},
	})
}

// ---------------------------------------------------------------------------
// Channels
// ---------------------------------------------------------------------------

func ListChannels(c *fiber.Ctx) error {
	var channels []models.Channel
	if err := db.DB.Where("user_id = ?", currentUserID(c)).Order("created_at desc").Find(&channels).Error; err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	for i := range channels {
		setConnectionFlags(&channels[i])
	}
	return c.JSON(channels)
}

type CreateChannelInput struct {
	Name  string `json:"name"`
	Niche string `json:"niche"`
}

func CreateChannel(c *fiber.Ctx) error {
	var input CreateChannelInput
	if err := c.BodyParser(&input); err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "Invalid request body"})
	}

	if input.Niche == "" {
		return c.Status(400).JSON(fiber.Map{"error": "Niche is required"})
	}

	channelID := uuid.New().String()
	channelName := input.Name
	concept := ""
	logoPath := ""
	bannerPath := ""

	// 1. Generate Branding via LLM
	branding, err := services.GenerateBranding(channelID, input.Niche)
	if err != nil {
		log.Printf("Branding warning: %v", err)
	}

	if channelName == "" && branding != nil && branding.Name != "" {
		channelName = branding.Name
	}
	if channelName == "" {
		channelName = fmt.Sprintf("Império do %s", input.Niche)
	}

	concept = ""
	if branding != nil && branding.Concept != "" {
		concept = branding.Concept
	} else {
		concept = fmt.Sprintf("Canal dark focado em produzir conteúdos de alto engajamento em %s.", input.Niche)
	}

	logoPrompt := fmt.Sprintf("A sleek minimalist vector logo depicting %s, centered icon, dark background, 4k", input.Niche)
	bannerPrompt := fmt.Sprintf("A cinematic horizontal YouTube banner representing %s, dark atmospheric lighting, 4k", input.Niche)

	if branding != nil && branding.LogoPrompt != "" {
		logoPrompt = branding.LogoPrompt
	}
	if branding != nil && branding.BannerPrompt != "" {
		bannerPrompt = branding.BannerPrompt
	}

	// 2. ALWAYS generate Logo (DALL-E 3) AND Banner (DALL-E 3) images!
	log.Printf("Generating DALL-E 3 Logo for channel '%s'...", channelName)
	var errLogo error
	logoPath, errLogo = services.GenerateImage(channelID, "", logoPrompt, "logo")
	if errLogo != nil {
		log.Printf("Logo generation error: %v", errLogo)
	}

	log.Printf("Generating DALL-E 3 Banner for channel '%s'...", channelName)
	var errBanner error
	bannerPath, errBanner = services.GenerateImage(channelID, "", bannerPrompt, "banner")
	if errBanner != nil {
		log.Printf("Banner generation error: %v", errBanner)
	}

	persona := "Investigador Enigmático"
	voiceType := "Onyx (Masculina Grave)"
	narrationTone := "Dramático e Pausado"

	if branding != nil {
		if branding.Persona != "" {
			persona = branding.Persona
		}
		if branding.VoiceType != "" {
			voiceType = branding.VoiceType
		}
		if branding.NarrationTone != "" {
			narrationTone = branding.NarrationTone
		}
	}

	channel := models.Channel{
		ID:               channelID,
		UserID:           currentUserID(c),
		Name:             channelName,
		Niche:            input.Niche,
		Concept:          concept,
		Persona:          persona,
		VoiceType:        voiceType,
		NarrationTone:    narrationTone,
		LogoPath:         logoPath,
		BannerPath:       bannerPath,
		AutoPilotEnabled: true, // Default to Auto-Pilot for 0-thinking automation!
		CreatedAt:        time.Now(),
		UpdatedAt:        time.Now(),
	}

	if err := db.DB.Create(&channel).Error; err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}

	// Give every channel a default Auto-Pilot config so the engine has real
	// videos-per-week / preferred-hour limits to respect instead of posting unbounded.
	autoPilotConfig := models.AutoPilotConfig{
		ID:        uuid.New().String(),
		ChannelID: channel.ID,
	}
	if err := db.DB.Create(&autoPilotConfig).Error; err != nil {
		log.Printf("Warning: failed to create default AutoPilotConfig for channel %s: %v", channel.ID, err)
	}

	log.Printf("Channel '%s' created successfully with Logo (%s) and Banner (%s)", channelName, logoPath, bannerPath)

	// Generate the channel's intro/outro clips once in the background - every
	// video rendered from now on will pick them up automatically (see CompileVideo).
	go services.GenerateChannelBookends(channel.ID)

	setConnectionFlags(&channel)
	return c.Status(201).JSON(channel)
}

func GetChannel(c *fiber.Ctx) error {
	id := c.Params("id")
	var channel models.Channel
	if err := db.DB.First(&channel, "id = ? AND user_id = ?", id, currentUserID(c)).Error; err != nil {
		return c.Status(404).JSON(fiber.Map{"error": "Channel not found"})
	}
	setConnectionFlags(&channel)
	return c.JSON(channel)
}

// GetAutoPilotStatus surfaces the Auto-Pilot backoff/failure state that
// previously existed only in server logs nobody reads - a channel stuck
// failing every cycle showed no sign of it anywhere in the app.
func GetAutoPilotStatus(c *fiber.Ctx) error {
	channel, ok := ownedChannel(c, c.Params("id"))
	if !ok {
		return nil
	}

	var cfg models.AutoPilotConfig
	if err := db.DB.Where("channel_id = ?", channel.ID).First(&cfg).Error; err != nil {
		return c.JSON(fiber.Map{"consecutive_failures": 0})
	}

	return c.JSON(cfg)
}

// GenerateBranding (re)generates ONLY the logo/banner images for an existing
// channel - it must never touch Name/Concept/Persona, since the UI exposes
// this as "Gerar Capa & Logo" on channels that already have their identity set.
func GenerateBranding(c *fiber.Ctx) error {
	id := c.Params("id")
	var channel models.Channel
	if err := db.DB.First(&channel, "id = ? AND user_id = ?", id, currentUserID(c)).Error; err != nil {
		return c.Status(404).JSON(fiber.Map{"error": "Channel not found"})
	}

	branding, err := services.GenerateBranding(channel.ID, channel.Niche)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": "Failed to generate branding: " + err.Error()})
	}

	logoImg, logoErr := services.GenerateImage(channel.ID, "", branding.LogoPrompt, "logo")
	if logoErr == nil {
		channel.LogoPath = logoImg
	}
	bannerImg, bannerErr := services.GenerateImage(channel.ID, "", branding.BannerPrompt, "banner")
	if bannerErr == nil {
		channel.BannerPath = bannerImg
	}

	if logoErr != nil && bannerErr != nil {
		return c.Status(502).JSON(fiber.Map{"error": "Failed to generate logo and banner: " + logoErr.Error()})
	}

	channel.UpdatedAt = time.Now()
	db.DB.Save(&channel)

	setConnectionFlags(&channel)
	return c.JSON(channel)
}

func GenerateLogo(c *fiber.Ctx) error {
	id := c.Params("id")
	channel, ok := ownedChannel(c, id)
	if !ok {
		return nil
	}

	branding, _ := services.GenerateBranding(channel.ID, channel.Niche)
	logoPrompt := fmt.Sprintf("A sleek minimalist vector logo depicting %s, centered icon, dark background, 4k, no text", channel.Niche)
	if branding != nil && branding.LogoPrompt != "" {
		logoPrompt = branding.LogoPrompt
	}

	logoImg, logoErr := services.GenerateImage(channel.ID, "", logoPrompt, "logo")
	if logoErr != nil {
		return c.Status(502).JSON(fiber.Map{"error": "Failed to generate logo: " + logoErr.Error()})
	}

	channel.LogoPath = logoImg
	channel.UpdatedAt = time.Now()
	db.DB.Save(&channel)

	setConnectionFlags(&channel)
	return c.JSON(channel)
}

func GenerateBanner(c *fiber.Ctx) error {
	id := c.Params("id")
	channel, ok := ownedChannel(c, id)
	if !ok {
		return nil
	}

	branding, _ := services.GenerateBranding(channel.ID, channel.Niche)
	bannerPrompt := fmt.Sprintf("A cinematic horizontal YouTube banner representing %s, dark atmospheric lighting, 4k, no text", channel.Niche)
	if branding != nil && branding.BannerPrompt != "" {
		bannerPrompt = branding.BannerPrompt
	}

	bannerImg, bannerErr := services.GenerateImage(channel.ID, "", bannerPrompt, "banner")
	if bannerErr != nil {
		return c.Status(502).JSON(fiber.Map{"error": "Failed to generate banner: " + bannerErr.Error()})
	}

	channel.BannerPath = bannerImg
	channel.UpdatedAt = time.Now()
	db.DB.Save(&channel)

	setConnectionFlags(&channel)
	return c.JSON(channel)
}

type BookendInput struct {
	Script string `json:"script"`
}

func GenerateIntro(c *fiber.Ctx) error {
	id := c.Params("id")
	channel, ok := ownedChannel(c, id)
	if !ok {
		return nil
	}

	var input BookendInput
	_ = c.BodyParser(&input)

	channel.IntroStatus = "generating"
	channel.IntroProgress = 10
	channel.IntroStage = "Iniciando geração da abertura..."
	channel.IntroError = ""
	db.DB.Save(&channel)

	go func(chID, script string) {
		if err := services.GenerateIntro(chID, script); err != nil {
			log.Printf("Background GenerateIntro error for channel %s: %v", chID, err)
		}
	}(channel.ID, input.Script)

	setConnectionFlags(&channel)
	return c.JSON(channel)
}

func GenerateOutro(c *fiber.Ctx) error {
	id := c.Params("id")
	channel, ok := ownedChannel(c, id)
	if !ok {
		return nil
	}

	var input BookendInput
	_ = c.BodyParser(&input)

	channel.OutroStatus = "generating"
	channel.OutroProgress = 10
	channel.OutroStage = "Iniciando geração do encerramento..."
	channel.OutroError = ""
	db.DB.Save(&channel)

	go func(chID, script string) {
		if err := services.GenerateOutro(chID, script); err != nil {
			log.Printf("Background GenerateOutro error for channel %s: %v", chID, err)
		}
	}(channel.ID, input.Script)

	setConnectionFlags(&channel)
	return c.JSON(channel)
}

func DeleteIntro(c *fiber.Ctx) error {
	id := c.Params("id")
	channel, ok := ownedChannel(c, id)
	if !ok {
		return nil
	}

	if err := services.DeleteIntro(channel.ID); err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}

	if err := db.DB.First(&channel, "id = ?", channel.ID).Error; err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}

	setConnectionFlags(&channel)
	return c.JSON(channel)
}

func DeleteOutro(c *fiber.Ctx) error {
	id := c.Params("id")
	channel, ok := ownedChannel(c, id)
	if !ok {
		return nil
	}

	if err := services.DeleteOutro(channel.ID); err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}

	if err := db.DB.First(&channel, "id = ?", channel.ID).Error; err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}

	setConnectionFlags(&channel)
	return c.JSON(channel)
}

// ---------------------------------------------------------------------------
// Videos
// ---------------------------------------------------------------------------

// ownedChannel loads a channel by id, scoped to the authenticated user. It
// writes a 404 response and returns ok=false when the channel doesn't exist
// or isn't owned by the caller (existence isn't leaked either way).
func ownedChannel(c *fiber.Ctx, channelID string) (channel models.Channel, ok bool) {
	if err := db.DB.First(&channel, "id = ? AND user_id = ?", channelID, currentUserID(c)).Error; err != nil {
		c.Status(404).JSON(fiber.Map{"error": "Channel not found"})
		return channel, false
	}
	return channel, true
}

// ownedVideo loads a video by id and verifies its parent channel belongs to
// the authenticated user, writing a 404 response otherwise.
func ownedVideo(c *fiber.Ctx, videoID string, preloadScenes bool) (video models.Video, ok bool) {
	query := db.DB
	if preloadScenes {
		query = query.Preload("Scenes")
	}
	if err := query.First(&video, "id = ?", videoID).Error; err != nil {
		c.Status(404).JSON(fiber.Map{"error": "Video not found"})
		return video, false
	}
	if _, chOk := ownedChannel(c, video.ChannelID); !chOk {
		return video, false
	}
	return video, true
}

func ListVideos(c *fiber.Ctx) error {
	channelID := c.Params("channelId")
	if _, ok := ownedChannel(c, channelID); !ok {
		return nil
	}

	var videos []models.Video
	if err := db.DB.Preload("Scenes").Order("created_at desc").Find(&videos, "channel_id = ?", channelID).Error; err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	return c.JSON(videos)
}

type CreateVideoInput struct {
	Topic string `json:"topic"`
}

// CreateVideo returns immediately with a placeholder "generating" video and
// does script+scene generation in the background (see services.GenerateVideoScenes) -
// that step alone can take several minutes (LLM call + up to 8 scenes, each
// with an image and a TTS call), which would otherwise be indistinguishable
// from the request just hanging. The frontend polls GetVideo and shows the
// same progress bar used for rendering (render_progress/render_stage).
func CreateVideo(c *fiber.Ctx) error {
	channelID := c.Params("channelId")
	var input CreateVideoInput
	if err := c.BodyParser(&input); err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "Invalid request body"})
	}

	channel, ok := ownedChannel(c, channelID)
	if !ok {
		return nil
	}

	videoID := uuid.New().String()
	video := models.Video{
		ID:             videoID,
		ChannelID:      channelID,
		Title:          "Gerando roteiro por IA...",
		Status:         "generating",
		RenderProgress: 0,
		RenderStage:    "Preparando...",
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}
	if err := db.DB.Create(&video).Error; err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}

	go services.GenerateVideoScenes(channel, videoID, input.Topic)

	return c.Status(202).JSON(video)
}

func GetVideo(c *fiber.Ctx) error {
	video, ok := ownedVideo(c, c.Params("id"), true)
	if !ok {
		return nil
	}
	return c.JSON(video)
}

func TriggerRender(c *fiber.Ctx) error {
	video, ok := ownedVideo(c, c.Params("id"), false)
	if !ok {
		return nil
	}

	// Trigger async video rendering
	go services.CompileVideo(video.ID)

	return c.JSON(fiber.Map{"status": "queued", "message": "Video rendering pipeline started"})
}

type ScheduleVideoInput struct {
	ScheduledFor string `json:"scheduled_for"` // ISO string
}

func ScheduleVideo(c *fiber.Ctx) error {
	var input ScheduleVideoInput
	if err := c.BodyParser(&input); err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "Invalid request body"})
	}

	scheduledTime, err := time.Parse(time.RFC3339, input.ScheduledFor)
	if err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "Invalid date format, use RFC3339 (e.g. 2026-08-10T15:00:00Z)"})
	}

	video, ok := ownedVideo(c, c.Params("id"), false)
	if !ok {
		return nil
	}

	if video.Status != "ready" && video.Status != "scheduled" {
		return c.Status(400).JSON(fiber.Map{"error": "Only videos with 'ready' status can be scheduled"})
	}

	video.Status = "scheduled"
	video.ScheduledFor = &scheduledTime
	video.UpdatedAt = time.Now()

	db.DB.Save(&video)

	return c.JSON(video)
}

// DeleteVideo lets the operator manually wipe a video's rendered output and
// every scene image/audio file (from R2 or local disk), then removes the
// video record itself. Scenes and analytics cascade-delete alongside it.
func DeleteVideo(c *fiber.Ctx) error {
	video, ok := ownedVideo(c, c.Params("id"), true)
	if !ok {
		return nil
	}

	if video.VideoPath != "" {
		if err := services.DeleteMedia(video.VideoPath); err != nil {
			log.Printf("Warning: failed to delete video file for %s: %v", video.ID, err)
		}
	}
	if video.ShortsPath != "" {
		if err := services.DeleteMedia(video.ShortsPath); err != nil {
			log.Printf("Warning: failed to delete shorts file for %s: %v", video.ID, err)
		}
	}
	if video.SubtitlesPath != "" {
		if err := services.DeleteMedia(video.SubtitlesPath); err != nil {
			log.Printf("Warning: failed to delete subtitles file for %s: %v", video.ID, err)
		}
	}
	if video.ThumbnailPath != "" {
		if err := services.DeleteMedia(video.ThumbnailPath); err != nil {
			log.Printf("Warning: failed to delete thumbnail for %s: %v", video.ID, err)
		}
	}
	for _, scene := range video.Scenes {
		if scene.ImagePath != "" {
			if err := services.DeleteMedia(scene.ImagePath); err != nil {
				log.Printf("Warning: failed to delete scene image for %s: %v", scene.ID, err)
			}
		}
		if scene.AudioPath != "" {
			if err := services.DeleteMedia(scene.AudioPath); err != nil {
				log.Printf("Warning: failed to delete scene audio for %s: %v", scene.ID, err)
			}
		}
		if scene.VideoClipURL != "" {
			if err := services.DeleteMedia(scene.VideoClipURL); err != nil {
				log.Printf("Warning: failed to delete scene video clip for %s: %v", scene.ID, err)
			}
		}
	}

	db.DB.Where("video_id = ?", video.ID).Delete(&models.Scene{})
	db.DB.Where("video_id = ?", video.ID).Delete(&models.AnalyticsData{})
	if err := db.DB.Delete(&video).Error; err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}

	return c.JSON(fiber.Map{"status": "deleted", "video_id": video.ID})
}

func ToggleAutoPilot(c *fiber.Ctx) error {
	channel, ok := ownedChannel(c, c.Params("id"))
	if !ok {
		return nil
	}

	channel.AutoPilotEnabled = !channel.AutoPilotEnabled
	channel.UpdatedAt = time.Now()
	db.DB.Save(&channel)

	return c.JSON(fiber.Map{
		"channel_id":        channel.ID,
		"autopilot_enabled": channel.AutoPilotEnabled,
	})
}

// availableNarratorVoices is the fixed list of ElevenLabs voices the channel
// narrator picker offers - all Brazilian Portuguese, matching this app's
// narration language. Verified directly against the ElevenLabs API (usable
// for TTS generation, even though they're shared-library voices not visible
// via the account's own /v1/voices listing).
var availableNarratorVoices = []fiber.Map{
	{"voice_id": "JcJSwKJ3l1HpINgnIA4X", "name": "Eric - Narrador Profissional", "gender": "male"},
	{"voice_id": "CstacWqMhJQlnfLPxRG4", "name": "Will - Grave e Envolvente", "gender": "male"},
	{"voice_id": "Qrdut83w0Cr152Yb4Xn3", "name": "Paulo - Expressivo e Confiante", "gender": "male"},
	{"voice_id": "3Je7qW9yPOhc47iG41pH", "name": "Yuri - Amigável e Caloroso", "gender": "male"},
	{"voice_id": "ohZOfA9iwlZ5nOsoY7LB", "name": "Roberta - Casual e Clara", "gender": "female"},
}

func GetNarratorVoices(c *fiber.Ctx) error {
	return c.JSON(availableNarratorVoices)
}

type SetNarratorVoiceInput struct {
	VoiceID string `json:"voice_id"`
}

func SetNarratorVoice(c *fiber.Ctx) error {
	channel, ok := ownedChannel(c, c.Params("id"))
	if !ok {
		return nil
	}

	var input SetNarratorVoiceInput
	if err := c.BodyParser(&input); err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "Invalid request body"})
	}

	if input.VoiceID != "" {
		valid := false
		for _, v := range availableNarratorVoices {
			if v["voice_id"] == input.VoiceID {
				valid = true
				break
			}
		}
		if !valid {
			return c.Status(400).JSON(fiber.Map{"error": "Unknown voice_id"})
		}
	}

	channel.NarratorVoiceID = input.VoiceID
	channel.UpdatedAt = time.Now()
	db.DB.Save(&channel)

	setConnectionFlags(&channel)
	return c.JSON(channel)
}

func GetChannelAnalytics(c *fiber.Ctx) error {
	if _, ok := ownedChannel(c, c.Params("id")); !ok {
		return nil
	}
	id := c.Params("id")

	var videos []models.Video
	if err := db.DB.Preload("Analytics").Find(&videos, "channel_id = ?", id).Error; err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}

	var totalViews int64 = 0
	var totalCTR float64 = 0
	var totalRetention float64 = 0
	var count int = 0

	for _, v := range videos {
		if v.Analytics != nil {
			totalViews += v.Analytics.Views
			totalCTR += v.Analytics.CTR
			totalRetention += v.Analytics.RetentionRate
			count++
		}
	}

	avgCTR := 0.0
	avgRetention := 0.0
	if count > 0 {
		avgCTR = totalCTR / float64(count)
		avgRetention = totalRetention / float64(count)
	}

	return c.JSON(fiber.Map{
		"total_videos":   len(videos),
		"total_views":    totalViews,
		"avg_ctr":        avgCTR,
		"avg_retention":  avgRetention,
		"analyzed_count": count,
		"videos":         videos,
	})
}

// ---------------------------------------------------------------------------
// Cost dashboard
// ---------------------------------------------------------------------------

type providerCostRow struct {
	Provider       string  `json:"provider"`
	Operation      string  `json:"operation"`
	CallCount      int64   `json:"call_count"`
	TotalUnits     float64 `json:"total_units"`
	TotalCostUSD   float64 `json:"total_cost_usd"`
}

type videoCostRow struct {
	VideoID      string  `json:"video_id"`
	Title        string  `json:"title"`
	TotalCostUSD float64 `json:"total_cost_usd"`
}

type dailyCostRow struct {
	Day          string  `json:"day"`
	Provider     string  `json:"provider"`
	CallCount    int64   `json:"call_count"`
	TotalCostUSD float64 `json:"total_cost_usd"`
}

// GetChannelCosts breaks down real (not simulated) API spend for a channel:
// total, per-provider/operation, per-video, and per-provider-per-day. Units
// (tokens/characters/images) are exact, taken from each provider's own
// response; the $ figures are estimates from a documented rate table - see
// services/costs.go.
func GetChannelCosts(c *fiber.Ctx) error {
	channel, ok := ownedChannel(c, c.Params("id"))
	if !ok {
		return nil
	}

	var totalCost float64
	db.DB.Model(&models.ApiUsageLog{}).Where("channel_id = ?", channel.ID).
		Select("COALESCE(SUM(estimated_cost_usd), 0)").Row().Scan(&totalCost)

	var byProvider []providerCostRow
	db.DB.Model(&models.ApiUsageLog{}).
		Select("provider, operation, COUNT(*) as call_count, COALESCE(SUM(units),0) as total_units, COALESCE(SUM(estimated_cost_usd),0) as total_cost_usd").
		Where("channel_id = ?", channel.ID).
		Group("provider, operation").
		Order("total_cost_usd desc").
		Scan(&byProvider)

	var byVideo []videoCostRow
	db.DB.Table("api_usage_logs").
		Select("api_usage_logs.video_id as video_id, videos.title as title, COALESCE(SUM(api_usage_logs.estimated_cost_usd),0) as total_cost_usd").
		Joins("LEFT JOIN videos ON videos.id = api_usage_logs.video_id").
		Where("api_usage_logs.channel_id = ? AND api_usage_logs.video_id <> ''", channel.ID).
		Group("api_usage_logs.video_id, videos.title").
		Order("total_cost_usd desc").
		Scan(&byVideo)

	thirtyDaysAgo := time.Now().AddDate(0, 0, -30)
	var daily []dailyCostRow
	db.DB.Model(&models.ApiUsageLog{}).
		Select("DATE(created_at) as day, provider, COUNT(*) as call_count, COALESCE(SUM(estimated_cost_usd),0) as total_cost_usd").
		Where("channel_id = ? AND created_at >= ?", channel.ID, thirtyDaysAgo).
		Group("DATE(created_at), provider").
		Order("day desc").
		Scan(&daily)

	// Gemini free-tier daily quota is 20 requests/day - surface today's usage
	// directly since we just hit this limit in testing and it silently forces
	// a fallback to OpenAI (which does cost money) once exhausted.
	var geminiRequestsToday int64
	todayStart := time.Now().Truncate(24 * time.Hour)
	db.DB.Model(&models.ApiUsageLog{}).
		Where("channel_id = ? AND provider = 'gemini' AND created_at >= ?", channel.ID, todayStart).
		Count(&geminiRequestsToday)

	return c.JSON(fiber.Map{
		"total_cost_usd":         totalCost,
		"by_provider_operation":  byProvider,
		"by_video":               byVideo,
		"daily_last_30_days":     daily,
		"gemini_free_tier": fiber.Map{
			"requests_today": geminiRequestsToday,
			"daily_limit":    1500,
			"limit_reached":  geminiRequestsToday >= 1500,
		},
	})
}

// ---------------------------------------------------------------------------
// OAuth linking (public - hit via plain browser navigation, see SetupRoutes)
// ---------------------------------------------------------------------------

func AuthYouTube(c *fiber.Ctx) error {
	if !isConfigured(config.AppConfig.YouTubeClientID) {
		return c.Status(400).SendString("<html><body style='font-family:sans-serif; background:#0a0b0e; color:#fff; text-align:center; padding:3rem;'><h2>YouTube OAuth não configurado!</h2><p>Por favor, insira o <b>YOUTUBE_CLIENT_ID</b> e <b>YOUTUBE_CLIENT_SECRET</b> no arquivo <code>backend/.env</code> antes de vincular sua conta do YouTube.</p></body></html>")
	}
	id := c.Params("id")
	authURL := services.GetYouTubeAuthURL(id)
	return c.Redirect(authURL)
}

func AuthTikTok(c *fiber.Ctx) error {
	if !isConfigured(config.AppConfig.TikTokClientID) {
		return c.Status(400).SendString("<html><body style='font-family:sans-serif; background:#0a0b0e; color:#fff; text-align:center; padding:3rem;'><h2>TikTok OAuth não configurado!</h2><p>Por favor, insira a sua <b>TIKTOK_CLIENT_ID</b> (Client Key) e <b>TIKTOK_CLIENT_SECRET</b> do TikTok for Developers no arquivo <code>backend/.env</code> antes de conectar.</p></body></html>")
	}
	id := c.Params("id")
	authURL := services.GetTikTokAuthURL(id)
	return c.Redirect(authURL)
}

func OAuthCallback(c *fiber.Ctx) error {
	code := c.Query("code")
	state := c.Query("state") // e.g. "youtube:channel-uuid" or "tiktok:channel-uuid"

	if code == "" || state == "" {
		return c.Status(400).SendString("Missing code or state parameters")
	}

	parts := strings.Split(state, ":")
	if len(parts) < 2 {
		return c.Status(400).SendString("Invalid state parameter format")
	}

	provider := parts[0]
	channelID := parts[1]

	var channel models.Channel
	if err := db.DB.First(&channel, "id = ?", channelID).Error; err != nil {
		return c.Status(404).SendString("Channel not found")
	}

	if provider == "youtube" {
		tokenResp, err := services.ExchangeYouTubeCode(code)
		if err != nil {
			return c.Status(500).SendString("YouTube token exchange failed: " + err.Error())
		}
		channel.YTAccessToken = tokenResp.AccessToken
		if tokenResp.RefreshToken != "" {
			channel.YTRefreshToken = tokenResp.RefreshToken
		}
		channel.YTTokenExpiry = time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)
		db.DB.Save(&channel)
		return c.SendString("<html><body style='font-family:sans-serif; background:#0a0b0e; color:#fff; text-align:center; padding:3rem;'><h2>Conta do YouTube vinculada com sucesso ao canal " + channel.Name + "!</h2><p>Você pode fechar esta janela.</p><script>setTimeout(() => window.close(), 2500);</script></body></html>")
	} else if provider == "tiktok" {
		tokenResp, err := services.ExchangeTikTokCode(code)
		if err != nil {
			return c.Status(500).SendString("TikTok token exchange failed: " + err.Error())
		}
		channel.TikTokAccessToken = tokenResp.AccessToken
		if tokenResp.RefreshToken != "" {
			channel.TikTokRefreshToken = tokenResp.RefreshToken
		}
		channel.TikTokTokenExpiry = time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)
		db.DB.Save(&channel)
		return c.SendString("<html><body style='font-family:sans-serif; background:#0a0b0e; color:#fff; text-align:center; padding:3rem;'><h2>Conta do TikTok vinculada com sucesso ao canal " + channel.Name + "!</h2><p>Você pode fechar esta janela.</p><script>setTimeout(() => window.close(), 2500);</script></body></html>")
	}

	return c.Status(400).SendString("Unknown provider")
}

func GetTrends(c *fiber.Ctx) error {
	niche := c.Query("niche", "Curiosidades")
	trends, err := services.FetchRealTimeTrends("", niche)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	return c.JSON(fiber.Map{
		"niche":  niche,
		"trends": trends,
	})
}

// ---------------------------------------------------------------------------
// Settings (operator-level API keys, overrides .env at runtime)
// ---------------------------------------------------------------------------

// isConfigured treats unset AND the "your_x_here" scaffold placeholders (still
// sitting in .env.example / a fresh .env) as not configured.
func isConfigured(value string) bool {
	return value != "" && !strings.HasPrefix(value, "your_")
}

func GetSettings(c *fiber.Ctx) error {
	var s models.AppSettings
	db.DB.FirstOrCreate(&s, models.AppSettings{ID: models.SettingsRowID})

	// Never echo raw secrets back - only whether a value is currently configured.
	return c.JSON(fiber.Map{
		"openai_configured":     isConfigured(config.AppConfig.OpenAIAPIKey),
		"gemini_configured":     isConfigured(config.AppConfig.GeminiAPIKey),
		"elevenlabs_configured": isConfigured(config.AppConfig.ElevenLabsAPIKey),
		"youtube_configured":    isConfigured(config.AppConfig.YouTubeClientID),
		"tiktok_configured":     isConfigured(config.AppConfig.TikTokClientID),
	})
}

type UpdateSettingsInput struct {
	OpenAIAPIKey     string `json:"openai_api_key"`
	GeminiAPIKey     string `json:"gemini_api_key"`
	ElevenLabsAPIKey string `json:"elevenlabs_api_key"`
	YouTubeClientID  string `json:"youtube_client_id"`
	YouTubeClientSec string `json:"youtube_client_secret"`
	TikTokClientID   string `json:"tiktok_client_id"`
	TikTokClientSec  string `json:"tiktok_client_secret"`
}

func UpdateSettings(c *fiber.Ctx) error {
	var input UpdateSettingsInput
	if err := c.BodyParser(&input); err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "Invalid request body"})
	}

	var s models.AppSettings
	db.DB.FirstOrCreate(&s, models.AppSettings{ID: models.SettingsRowID})

	// Only overwrite fields the operator actually filled in - blank inputs keep the current value.
	if input.OpenAIAPIKey != "" {
		s.OpenAIAPIKey = input.OpenAIAPIKey
	}
	if input.GeminiAPIKey != "" {
		s.GeminiAPIKey = input.GeminiAPIKey
	}
	if input.ElevenLabsAPIKey != "" {
		s.ElevenLabsAPIKey = input.ElevenLabsAPIKey
	}
	if input.YouTubeClientID != "" {
		s.YouTubeClientID = input.YouTubeClientID
	}
	if input.YouTubeClientSec != "" {
		s.YouTubeClientSec = input.YouTubeClientSec
	}
	if input.TikTokClientID != "" {
		s.TikTokClientID = input.TikTokClientID
	}
	if input.TikTokClientSec != "" {
		s.TikTokClientSec = input.TikTokClientSec
	}
	s.UpdatedAt = time.Now()

	if err := db.DB.Save(&s).Error; err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}

	config.ApplySettingsOverride(s.OpenAIAPIKey, s.GeminiAPIKey, s.ElevenLabsAPIKey,
		s.YouTubeClientID, s.YouTubeClientSec, s.TikTokClientID, s.TikTokClientSec)

	return c.JSON(fiber.Map{"status": "saved"})
}
