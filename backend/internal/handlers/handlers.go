package handlers

import (
	"errors"
	"fmt"
	"html"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"gorm.io/gorm"
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
	api.Get("/auth/config", AuthConfig)
	api.Post("/auth/register", Register)
	api.Post("/auth/login", Login)

	// The OAuth callback is hit by Google/TikTok redirecting the browser, so
	// it can't carry an Authorization header. It authenticates the flow via
	// the HMAC-signed state minted by the protected StartOAuth endpoint.
	api.Get("/oauth/callback", OAuthCallback)

	// Everything else requires a valid Bearer token
	protected := api.Group("", auth.RequireAuth)

	protected.Put("/auth/password", ChangePassword)

	protected.Get("/channels", ListChannels)
	protected.Post("/channels", CreateChannel)
	protected.Get("/channels/:id", GetChannel)
	protected.Delete("/channels/:id", DeleteChannel)
	protected.Post("/channels/:id/generate-branding", GenerateBranding)
	protected.Post("/channels/:id/generate-logo", GenerateLogo)
	protected.Post("/channels/:id/generate-banner", GenerateBanner)
	protected.Post("/channels/:id/generate-intro", GenerateIntro)
	protected.Post("/channels/:id/generate-outro", GenerateOutro)
	protected.Delete("/channels/:id/intro", DeleteIntro)
	protected.Delete("/channels/:id/outro", DeleteOutro)
	protected.Post("/channels/:id/toggle-autopilot", ToggleAutoPilot)
	protected.Get("/channels/:id/autopilot-status", GetAutoPilotStatus)
	protected.Get("/channels/:id/oauth/:provider", StartOAuth)
	protected.Delete("/channels/:id/oauth/:provider", DisconnectOAuth)
	protected.Get("/narrator-voices", GetNarratorVoices)
	protected.Put("/channels/:id/narrator-voice", SetNarratorVoice)
	protected.Get("/channels/:id/analytics", GetChannelAnalytics)
	protected.Get("/channels/:id/costs", GetChannelCosts)

	protected.Get("/trends", GetTrends)
	protected.Get("/calendar", GetCalendar)

	protected.Get("/channels/:channelId/videos", ListVideos)
	protected.Post("/channels/:channelId/videos", CreateVideo)
	protected.Get("/videos/:id", GetVideo)
	protected.Post("/videos/:id/render", TriggerRender)
	protected.Post("/videos/:id/schedule", ScheduleVideo)
	protected.Post("/videos/:id/unschedule", UnscheduleVideo)
	protected.Delete("/videos/:id", DeleteVideo)

	protected.Get("/settings", GetSettings)
	protected.Put("/settings", UpdateSettings)
	protected.Get("/settings/test/:provider", TestAPIProvider)
}

// currentUserID reads the authenticated user id stashed by auth.RequireAuth.
func currentUserID(c *fiber.Ctx) string {
	uid, _ := c.Locals("user_id").(string)
	return uid
}

func jsonError(c *fiber.Ctx, status int, msg string) error {
	return c.Status(status).JSON(fiber.Map{"error": msg})
}

// setConnectionFlags derives the public yt_connected/tiktok_connected booleans
// from the (non-serialized) OAuth tokens so the frontend never receives raw secrets.
func setConnectionFlags(ch *models.Channel) {
	ch.YTConnected = ch.YTRefreshToken != ""
	ch.TikTokConnected = ch.TikTokRefreshToken != ""
}

// updateChannel writes only the given columns and returns the fresh row.
// Handlers used to db.Save() a channel copy loaded before a 30-90s image or
// LLM call, silently reverting anything that changed in between (bookend
// progress, a toggled auto-pilot, freshly linked OAuth tokens...).
func updateChannel(c *fiber.Ctx, channelID string, fields map[string]interface{}) error {
	fields["updated_at"] = time.Now()
	if err := db.DB.Model(&models.Channel{}).Where("id = ?", channelID).Updates(fields).Error; err != nil {
		return jsonError(c, 500, err.Error())
	}
	return respondChannel(c, channelID)
}

func respondChannel(c *fiber.Ctx, channelID string) error {
	var channel models.Channel
	if err := db.DB.First(&channel, "id = ?", channelID).Error; err != nil {
		return jsonError(c, 404, "Channel not found")
	}
	setConnectionFlags(&channel)
	return c.JSON(channel)
}

// ---------------------------------------------------------------------------
// Auth
// ---------------------------------------------------------------------------

func AuthConfig(c *fiber.Ctx) error {
	return c.JSON(fiber.Map{"registration_enabled": config.AppConfig.AllowRegistration})
}

type RegisterInput struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	Name     string `json:"name"`
}

func Register(c *fiber.Ctx) error {
	if !config.AppConfig.AllowRegistration {
		return jsonError(c, 403, "Cadastro de novas contas desativado neste servidor (ALLOW_REGISTRATION).")
	}

	var input RegisterInput
	if err := c.BodyParser(&input); err != nil {
		return jsonError(c, 400, "Invalid request body")
	}

	input.Email = strings.ToLower(strings.TrimSpace(input.Email))
	if input.Email == "" || len(input.Password) < 8 {
		return jsonError(c, 400, "Email is required and password must be at least 8 characters")
	}

	var existing models.User
	if err := db.DB.First(&existing, "email = ?", input.Email).Error; err == nil {
		return jsonError(c, 409, "Email already registered")
	}

	hash, err := auth.HashPassword(input.Password)
	if err != nil {
		return jsonError(c, 500, "Failed to process password")
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
		return jsonError(c, 500, err.Error())
	}

	token, err := auth.GenerateToken(user.ID)
	if err != nil {
		return jsonError(c, 500, "Failed to generate token")
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
		return jsonError(c, 400, "Invalid request body")
	}
	input.Email = strings.ToLower(strings.TrimSpace(input.Email))

	var user models.User
	if err := db.DB.First(&user, "email = ?", input.Email).Error; err != nil {
		return jsonError(c, 401, "Invalid email or password")
	}

	if !auth.CheckPassword(user.PasswordHash, input.Password) {
		return jsonError(c, 401, "Invalid email or password")
	}

	token, err := auth.GenerateToken(user.ID)
	if err != nil {
		return jsonError(c, 500, "Failed to generate token")
	}

	return c.JSON(fiber.Map{
		"token": token,
		"user":  fiber.Map{"id": user.ID, "email": user.Email, "name": user.Name},
	})
}

type ChangePasswordInput struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

// ChangePassword exists because the seeded admin was told to "change this
// password after first login" - with no way to actually do it.
func ChangePassword(c *fiber.Ctx) error {
	var input ChangePasswordInput
	if err := c.BodyParser(&input); err != nil {
		return jsonError(c, 400, "Invalid request body")
	}
	if len(input.NewPassword) < 8 {
		return jsonError(c, 400, "A nova senha precisa ter pelo menos 8 caracteres.")
	}
	var user models.User
	if err := db.DB.First(&user, "id = ?", currentUserID(c)).Error; err != nil {
		return jsonError(c, 404, "Usuário não encontrado")
	}
	if !auth.CheckPassword(user.PasswordHash, input.CurrentPassword) {
		return jsonError(c, 401, "Senha atual incorreta.")
	}
	hash, err := auth.HashPassword(input.NewPassword)
	if err != nil {
		return jsonError(c, 500, "Failed to process password")
	}
	db.DB.Model(&models.User{}).Where("id = ?", user.ID).Updates(map[string]interface{}{
		"password_hash": hash,
		"updated_at":    time.Now(),
	})
	return c.JSON(fiber.Map{"status": "ok"})
}

// ---------------------------------------------------------------------------
// Channels
// ---------------------------------------------------------------------------

func ListChannels(c *fiber.Ctx) error {
	var channels []models.Channel
	if err := db.DB.Where("user_id = ?", currentUserID(c)).Order("created_at desc").Find(&channels).Error; err != nil {
		return jsonError(c, 500, err.Error())
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

// channelArt holds the outcome of one logo/banner generation.
type channelArt struct {
	path string
	err  error
}

// generateChannelArt renders the requested logo/banner in parallel (each is
// a 30-90s OpenAI call; sequentially, channel creation took minutes).
func generateChannelArt(channelID, name, niche, concept string, branding *services.GenerateBrandingResult, wantLogo, wantBanner bool) (logo, banner channelArt) {
	logoIdea, bannerIdea := "", ""
	if branding != nil {
		logoIdea, bannerIdea = branding.LogoPrompt, branding.BannerPrompt
	}
	var wg sync.WaitGroup
	if wantLogo {
		wg.Add(1)
		go func() {
			defer wg.Done()
			logo.path, logo.err = services.GenerateImage(channelID, "", services.BuildLogoPrompt(name, niche, concept, logoIdea), "logo")
		}()
	}
	if wantBanner {
		wg.Add(1)
		go func() {
			defer wg.Done()
			banner.path, banner.err = services.GenerateImage(channelID, "", services.BuildBannerPrompt(name, niche, concept, bannerIdea), "banner")
		}()
	}
	wg.Wait()
	return logo, banner
}

func CreateChannel(c *fiber.Ctx) error {
	var input CreateChannelInput
	if err := c.BodyParser(&input); err != nil {
		return jsonError(c, 400, "Invalid request body")
	}

	input.Niche = strings.TrimSpace(input.Niche)
	input.Name = strings.TrimSpace(input.Name)
	if input.Niche == "" {
		return jsonError(c, 400, "Niche is required")
	}

	channelID := uuid.New().String()

	// 1. Identity via LLM
	branding, err := services.GenerateBranding(channelID, input.Niche)
	if err != nil {
		log.Printf("Branding warning: %v", err)
	}

	channelName := input.Name
	if channelName == "" && branding != nil && strings.TrimSpace(branding.Name) != "" {
		channelName = strings.TrimSpace(branding.Name)
	}
	if channelName == "" {
		channelName = fmt.Sprintf("Império do %s", input.Niche)
	}

	concept := fmt.Sprintf("Canal dark focado em produzir conteúdos de alto engajamento em %s.", input.Niche)
	persona := "Investigador Enigmático"
	voiceType := "Onyx (Masculina Grave)"
	narrationTone := "Dramático e Pausado"
	if branding != nil {
		if branding.Concept != "" {
			concept = branding.Concept
		}
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

	// 2. Logo + banner (in parallel)
	log.Printf("Generating logo and banner for channel '%s'...", channelName)
	logo, banner := generateChannelArt(channelID, channelName, input.Niche, concept, branding, true, true)
	if logo.err != nil {
		log.Printf("Logo generation error: %v", logo.err)
	}
	if banner.err != nil {
		log.Printf("Banner generation error: %v", banner.err)
	}

	channel := models.Channel{
		ID:            channelID,
		UserID:        currentUserID(c),
		Name:          channelName,
		Niche:         input.Niche,
		Concept:       concept,
		Persona:       persona,
		VoiceType:     voiceType,
		NarrationTone: narrationTone,
		LogoPath:      logo.path,
		BannerPath:    banner.path,
		// Auto-Pilot starts on for "zero-thinking" automation, but it now
		// produces nothing until a YouTube/TikTok account is linked (see
		// processAutoPilotChannel), so a new channel can't burn API credits
		// on videos that have nowhere to go.
		AutoPilotEnabled: true,
		// The intro/outro are generated in the background right below; mark
		// them up front so the UI shows (and polls) the progress immediately.
		IntroStatus: "generating",
		IntroStage:  "Na fila...",
		OutroStatus: "generating",
		OutroStage:  "Aguardando a abertura...",
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}

	if err := db.DB.Create(&channel).Error; err != nil {
		return jsonError(c, 500, err.Error())
	}

	// Default Auto-Pilot config so the engine has real videos-per-week /
	// preferred-hour limits to respect instead of posting unbounded.
	autoPilotConfig := models.AutoPilotConfig{ID: uuid.New().String(), ChannelID: channel.ID}
	if err := db.DB.Create(&autoPilotConfig).Error; err != nil {
		log.Printf("Warning: failed to create default AutoPilotConfig for channel %s: %v", channel.ID, err)
	}

	log.Printf("Channel '%s' created (logo=%q banner=%q)", channelName, logo.path, banner.path)

	// Generate the channel's intro/outro clips once in the background - every
	// video rendered from now on picks them up automatically (CompileVideo).
	go services.GenerateChannelBookends(channel.ID)

	setConnectionFlags(&channel)
	resp := fiber.Map{"channel": channel}
	var warnings []string
	if logo.err != nil {
		warnings = append(warnings, "Logo: "+logo.err.Error())
	}
	if banner.err != nil {
		warnings = append(warnings, "Capa: "+banner.err.Error())
	}
	if len(warnings) > 0 {
		resp["warning"] = strings.Join(warnings, " | ")
	}
	return c.Status(201).JSON(resp)
}

func GetChannel(c *fiber.Ctx) error {
	channel, ok := ownedChannel(c, c.Params("id"))
	if !ok {
		return nil
	}
	setConnectionFlags(&channel)
	return c.JSON(channel)
}

// DeleteChannel removes a channel and everything it owns: videos, scenes,
// analytics, auto-pilot config, cost log rows and every stored file. There
// was no way to delete a channel at all before.
func DeleteChannel(c *fiber.Ctx) error {
	channel, ok := ownedChannel(c, c.Params("id"))
	if !ok {
		return nil
	}

	var busy int64
	db.DB.Model(&models.Video{}).Where("channel_id = ? AND status IN ?", channel.ID,
		[]string{models.VideoStatusGenerating, models.VideoStatusPosting}).Count(&busy)
	if busy > 0 || channel.IntroStatus == "generating" || channel.OutroStatus == "generating" {
		return jsonError(c, 409, "Há gerações ou publicações em andamento neste canal - aguarde terminarem para excluí-lo.")
	}

	// Stop the auto-pilot first so no new cycle starts mid-delete.
	db.DB.Model(&models.Channel{}).Where("id = ?", channel.ID).Update("auto_pilot_enabled", false)

	var videos []models.Video
	db.DB.Preload("Scenes").Where("channel_id = ?", channel.ID).Find(&videos)
	for i := range videos {
		deleteVideoFiles(&videos[i])
		db.DB.Where("video_id = ?", videos[i].ID).Delete(&models.Scene{})
		db.DB.Where("video_id = ?", videos[i].ID).Delete(&models.AnalyticsData{})
	}
	db.DB.Where("channel_id = ?", channel.ID).Delete(&models.Video{})

	for _, p := range []string{channel.LogoPath, channel.BannerPath, channel.IntroVideoPath, channel.OutroVideoPath} {
		if err := services.DeleteMedia(p); err != nil {
			log.Printf("Warning: failed to delete channel asset %s: %v", p, err)
		}
	}
	db.DB.Where("channel_id = ?", channel.ID).Delete(&models.AutoPilotConfig{})
	db.DB.Where("channel_id = ?", channel.ID).Delete(&models.ApiUsageLog{})
	if err := db.DB.Delete(&models.Channel{}, "id = ?", channel.ID).Error; err != nil {
		return jsonError(c, 500, err.Error())
	}
	return c.JSON(fiber.Map{"status": "deleted", "channel_id": channel.ID})
}

// GetAutoPilotStatus surfaces the Auto-Pilot backoff/failure/skip state that
// previously existed only in server logs nobody reads.
func GetAutoPilotStatus(c *fiber.Ctx) error {
	channel, ok := ownedChannel(c, c.Params("id"))
	if !ok {
		return nil
	}

	canPublish := services.ChannelCanPublish(&channel)
	var cfg models.AutoPilotConfig
	if err := db.DB.Where("channel_id = ?", channel.ID).First(&cfg).Error; err != nil {
		return c.JSON(fiber.Map{"consecutive_failures": 0, "can_publish": canPublish})
	}

	return c.JSON(fiber.Map{
		"videos_per_week":      cfg.VideosPerWeek,
		"preferred_post_hour":  cfg.PreferredPostHour,
		"current_strategy":     cfg.CurrentStrategy,
		"last_optimization_at": cfg.LastOptimizationAt,
		"consecutive_failures": cfg.ConsecutiveFailures,
		"last_failure_at":      cfg.LastFailureAt,
		"last_failure_reason":  cfg.LastFailureReason,
		"last_skip_reason":     cfg.LastSkipReason,
		"last_checked_at":      cfg.LastCheckedAt,
		"can_publish":          canPublish,
	})
}

// GenerateBranding (re)generates ONLY the logo/banner images for an existing
// channel - it must never touch Name/Concept/Persona.
func GenerateBranding(c *fiber.Ctx) error {
	return regenerateArt(c, true, true)
}

func GenerateLogo(c *fiber.Ctx) error {
	return regenerateArt(c, true, false)
}

func GenerateBanner(c *fiber.Ctx) error {
	return regenerateArt(c, false, true)
}

func regenerateArt(c *fiber.Ctx, wantLogo, wantBanner bool) error {
	channel, ok := ownedChannel(c, c.Params("id"))
	if !ok {
		return nil
	}

	// A fresh visual idea per regeneration (otherwise "Regerar" keeps
	// producing near-identical images). Failure here is fine - the prompt
	// builders have niche-based defaults.
	branding, err := services.GenerateBranding(channel.ID, channel.Niche)
	if err != nil {
		log.Printf("Branding idea warning for channel %s: %v", channel.ID, err)
	}

	logo, banner := generateChannelArt(channel.ID, channel.Name, channel.Niche, channel.Concept, branding, wantLogo, wantBanner)

	fields := map[string]interface{}{}
	var errs []string
	if wantLogo {
		if logo.err != nil {
			errs = append(errs, "logo: "+logo.err.Error())
		} else {
			fields["logo_path"] = logo.path
			if channel.LogoPath != "" && channel.LogoPath != logo.path {
				_ = services.DeleteMedia(channel.LogoPath)
			}
		}
	}
	if wantBanner {
		if banner.err != nil {
			errs = append(errs, "capa: "+banner.err.Error())
		} else {
			fields["banner_path"] = banner.path
			if channel.BannerPath != "" && channel.BannerPath != banner.path {
				_ = services.DeleteMedia(channel.BannerPath)
			}
		}
	}
	if len(fields) == 0 {
		return jsonError(c, 502, "Falha ao gerar imagem - "+strings.Join(errs, " | "))
	}
	return updateChannel(c, channel.ID, fields)
}

type BookendInput struct {
	Script string `json:"script"`
}

func GenerateIntro(c *fiber.Ctx) error {
	return startBookend(c, "intro")
}

func GenerateOutro(c *fiber.Ctx) error {
	return startBookend(c, "outro")
}

func startBookend(c *fiber.Ctx, kind string) error {
	channel, ok := ownedChannel(c, c.Params("id"))
	if !ok {
		return nil
	}

	var input BookendInput
	_ = c.BodyParser(&input)

	// Atomic claim: two clicks used to start two ffmpeg renders writing the
	// same output file at once.
	col := kind + "_status"
	label := "abertura"
	if kind == "outro" {
		label = "encerramento"
	}
	res := db.DB.Model(&models.Channel{}).
		Where("id = ? AND ("+col+" IS NULL OR "+col+" <> ?)", channel.ID, "generating").
		Updates(map[string]interface{}{
			col:                "generating",
			kind + "_progress": 10,
			kind + "_stage":    "Iniciando geração do(a) " + label + "...",
			kind + "_error":    "",
			"updated_at":       time.Now(),
		})
	if res.Error != nil {
		return jsonError(c, 500, res.Error.Error())
	}
	if res.RowsAffected == 0 {
		return jsonError(c, 409, "A geração do(a) "+label+" já está em andamento.")
	}

	go func(chID, script string) {
		var err error
		if kind == "intro" {
			err = services.GenerateIntro(chID, script)
		} else {
			err = services.GenerateOutro(chID, script)
		}
		if err != nil {
			log.Printf("Background %s generation error for channel %s: %v", kind, chID, err)
		}
	}(channel.ID, input.Script)

	return respondChannel(c, channel.ID)
}

func DeleteIntro(c *fiber.Ctx) error {
	return deleteBookend(c, "intro")
}

func DeleteOutro(c *fiber.Ctx) error {
	return deleteBookend(c, "outro")
}

func deleteBookend(c *fiber.Ctx, kind string) error {
	channel, ok := ownedChannel(c, c.Params("id"))
	if !ok {
		return nil
	}
	var err error
	if kind == "intro" {
		err = services.DeleteIntro(channel.ID)
	} else {
		err = services.DeleteOutro(channel.ID)
	}
	if errors.Is(err, services.ErrBookendBusy) {
		return jsonError(c, 409, err.Error())
	}
	if err != nil {
		return jsonError(c, 500, err.Error())
	}
	return respondChannel(c, channel.ID)
}

// ---------------------------------------------------------------------------
// Ownership helpers
// ---------------------------------------------------------------------------

// ownedChannel loads a channel by id, scoped to the authenticated user. It
// writes a 404 response and returns ok=false when the channel doesn't exist
// or isn't owned by the caller (existence isn't leaked either way).
func ownedChannel(c *fiber.Ctx, channelID string) (channel models.Channel, ok bool) {
	if err := db.DB.First(&channel, "id = ? AND user_id = ?", channelID, currentUserID(c)).Error; err != nil {
		_ = jsonError(c, 404, "Channel not found")
		return channel, false
	}
	return channel, true
}

// ownedVideo loads a video by id and verifies its parent channel belongs to
// the authenticated user, writing a 404 response otherwise.
func ownedVideo(c *fiber.Ctx, videoID string, preloadScenes bool) (video models.Video, ok bool) {
	query := db.DB
	if preloadScenes {
		query = query.Preload("Scenes", func(tx *gorm.DB) *gorm.DB { return tx.Order("\"index\" asc") })
	}
	if err := query.First(&video, "id = ?", videoID).Error; err != nil {
		_ = jsonError(c, 404, "Video not found")
		return video, false
	}
	if _, chOk := ownedChannel(c, video.ChannelID); !chOk {
		return video, false
	}
	video.SceneCount = len(video.Scenes)
	return video, true
}

// ---------------------------------------------------------------------------
// Videos
// ---------------------------------------------------------------------------

// fillSceneCounts sets SceneCount on each video with one grouped query, so
// list endpoints no longer have to preload every scene of every video (35-55
// rows each, refetched every 4s while anything is generating).
func fillSceneCounts(videos []models.Video) {
	if len(videos) == 0 {
		return
	}
	ids := make([]string, len(videos))
	for i, v := range videos {
		ids[i] = v.ID
	}
	var rows []struct {
		VideoID string
		N       int
	}
	db.DB.Model(&models.Scene{}).Select("video_id, COUNT(*) AS n").Where("video_id IN ?", ids).Group("video_id").Scan(&rows)
	counts := make(map[string]int, len(rows))
	for _, r := range rows {
		counts[r.VideoID] = r.N
	}
	for i := range videos {
		videos[i].SceneCount = counts[videos[i].ID]
	}
}

func ListVideos(c *fiber.Ctx) error {
	channelID := c.Params("channelId")
	if _, ok := ownedChannel(c, channelID); !ok {
		return nil
	}

	var videos []models.Video
	if err := db.DB.Order("created_at desc").Find(&videos, "channel_id = ?", channelID).Error; err != nil {
		return jsonError(c, 500, err.Error())
	}
	fillSceneCounts(videos)
	return c.JSON(videos)
}

type calendarEntry struct {
	ID            string     `json:"id"`
	ChannelID     string     `json:"channel_id"`
	ChannelName   string     `json:"channel_name"`
	Title         string     `json:"title"`
	Status        string     `json:"status"`
	ScheduledFor  *time.Time `json:"scheduled_for,omitempty"`
	PostedAt      *time.Time `json:"posted_at,omitempty"`
	ThumbnailPath string     `json:"thumbnail_path,omitempty"`
}

// GetCalendar returns every scheduled/posted video of the user's channels in
// [from, to) in ONE query. The calendar used to fetch the full video list
// (with every scene) of each channel, one request per channel.
func GetCalendar(c *fiber.Ctx) error {
	now := time.Now()
	from := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	to := from.AddDate(0, 1, 0)
	if v := c.Query("from"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return jsonError(c, 400, "from inválido (use RFC3339)")
		}
		from = t
	}
	if v := c.Query("to"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return jsonError(c, 400, "to inválido (use RFC3339)")
		}
		to = t
	}

	var entries []calendarEntry
	err := db.DB.Table("videos").
		Select("videos.id, videos.channel_id, channels.name AS channel_name, videos.title, videos.status, videos.scheduled_for, videos.posted_at, videos.thumbnail_path").
		Joins("JOIN channels ON channels.id = videos.channel_id").
		Where("channels.user_id = ?", currentUserID(c)).
		Where("(videos.scheduled_for >= ? AND videos.scheduled_for < ?) OR (videos.posted_at >= ? AND videos.posted_at < ?)", from, to, from, to).
		Order("COALESCE(videos.scheduled_for, videos.posted_at) asc").
		Scan(&entries).Error
	if err != nil {
		return jsonError(c, 500, err.Error())
	}
	if entries == nil {
		entries = []calendarEntry{}
	}
	return c.JSON(entries)
}

type CreateVideoInput struct {
	Topic string `json:"topic"`
}

// CreateVideo returns immediately with a placeholder "generating" video and
// does script+scene generation in the background (services.GenerateVideoScenes).
// The frontend polls GetVideo and shows render_progress/render_stage.
func CreateVideo(c *fiber.Ctx) error {
	channelID := c.Params("channelId")
	var input CreateVideoInput
	if err := c.BodyParser(&input); err != nil {
		return jsonError(c, 400, "Invalid request body")
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
		Status:         models.VideoStatusGenerating,
		RenderProgress: 0,
		RenderStage:    "Preparando...",
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}
	if err := db.DB.Create(&video).Error; err != nil {
		return jsonError(c, 500, err.Error())
	}

	go services.GenerateVideoScenes(channel, videoID, strings.TrimSpace(input.Topic))

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
	video, ok := ownedVideo(c, c.Params("id"), true)
	if !ok {
		return nil
	}

	switch video.Status {
	case models.VideoStatusDraft, models.VideoStatusFailed:
	case models.VideoStatusGenerating:
		return jsonError(c, 409, "Este vídeo já está sendo gerado/renderizado.")
	default:
		return jsonError(c, 409, fmt.Sprintf("Vídeos com status %q não podem ser renderizados novamente.", video.Status))
	}

	if len(video.Scenes) == 0 {
		return jsonError(c, 409, "Este vídeo não tem cenas - a geração do roteiro falhou. Exclua-o e gere um novo.")
	}
	// After a successful render the scene source files are purged to save
	// storage - re-rendering from empty scenes would produce a video made
	// entirely of fallback silence/stock clips.
	hasAssets := false
	for _, s := range video.Scenes {
		if s.ImagePath != "" || s.VideoClipURL != "" || s.AudioPath != "" {
			hasAssets = true
			break
		}
	}
	if !hasAssets {
		return jsonError(c, 409, "Os arquivos das cenas já foram removidos após a renderização anterior - gere um novo vídeo.")
	}

	// Atomic claim so a double click can't start two renders of one video.
	res := db.DB.Model(&models.Video{}).
		Where("id = ? AND status IN ?", video.ID, []string{models.VideoStatusDraft, models.VideoStatusFailed}).
		Updates(map[string]interface{}{
			"status":          models.VideoStatusGenerating,
			"render_progress": 0,
			"render_stage":    "Na fila de renderização...",
			"error_message":   "",
		})
	if res.Error != nil {
		return jsonError(c, 500, res.Error.Error())
	}
	if res.RowsAffected == 0 {
		return jsonError(c, 409, "Este vídeo já está sendo renderizado.")
	}

	go services.CompileVideo(video.ID)

	return c.JSON(fiber.Map{"status": "queued", "message": "Video rendering pipeline started"})
}

type ScheduleVideoInput struct {
	ScheduledFor string `json:"scheduled_for"` // ISO string
}

func ScheduleVideo(c *fiber.Ctx) error {
	var input ScheduleVideoInput
	if err := c.BodyParser(&input); err != nil {
		return jsonError(c, 400, "Invalid request body")
	}

	scheduledTime, err := time.Parse(time.RFC3339, input.ScheduledFor)
	if err != nil {
		return jsonError(c, 400, "Invalid date format, use RFC3339 (e.g. 2026-08-10T15:00:00Z)")
	}
	if scheduledTime.Before(time.Now().Add(-5 * time.Minute)) {
		return jsonError(c, 400, "A data escolhida já passou - escolha um horário no futuro.")
	}

	video, ok := ownedVideo(c, c.Params("id"), false)
	if !ok {
		return nil
	}

	if video.Status != models.VideoStatusReady && video.Status != models.VideoStatusScheduled {
		return jsonError(c, 400, "Apenas vídeos prontos (renderizados) podem ser agendados.")
	}
	if video.VideoPath == "" {
		return jsonError(c, 400, "Este vídeo não tem arquivo renderizado para publicar.")
	}

	var channel models.Channel
	db.DB.First(&channel, "id = ?", video.ChannelID)
	if !services.ChannelCanPublish(&channel) {
		return jsonError(c, 400, "Conecte o YouTube ou o TikTok deste canal antes de agendar - sem conta vinculada a publicação falharia.")
	}

	res := db.DB.Model(&models.Video{}).
		Where("id = ? AND status IN ?", video.ID, []string{models.VideoStatusReady, models.VideoStatusScheduled}).
		Updates(map[string]interface{}{
			"status":        models.VideoStatusScheduled,
			"scheduled_for": &scheduledTime,
			"error_message": "",
		})
	if res.Error != nil {
		return jsonError(c, 500, res.Error.Error())
	}
	if res.RowsAffected == 0 {
		return jsonError(c, 409, "O status do vídeo mudou (talvez já esteja sendo publicado) - recarregue a página.")
	}

	video, _ = ownedVideo(c, video.ID, true)
	return c.JSON(video)
}

// UnscheduleVideo cancels a pending post (there was no way to undo one).
func UnscheduleVideo(c *fiber.Ctx) error {
	video, ok := ownedVideo(c, c.Params("id"), false)
	if !ok {
		return nil
	}
	res := db.DB.Model(&models.Video{}).
		Where("id = ? AND status = ?", video.ID, models.VideoStatusScheduled).
		Updates(map[string]interface{}{"status": models.VideoStatusReady, "scheduled_for": nil})
	if res.Error != nil {
		return jsonError(c, 500, res.Error.Error())
	}
	if res.RowsAffected == 0 {
		return jsonError(c, 409, "Este vídeo não está agendado (ou já está sendo publicado).")
	}
	video, _ = ownedVideo(c, video.ID, true)
	return c.JSON(video)
}

// deleteVideoFiles removes every stored file of a video (renders, thumbnail,
// scene images/audio/clips) from R2 or local disk.
func deleteVideoFiles(video *models.Video) {
	paths := []string{video.VideoPath, video.ShortsPath, video.SubtitlesPath, video.ThumbnailPath}
	for _, s := range video.Scenes {
		paths = append(paths, s.ImagePath, s.AudioPath, s.VideoClipURL)
	}
	for _, p := range paths {
		if p == "" {
			continue
		}
		if err := services.DeleteMedia(p); err != nil {
			log.Printf("Warning: failed to delete %s for video %s: %v", p, video.ID, err)
		}
	}
}

// DeleteVideo wipes a video's files and its rows. Refused while a background
// job is working on it: the job's final save would otherwise re-create the
// row (GORM's Save upserts) - a "deleted" video coming back from the dead.
func DeleteVideo(c *fiber.Ctx) error {
	video, ok := ownedVideo(c, c.Params("id"), true)
	if !ok {
		return nil
	}
	if video.Status == models.VideoStatusGenerating || video.Status == models.VideoStatusPosting {
		return jsonError(c, 409, "Aguarde a geração/publicação em andamento terminar para excluir este vídeo.")
	}

	deleteVideoFiles(&video)
	db.DB.Where("video_id = ?", video.ID).Delete(&models.Scene{})
	db.DB.Where("video_id = ?", video.ID).Delete(&models.AnalyticsData{})
	if err := db.DB.Delete(&models.Video{}, "id = ?", video.ID).Error; err != nil {
		return jsonError(c, 500, err.Error())
	}

	return c.JSON(fiber.Map{"status": "deleted", "video_id": video.ID})
}

func ToggleAutoPilot(c *fiber.Ctx) error {
	channel, ok := ownedChannel(c, c.Params("id"))
	if !ok {
		return nil
	}

	enabled := !channel.AutoPilotEnabled
	db.DB.Model(&models.Channel{}).Where("id = ?", channel.ID).Updates(map[string]interface{}{
		"auto_pilot_enabled": enabled,
		"updated_at":         time.Now(),
	})

	return c.JSON(fiber.Map{
		"channel_id":        channel.ID,
		"autopilot_enabled": enabled,
		"can_publish":       services.ChannelCanPublish(&channel),
	})
}

// availableNarratorVoices is the fixed list of ElevenLabs voices the channel
// narrator picker offers - all Brazilian Portuguese, matching this app's
// narration language.
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
		return jsonError(c, 400, "Invalid request body")
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
			return jsonError(c, 400, "Unknown voice_id")
		}
	}

	return updateChannel(c, channel.ID, map[string]interface{}{"narrator_voice_id": input.VoiceID})
}

func GetChannelAnalytics(c *fiber.Ctx) error {
	channel, ok := ownedChannel(c, c.Params("id"))
	if !ok {
		return nil
	}

	var videos []models.Video
	if err := db.DB.Preload("Analytics").Find(&videos, "channel_id = ?", channel.ID).Error; err != nil {
		return jsonError(c, 500, err.Error())
	}

	var totalViews int64
	var totalCTR, totalRetention float64
	count := 0
	for _, v := range videos {
		if v.Analytics != nil {
			totalViews += v.Analytics.Views
			totalCTR += v.Analytics.CTR
			totalRetention += v.Analytics.RetentionRate
			count++
		}
	}

	avgCTR, avgRetention := 0.0, 0.0
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
	Provider     string  `json:"provider"`
	Operation    string  `json:"operation"`
	CallCount    int64   `json:"call_count"`
	TotalUnits   float64 `json:"total_units"`
	TotalCostUSD float64 `json:"total_cost_usd"`
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
// are exact; the $ figures are estimates from services/costs.go.
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

	// The Gemini free-tier quota is per API key, not per channel - counting
	// only this channel's calls under-reported it whenever several channels
	// share the key (which they always do).
	var geminiRequestsToday int64
	now := time.Now()
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	db.DB.Model(&models.ApiUsageLog{}).
		Where("provider = 'gemini' AND operation = 'llm_completion' AND created_at >= ?", todayStart).
		Count(&geminiRequestsToday)
	limit := int64(config.AppConfig.GeminiFreeTierDailyLimit)

	return c.JSON(fiber.Map{
		"total_cost_usd":        totalCost,
		"by_provider_operation": byProvider,
		"by_video":              byVideo,
		"daily_last_30_days":    daily,
		"gemini_free_tier": fiber.Map{
			"configured":     config.IsSet(config.AppConfig.GeminiAPIKey),
			"requests_today": geminiRequestsToday,
			"daily_limit":    limit,
			"limit_reached":  limit > 0 && geminiRequestsToday >= limit,
		},
	})
}

// ---------------------------------------------------------------------------
// OAuth linking
// ---------------------------------------------------------------------------

// StartOAuth returns the provider's consent URL with a signed state bound to
// this user + channel. It replaces the old public GET .../auth/youtube link,
// which needed no login and produced an unsigned, forgeable state.
func StartOAuth(c *fiber.Ctx) error {
	channel, ok := ownedChannel(c, c.Params("id"))
	if !ok {
		return nil
	}
	provider := c.Params("provider")
	state := auth.SignOAuthState(auth.OAuthState{Provider: provider, ChannelID: channel.ID, UserID: currentUserID(c)})

	switch provider {
	case "youtube":
		if !config.IsSet(config.AppConfig.YouTubeClientID) || !config.IsSet(config.AppConfig.YouTubeClientSec) {
			return jsonError(c, 400, "YouTube OAuth não configurado: defina YOUTUBE_CLIENT_ID e YOUTUBE_CLIENT_SECRET (ou em Configurações).")
		}
		return c.JSON(fiber.Map{"url": services.GetYouTubeAuthURL(state)})
	case "tiktok":
		if !config.IsSet(config.AppConfig.TikTokClientID) || !config.IsSet(config.AppConfig.TikTokClientSec) {
			return jsonError(c, 400, "TikTok OAuth não configurado: defina TIKTOK_CLIENT_ID e TIKTOK_CLIENT_SECRET (ou em Configurações).")
		}
		return c.JSON(fiber.Map{"url": services.GetTikTokAuthURL(state, auth.PKCEVerifier(state))})
	}
	return jsonError(c, 400, "Provedor desconhecido: "+provider)
}

// DisconnectOAuth unlinks a posting account from the channel.
func DisconnectOAuth(c *fiber.Ctx) error {
	channel, ok := ownedChannel(c, c.Params("id"))
	if !ok {
		return nil
	}
	switch c.Params("provider") {
	case "youtube":
		return updateChannel(c, channel.ID, map[string]interface{}{"yt_access_token": "", "yt_refresh_token": "", "yt_channel_id": ""})
	case "tiktok":
		return updateChannel(c, channel.ID, map[string]interface{}{"tik_tok_access_token": "", "tik_tok_refresh_token": "", "tik_tok_account_id": ""})
	}
	return jsonError(c, 400, "Provedor desconhecido")
}

func oauthPage(c *fiber.Ctx, status int, title, body string) error {
	c.Set(fiber.HeaderContentType, fiber.MIMETextHTMLCharsetUTF8)
	ok := "false"
	if status < 300 {
		ok = "true"
	}
	return c.Status(status).SendString(`<!doctype html><html lang="pt-BR"><head><meta charset="utf-8"><title>Mákina Dark</title></head>` +
		`<body style="font-family:system-ui,sans-serif;background:#0a0b0e;color:#f4f4f5;text-align:center;padding:3rem;">` +
		`<h2>` + html.EscapeString(title) + `</h2><p style="color:#a1a1aa">` + html.EscapeString(body) + `</p>` +
		`<script>try{window.opener&&window.opener.postMessage({type:"makinadark-oauth",ok:` + ok + `},"*")}catch(e){}` +
		(map[bool]string{true: `setTimeout(function(){window.close()},2500);`, false: ``}[status < 300]) +
		`</script></body></html>`)
}

func OAuthCallback(c *fiber.Ctx) error {
	if e := c.Query("error"); e != "" {
		return oauthPage(c, 400, "Autorização cancelada", "O provedor retornou: "+e+" "+c.Query("error_description"))
	}

	code := c.Query("code")
	rawState := c.Query("state")
	if code == "" || rawState == "" {
		return oauthPage(c, 400, "Parâmetros ausentes", "A resposta do provedor não trouxe code/state.")
	}

	state, err := auth.VerifyOAuthState(rawState)
	if err != nil {
		return oauthPage(c, 400, "Link de autorização inválido", err.Error())
	}

	var channel models.Channel
	if err := db.DB.First(&channel, "id = ? AND user_id = ?", state.ChannelID, state.UserID).Error; err != nil {
		return oauthPage(c, 404, "Canal não encontrado", "O canal deste link não existe mais.")
	}

	switch state.Provider {
	case "youtube":
		tokenResp, err := services.ExchangeYouTubeCode(code)
		if err != nil {
			return oauthPage(c, 502, "Falha ao vincular o YouTube", err.Error())
		}
		fields := map[string]interface{}{
			"yt_access_token": tokenResp.AccessToken,
			"yt_token_expiry": time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second),
			"updated_at":      time.Now(),
		}
		if tokenResp.RefreshToken != "" {
			fields["yt_refresh_token"] = tokenResp.RefreshToken
		}
		db.DB.Model(&models.Channel{}).Where("id = ?", channel.ID).Updates(fields)
		return oauthPage(c, 200, "YouTube vinculado ao canal "+channel.Name+"!", "Você pode fechar esta janela.")
	case "tiktok":
		tokenResp, err := services.ExchangeTikTokCode(code, auth.PKCEVerifier(rawState))
		if err != nil {
			return oauthPage(c, 502, "Falha ao vincular o TikTok", err.Error())
		}
		fields := map[string]interface{}{
			"tik_tok_access_token": tokenResp.AccessToken,
			"tik_tok_token_expiry": time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second),
			"updated_at":           time.Now(),
		}
		if tokenResp.RefreshToken != "" {
			fields["tik_tok_refresh_token"] = tokenResp.RefreshToken
		}
		db.DB.Model(&models.Channel{}).Where("id = ?", channel.ID).Updates(fields)
		return oauthPage(c, 200, "TikTok vinculado ao canal "+channel.Name+"!", "Você pode fechar esta janela.")
	}

	return oauthPage(c, 400, "Provedor desconhecido", state.Provider)
}

func GetTrends(c *fiber.Ctx) error {
	niche := c.Query("niche", "Curiosidades")
	trends, err := services.FetchRealTimeTrends("", niche)
	if err != nil {
		return jsonError(c, 500, err.Error())
	}
	return c.JSON(fiber.Map{
		"niche":  niche,
		"trends": trends,
	})
}

// ---------------------------------------------------------------------------
// Settings (operator-level API keys, overrides .env at runtime)
// ---------------------------------------------------------------------------

func GetSettings(c *fiber.Ctx) error {
	var s models.AppSettings
	db.DB.FirstOrCreate(&s, models.AppSettings{ID: models.SettingsRowID})

	cfg := config.AppConfig
	// Never echo raw secrets back - only whether a value is currently configured.
	return c.JSON(fiber.Map{
		"openai_configured":     config.IsSet(cfg.OpenAIAPIKey),
		"gemini_configured":     config.IsSet(cfg.GeminiAPIKey),
		"elevenlabs_configured": config.IsSet(cfg.ElevenLabsAPIKey),
		"youtube_configured":    config.IsSet(cfg.YouTubeClientID) && config.IsSet(cfg.YouTubeClientSec),
		"tiktok_configured":     config.IsSet(cfg.TikTokClientID) && config.IsSet(cfg.TikTokClientSec),
		"groq_configured":       config.IsSet(cfg.GroqAPIKey),
		"claude_configured":     config.IsSet(cfg.AnthropicAPIKey),
		"pexels_configured":     config.IsSet(cfg.PexelsAPIKey),
		"shotstack_configured":  config.IsSet(cfg.ShotstackAPIKey),
		"r2_configured":         cfg.R2Bucket != "" && cfg.R2AccessKeyID != "",
		"redirect_uri":          cfg.RedirectURI,
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
		return jsonError(c, 400, "Invalid request body")
	}

	var s models.AppSettings
	db.DB.FirstOrCreate(&s, models.AppSettings{ID: models.SettingsRowID})

	// Only overwrite fields the operator actually filled in - blank inputs keep the current value.
	set := func(dst *string, v string) {
		if v = strings.TrimSpace(v); v != "" {
			*dst = v
		}
	}
	set(&s.OpenAIAPIKey, input.OpenAIAPIKey)
	set(&s.GeminiAPIKey, input.GeminiAPIKey)
	set(&s.ElevenLabsAPIKey, input.ElevenLabsAPIKey)
	set(&s.YouTubeClientID, input.YouTubeClientID)
	set(&s.YouTubeClientSec, input.YouTubeClientSec)
	set(&s.TikTokClientID, input.TikTokClientID)
	set(&s.TikTokClientSec, input.TikTokClientSec)
	s.UpdatedAt = time.Now()

	if err := db.DB.Save(&s).Error; err != nil {
		return jsonError(c, 500, err.Error())
	}

	config.ApplySettingsOverride(s.OpenAIAPIKey, s.GeminiAPIKey, s.ElevenLabsAPIKey,
		s.YouTubeClientID, s.YouTubeClientSec, s.TikTokClientID, s.TikTokClientSec)

	return c.JSON(fiber.Map{"status": "saved"})
}

// testableProviders is the whitelist of :provider values TestAPIProvider
// accepts - keeps the endpoint from being used to make the backend fire an
// arbitrary outbound request to a caller-chosen host.
var testableProviders = map[string]bool{
	"groq": true, "openai": true, "gemini": true, "claude": true,
	"elevenlabs": true, "pexels": true, "pollinations": true, "shotstack": true,
	"r2": true, "image_ranker": true, "youtube": true, "tiktok": true,
}

// TestAPIProvider makes one real, minimal-cost call to the named external
// API using whatever credentials are currently active and reports the
// provider's own error message.
func TestAPIProvider(c *fiber.Ctx) error {
	provider := c.Params("provider")
	if !testableProviders[provider] {
		return jsonError(c, 400, "provedor desconhecido: "+provider)
	}
	ok, message := services.TestAPIConnection(provider)
	return c.JSON(fiber.Map{"provider": provider, "ok": ok, "message": message})
}
