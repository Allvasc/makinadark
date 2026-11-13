package services

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"makinadark/backend/internal/config"
	"makinadark/backend/internal/db"
	"makinadark/backend/internal/models"
)

// autopilotRunning holds the IDs of channels with a cycle in flight. A full
// cycle (script + 35-55 scenes + render) routinely outlasts the 10-minute
// tick, and every tick used to start another goroutine for the same channel
// - duplicate videos and doubled API spend.
var autopilotRunning sync.Map

// autopilotMaxQueued is how many unpublished auto-pilot videos may be waiting
// (generating/scheduled) before the engine stops producing more. Without it
// the engine produced a whole week's quota in about an hour.
const autopilotMaxQueued = 2

// StartAutoPilotEngine starts the background loop that manages channels in Auto-Pilot mode
func StartAutoPilotEngine() {
	log.Println("Starting Autonomous AI Auto-Pilot Engine...")

	ticker := time.NewTicker(10 * time.Minute)
	go func() {
		for {
			select {
			case <-ticker.C:
				runAutoPilotCycle()
			}
		}
	}()
}

func runAutoPilotCycle() {
	var channels []models.Channel
	err := db.DB.Where("auto_pilot_enabled = ?", true).Find(&channels).Error
	if err != nil {
		log.Printf("AutoPilot error fetching channels: %v", err)
		return
	}

	for _, channel := range channels {
		if _, busy := autopilotRunning.LoadOrStore(channel.ID, true); busy {
			log.Printf("Auto-Pilot: channel '%s' still has a cycle in progress, skipping this tick", channel.Name)
			continue
		}
		go func(ch models.Channel) {
			defer autopilotRunning.Delete(ch.ID)
			processAutoPilotChannel(ch)
		}(channel)
	}
}

// recordAutoPilotSkip notes why a cycle deliberately did nothing. It's not a
// failure (no backoff), but it IS shown in the UI - otherwise an enabled
// auto-pilot that never produces anything looks simply broken.
func recordAutoPilotSkip(cfg *models.AutoPilotConfig, reason string) {
	now := time.Now()
	cfg.LastSkipReason = reason
	cfg.LastCheckedAt = &now
	db.DB.Model(&models.AutoPilotConfig{}).Where("id = ?", cfg.ID).Updates(map[string]interface{}{
		"last_skip_reason": reason,
		"last_checked_at":  &now,
	})
}

// nextAutoPilotSlot spreads posts evenly across the week instead of putting
// every video at "tomorrow, preferred hour". Up to 7/week: at most one per
// day, every ceil(7/n) days, always at the preferred hour. More than 7/week:
// evenly spaced hours starting from the preferred hour.
func nextAutoPilotSlot(channelID string, videosPerWeek, preferredHour int, now time.Time) time.Time {
	interval := (7 * 24 * time.Hour) / time.Duration(videosPerWeek)
	earliest := now.Add(1 * time.Hour)

	var last models.Video
	err := db.DB.Where("channel_id = ? AND scheduled_for IS NOT NULL AND status IN ?", channelID,
		[]string{models.VideoStatusScheduled, models.VideoStatusPosting, models.VideoStatusPosted}).
		Order("scheduled_for desc").First(&last).Error
	if err == nil && last.ScheduledFor != nil {
		if next := last.ScheduledFor.Add(interval); next.After(earliest) {
			earliest = next
		}
	}

	if videosPerWeek <= 7 {
		slot := time.Date(earliest.Year(), earliest.Month(), earliest.Day(), preferredHour, 0, 0, 0, earliest.Location())
		if slot.Before(earliest) {
			slot = slot.AddDate(0, 0, 1)
		}
		return slot
	}
	return earliest.Truncate(time.Hour).Add(time.Hour)
}

func processAutoPilotChannel(channel models.Channel) {
	log.Printf("Auto-Pilot checking channel '%s' (%s)...", channel.Name, channel.Niche)

	// 0. Load (or lazily create) the channel's Auto-Pilot config and respect its
	// videos-per-week cap so the engine can't generate unbounded paid API calls.
	var cfg models.AutoPilotConfig
	if err := db.DB.Where("channel_id = ?", channel.ID).First(&cfg).Error; err != nil {
		cfg = models.AutoPilotConfig{ID: uuid.New().String(), ChannelID: channel.ID}
		if err := db.DB.Create(&cfg).Error; err != nil {
			log.Printf("Auto-Pilot: failed to create default config for channel %s: %v", channel.ID, err)
		}
		db.DB.Where("channel_id = ?", channel.ID).First(&cfg) // reload to pick up DB-side column defaults
	}

	// 0b. Back off after repeated failures instead of retrying every 10
	// minutes forever - a channel stuck on e.g. a prompt that always
	// truncates was silently burning paid API calls all day with no signal
	// to the operator. Delay doubles per consecutive failure, capped at 6h.
	if cfg.ConsecutiveFailures > 0 && cfg.LastFailureAt != nil {
		delay := autopilotBackoffDelay(cfg.ConsecutiveFailures)
		if elapsed := time.Since(*cfg.LastFailureAt); elapsed < delay {
			log.Printf("Auto-Pilot: channel '%s' in backoff after %d consecutive failure(s) (last: %q) - retrying in %s",
				channel.Name, cfg.ConsecutiveFailures, cfg.LastFailureReason, (delay - elapsed).Round(time.Minute))
			return
		}
	}

	// 0c. Nothing can be published without a linked account. Producing
	// videos anyway just burned paid API calls on content that could only
	// fail at publish time.
	if !ChannelCanPublish(&channel) {
		recordAutoPilotSkip(&cfg, "Nenhuma conta de postagem conectada - conecte o YouTube ou o TikTok para o Piloto Automático produzir vídeos.")
		return
	}

	// 0d. Keep only a small buffer of unpublished videos.
	var queued int64
	db.DB.Model(&models.Video{}).
		Where("channel_id = ? AND status IN ?", channel.ID, []string{models.VideoStatusGenerating, models.VideoStatusScheduled, models.VideoStatusPosting}).
		Count(&queued)
	if queued >= autopilotMaxQueued {
		recordAutoPilotSkip(&cfg, fmt.Sprintf("Fila cheia: %d vídeo(s) aguardando publicação - o próximo será produzido quando a fila andar.", queued))
		return
	}

	videosPerWeek := cfg.VideosPerWeek
	if videosPerWeek <= 0 {
		videosPerWeek = 7
	}
	preferredHour := cfg.PreferredPostHour
	if preferredHour < 0 || preferredHour > 23 {
		preferredHour = 18
	}

	var videosThisWeek int64
	weekAgo := time.Now().AddDate(0, 0, -7)
	db.DB.Model(&models.Video{}).
		Where("channel_id = ? AND created_at >= ?", channel.ID, weekAgo).
		Count(&videosThisWeek)

	if videosThisWeek >= int64(videosPerWeek) {
		log.Printf("Auto-Pilot: channel '%s' already reached its weekly cap (%d/%d videos in the last 7 days), skipping this cycle",
			channel.Name, videosThisWeek, videosPerWeek)
		recordAutoPilotSkip(&cfg, fmt.Sprintf("Limite semanal atingido (%d/%d vídeos nos últimos 7 dias).", videosThisWeek, videosPerWeek))
		return
	}
	recordAutoPilotSkip(&cfg, "")

	// 1. Fetch channel's recent videos and analytics
	var recentVideos []models.Video
	db.DB.Preload("Analytics").Where("channel_id = ?", channel.ID).Order("created_at desc").Limit(5).Find(&recentVideos)

	// Calculate average metrics
	var totalCTR float64 = 0
	var totalRetention float64 = 0
	var count int = 0
	var topPerformingTopic string = channel.Niche

	for _, v := range recentVideos {
		if v.Analytics != nil {
			totalCTR += v.Analytics.CTR
			totalRetention += v.Analytics.RetentionRate
			count++
			if v.Analytics.CTR > 8.0 {
				topPerformingTopic = v.Title
			}
		}
	}

	avgCTR := 7.5
	avgRetention := 55.0
	if count > 0 {
		avgCTR = totalCTR / float64(count)
		avgRetention = totalRetention / float64(count)
	}

	// videoID is minted up front (before we've even picked a topic) so every
	// LLM/image/TTS call from here on can be attributed to it in the cost log.
	videoID := uuid.New().String()

	// 2. Discover Real-Time Google Trends for the Niche
	liveTrends, _ := FetchRealTimeTrends(channel.ID, channel.Niche)
	trendsSummary := "Tendências de busca em alta hoje: " + strings.Join(liveTrends, ", ")

	// Same no-repeat rule the manual flow uses (video_creation.go) - the
	// auto-pilot never told the LLM what the channel had already covered.
	var existingTitles []string
	db.DB.Model(&models.Video{}).Where("channel_id = ?", channel.ID).Order("created_at desc").Limit(50).Pluck("title", &existingTitles)

	// 3. Ask AI to analyze metrics + trends and decide next high-CTR topic
	prompt := fmt.Sprintf(`Você é o Gerente Autônomo de Inteligência Artificial do canal dark '%s' (Nicho: '%s').
Analise as métricas recentes de desempenho do canal e as tendências de pesquisa em tempo real:
- Taxa Média de Clique (CTR): %.1f%%
- Duração Média de Visualização (Retenção): %.1f%%
- Tópico com maior engajamento recente: '%s'
- %s

- Vídeos já criados neste canal: %v

Com base nesses dados de tendências ao vivo e retenção, escolha o assunto do PRÓXIMO VÍDEO que surfará nas maiores buscas de hoje com a maior taxa de cliques.
REGRA OBRIGATÓRIA: o assunto deve ser inédito - nunca repita um tema da lista de vídeos já criados. Retorne apenas o assunto em texto simples.`,
		channel.Name, channel.Niche, avgCTR, avgRetention, topPerformingTopic, trendsSummary, existingTitles)

	nextTopic, err := CallLLM(channel.ID, videoID, prompt)
	nextTopic = strings.Trim(strings.TrimSpace(nextTopic), "\"")
	if err != nil || nextTopic == "" {
		if len(liveTrends) > 0 {
			nextTopic = liveTrends[0]
		} else {
			nextTopic = fmt.Sprintf("Segredos não revelados de %s", channel.Niche)
		}
	}

	log.Printf("Auto-Pilot selected real-time trend topic for '%s': %s", channel.Name, nextTopic)

	// 3. Create the video row upfront (placeholder title) so a script/scene
	// generation failure is still visible in the channel's video history
	// instead of vanishing with just a log line - this used to be silent.
	video := models.Video{
		ID:        videoID,
		ChannelID: channel.ID,
		Title:     "Gerando roteiro por IA...",
		Status:    "generating",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := db.DB.Create(&video).Error; err != nil {
		log.Printf("Auto-Pilot: failed to create video row for channel %s: %v", channel.ID, err)
		recordAutoPilotFailure(&cfg, "Criação do vídeo: "+err.Error())
		return
	}

	// 4. Generate script, every scene (narration + alternating video/image
	// asset), and the thumbnail - shared with the manual "Gerar Vídeo" flow
	// (video_creation.go) so the two pipelines can't drift out of sync again.
	if _, err := generateScriptScenesAndThumbnail(channel, videoID, nextTopic, func(pct int, stage string) {}); err != nil {
		log.Printf("Auto-Pilot script/scenes generation error: %v", err)
		video.Status = "failed"
		video.ErrorMessage = err.Error()
		db.DB.Save(&video)
		recordAutoPilotFailure(&cfg, "Roteiro/cenas: "+err.Error())
		return
	}

	// Fetch full video model with scenes
	db.DB.Preload("Scenes").First(&video, "id = ?", videoID)

	// 5. Render the actual video. Shotstack renders in the cloud when configured;
	// otherwise we render for real locally with FFmpeg (the same pipeline the
	// manual "Gerar Vídeo Completo" button uses) - never substitute a random
	// stock clip and pretend it's the rendered video.
	if config.IsSet(config.AppConfig.ShotstackAPIKey) {
		// Shotstack doesn't report per-scene fallback usage back, so it's
		// computed here from the same data submitShotstackRender uses to pick
		// assets - a scene with no visual and/or no narration audio degrades
		// the render exactly like the local FFmpeg path's fallback does.
		degraded := 0
		for _, s := range video.Scenes {
			if (s.VideoClipURL == "" && s.ImagePath == "") || s.AudioPath == "" {
				degraded++
			}
		}
		video.DegradedSceneCount = degraded
		video.TotalSceneCount = len(video.Scenes)

		cloudVideoURL, err := RenderVideoInCloud(&video, false)
		if err != nil {
			log.Printf("Auto-Pilot cloud render error: %v", err)
			video.Status = "failed"
			video.ErrorMessage = err.Error()
			db.DB.Save(&video)
			recordAutoPilotFailure(&cfg, "Render (cloud): "+err.Error())
			return
		}
		cloudShortsURL, _ := RenderVideoInCloud(&video, true)
		video.VideoPath = cloudVideoURL
		video.ShortsPath = cloudShortsURL

		// Source scene assets are already baked into the Shotstack render -
		// only the rendered outputs need to persist in the bucket from here on.
		purgeSceneAssets(&video)
	} else {
		// CompileVideo is normally launched with `go` from the manual render
		// endpoint (fire-and-forget for an HTTP handler); here we're already
		// inside our own per-channel goroutine, so we call it synchronously
		// and wait for the real render before scheduling anything.
		CompileVideo(video.ID)
		if err := db.DB.First(&video, "id = ?", videoID).Error; err != nil {
			log.Printf("Auto-Pilot: failed to reload video %s after render: %v", videoID, err)
			recordAutoPilotFailure(&cfg, "Render (local): falha ao recarregar vídeo: "+err.Error())
			return
		}
		if video.Status != "ready" {
			log.Printf("Auto-Pilot local render failed for '%s': %s", video.Title, video.ErrorMessage)
			recordAutoPilotFailure(&cfg, "Render (local): "+video.ErrorMessage)
			return
		}
	}

	// 6. Automatically approve and schedule video post for tomorrow at the channel's preferred hour
	// (video.VideoPath/ShortsPath were already set above, by whichever render path ran)
	now := time.Now()
	scheduledDate := nextAutoPilotSlot(channel.ID, videosPerWeek, preferredHour, now)

	video.Status = models.VideoStatusScheduled
	video.ScheduledFor = &scheduledDate
	video.UpdatedAt = time.Now()
	db.DB.Save(&video)

	cfg.LastOptimizationAt = now
	cfg.CurrentStrategy = fmt.Sprintf("Último tópico escolhido: '%s' (%d/%d vídeos usados nesta semana)", nextTopic, videosThisWeek+1, videosPerWeek)
	recordAutoPilotSuccess(&cfg)

	log.Printf("Auto-Pilot SUCCESSFULLY generated, rendered, approved and scheduled video '%s' for %s",
		video.Title, scheduledDate.Format(time.RFC3339))
}

// autopilotBackoffDelay doubles the wait per consecutive failure (10min, 20,
// 40, 80...), capped at 6h so a persistently broken channel is retried a few
// times a day instead of every single 10-minute tick forever.
func autopilotBackoffDelay(failures int) time.Duration {
	if failures <= 0 {
		return 0
	}
	exp := failures - 1
	if exp > 6 {
		exp = 6
	}
	delay := 10 * time.Minute * time.Duration(1<<uint(exp))
	const maxDelay = 6 * time.Hour
	if delay > maxDelay {
		delay = maxDelay
	}
	return delay
}

// recordAutoPilotFailure persists the failure so the next cycle's backoff
// check (see processAutoPilotChannel) can see it, and so the operator can
// see why a channel stalled (surfaced via GET /autopilot-config).
func recordAutoPilotFailure(cfg *models.AutoPilotConfig, reason string) {
	now := time.Now()
	cfg.ConsecutiveFailures++
	cfg.LastFailureAt = &now
	cfg.LastFailureReason = reason
	if err := db.DB.Save(cfg).Error; err != nil {
		log.Printf("Auto-Pilot: failed to persist failure state for channel %s: %v", cfg.ChannelID, err)
	}
}

// recordAutoPilotSuccess resets the backoff state once a cycle actually
// completes end to end.
func recordAutoPilotSuccess(cfg *models.AutoPilotConfig) {
	cfg.ConsecutiveFailures = 0
	cfg.LastFailureAt = nil
	cfg.LastFailureReason = ""
	db.DB.Save(cfg)
}
