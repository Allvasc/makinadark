package services

import (
	"fmt"
	"log"
	"time"

	"makinadark/backend/internal/db"
	"makinadark/backend/internal/models"
)

// StartScheduler starts a background worker that polls for scheduled videos and publishes them
func StartScheduler() {
	log.Println("Starting background video posting scheduler...")

	ticker := time.NewTicker(1 * time.Minute)
	go func() {
		for {
			select {
			case <-ticker.C:
				processScheduledVideos()
			}
		}
	}()

	// A video whose generation/render goroutine dies mid-flight (server
	// restart, deploy, crash) leaves status="generating" forever - nothing
	// was ever watching for that, so the operator just saw a progress bar
	// that silently stopped moving with no error and no way to retry. Every
	// 5 minutes, anything that hasn't touched updated_at in 20+ minutes is
	// almost certainly dead, not just slow (a real 35-55 scene generation
	// updates render_progress after every scene).
	watchdogTicker := time.NewTicker(5 * time.Minute)
	go func() {
		for {
			select {
			case <-watchdogTicker.C:
				recoverStuckGeneratingVideos()
				recoverStuckBookends()
			}
		}
	}()
}

func recoverStuckGeneratingVideos() {
	const staleAfter = 20 * time.Minute
	cutoff := time.Now().Add(-staleAfter)

	var stuck []models.Video
	if err := db.DB.Where("status = ? AND updated_at < ?", "generating", cutoff).Find(&stuck).Error; err != nil {
		log.Printf("Watchdog: error fetching stuck videos: %v", err)
		return
	}

	for _, v := range stuck {
		log.Printf("Watchdog: video %s (%q) stuck in \"generating\" since %s, marking failed", v.ID, v.Title, v.UpdatedAt.Format(time.RFC3339))
		updateVideoStatus(&v, "failed", fmt.Sprintf("Geração/renderização interrompida inesperadamente (parou em %q) - tente novamente.", v.RenderStage))
	}
}

// recoverStuckBookends is the same watchdog as recoverStuckGeneratingVideos
// but for a channel's intro/outro clips (bookends.go) - a goroutine dies the
// same way on a mid-render server restart, but it writes to the channels
// table via updateIntroProgress/updateOutroProgress instead of the videos
// table, so the video watchdog never saw it (this is exactly how one outro
// got stuck at "generating" 75% forever after a backend restart). A bookend
// render is one short ffmpeg call, not a 35-55 scene job, so a much shorter
// staleness window is appropriate here.
func recoverStuckBookends() {
	const staleAfter = 5 * time.Minute
	cutoff := time.Now().Add(-staleAfter)
	const stuckMsg = "Geração interrompida inesperadamente (reinício do servidor durante o render) - tente novamente."

	var stuckIntros []models.Channel
	if err := db.DB.Where("intro_status = ? AND updated_at < ?", "generating", cutoff).Find(&stuckIntros).Error; err != nil {
		log.Printf("Watchdog: error fetching stuck intros: %v", err)
	}
	for _, c := range stuckIntros {
		log.Printf("Watchdog: channel %s (%q) intro stuck in \"generating\" since %s, marking failed", c.ID, c.Name, c.UpdatedAt.Format(time.RFC3339))
		updateIntroProgress(c.ID, "failed", 0, "Falha ao renderizar vídeo", stuckMsg)
	}

	var stuckOutros []models.Channel
	if err := db.DB.Where("outro_status = ? AND updated_at < ?", "generating", cutoff).Find(&stuckOutros).Error; err != nil {
		log.Printf("Watchdog: error fetching stuck outros: %v", err)
	}
	for _, c := range stuckOutros {
		log.Printf("Watchdog: channel %s (%q) outro stuck in \"generating\" since %s, marking failed", c.ID, c.Name, c.UpdatedAt.Format(time.RFC3339))
		updateOutroProgress(c.ID, "failed", 0, "Falha ao renderizar vídeo", stuckMsg)
	}
}

func processScheduledVideos() {
	var videos []models.Video
	now := time.Now()

	// Query videos that are "scheduled" and scheduled_for <= now
	err := db.DB.Preload("Scenes").
		Where("status = ? AND scheduled_for <= ?", "scheduled", now).
		Find(&videos).Error

	if err != nil {
		log.Printf("Scheduler error fetching videos: %v", err)
		return
	}

	if len(videos) == 0 {
		return
	}

	log.Printf("Scheduler found %d video(s) ready to be posted", len(videos))

	for _, video := range videos {
		go publishVideo(video)
	}
}

func publishVideo(video models.Video) {
	// Last line of defense before this goes out to a real YouTube/TikTok
	// account - nothing upstream (script generation, scene rendering) blocks
	// a bad result on its own, so a bug in an earlier stage still gets
	// caught here instead of reaching a real audience.
	if ok, reason := videoPassesPublishGate(&video); !ok {
		log.Printf("Scheduler: blocking publish of video %s (%q): %s", video.ID, video.Title, reason)
		updateVideoStatus(&video, "failed", "Bloqueado antes de publicar: "+reason)
		return
	}

	// 1. Fetch channel info
	var channel models.Channel
	if err := db.DB.First(&channel, "id = ?", video.ChannelID).Error; err != nil {
		log.Printf("Scheduler: failed to find channel for video %s: %v", video.ID, err)
		updateVideoStatus(&video, "failed", "Channel not found in database")
		return
	}

	updateVideoStatus(&video, "posting", "")

	var ytID string
	var ttID string
	var err error

	// 2. Upload to YouTube if configured
	if channel.YTRefreshToken != "" && channel.YTRefreshToken != "your_youtube_client_secret_here" {
		log.Printf("Posting video %s to YouTube...", video.Title)
		ytID, err = UploadToYouTube(&channel, &video, false)
		if err != nil {
			log.Printf("YouTube upload error: %v", err)
			updateVideoStatus(&video, "failed", "YouTube upload error: "+err.Error())
			return
		}
	} else {
		log.Printf("Skipping YouTube upload for video %s: channel has no credentials", video.ID)
	}

	// 3. Upload Shorts to TikTok if configured
	if channel.TikTokRefreshToken != "" && channel.TikTokRefreshToken != "your_tiktok_client_secret_here" {
		log.Printf("Posting shorts of video %s to TikTok...", video.Title)
		ttID, err = UploadToTikTok(&channel, &video)
		if err != nil {
			log.Printf("TikTok upload error: %v", err)
			// We can decide whether to fail the whole post if TikTok fails but YT succeeds.
			// Let's log it and note it down.
			video.ErrorMessage += " | TikTok error: " + err.Error()
		}
	} else {
		log.Printf("Skipping TikTok upload for video %s: channel has no credentials", video.ID)
	}

	// 4. Update status to posted
	now := time.Now()
	video.Status = "posted"
	video.PostedAt = &now
	if ytID != "" {
		video.ErrorMessage += " | YT: " + ytID
	}
	if ttID != "" {
		video.ErrorMessage += " | TT: " + ttID
	}

	// 5. The video is safely on YouTube/TikTok now, so free up bucket storage by
	// deleting the rendered files - only when at least one platform actually got it.
	if ytID != "" || ttID != "" {
		purgeVideoMedia(&video)
	}

	db.DB.Save(&video)

	log.Printf("Scheduler: video %s successfully published", video.Title)
}

// purgeVideoMedia deletes the final rendered video/shorts files from storage
// (R2 bucket or local disk) after a successful auto-post, since the content
// now lives on YouTube/TikTok and keeping a duplicate copy just costs storage.
// Scene images/audio are left untouched (small, useful for future reference).
func purgeVideoMedia(video *models.Video) {
	if video.VideoPath != "" {
		if err := DeleteMedia(video.VideoPath); err != nil {
			log.Printf("Warning: failed to purge video file for %s: %v", video.ID, err)
		}
	}
	if video.ShortsPath != "" {
		if err := DeleteMedia(video.ShortsPath); err != nil {
			log.Printf("Warning: failed to purge shorts file for %s: %v", video.ID, err)
		}
	}
	if video.SubtitlesPath != "" {
		if err := DeleteMedia(video.SubtitlesPath); err != nil {
			log.Printf("Warning: failed to purge subtitles for %s: %v", video.ID, err)
		}
	}
	video.VideoPath = ""
	video.ShortsPath = ""
	video.SubtitlesPath = ""
	video.MediaPurged = true
}

// videoPassesPublishGate checks the actual render output, not upstream
// intent - a truncated/near-empty file or a video where too many scenes fell
// back to silence/generic stock footage never reaches a real audience.
func videoPassesPublishGate(video *models.Video) (bool, string) {
	if video.VideoPath == "" {
		return false, "nenhum arquivo de vídeo final encontrado"
	}

	const minDurationSeconds = 120.0
	duration, err := GetAudioDuration(video.VideoPath)
	if err != nil {
		return false, fmt.Sprintf("não foi possível ler o arquivo de vídeo final: %v", err)
	}
	if duration < minDurationSeconds {
		return false, fmt.Sprintf("vídeo final tem apenas %.0fs (mínimo exigido: %.0fs) - provavelmente truncado", duration, minDurationSeconds)
	}

	if video.TotalSceneCount > 0 {
		const maxDegradedRatio = 0.3
		degradedRatio := float64(video.DegradedSceneCount) / float64(video.TotalSceneCount)
		if degradedRatio > maxDegradedRatio {
			return false, fmt.Sprintf("%d de %d cenas (%.0f%%) usaram conteúdo de reserva (silêncio ou clipe genérico) - acima do limite de %.0f%%",
				video.DegradedSceneCount, video.TotalSceneCount, degradedRatio*100, maxDegradedRatio*100)
		}
	}

	return true, ""
}

func updateVideoStatus(video *models.Video, status string, errMsg string) {
	video.Status = status
	video.ErrorMessage = errMsg
	db.DB.Save(video)
}
