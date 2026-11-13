package services

import (
	"fmt"
	"log"
	"strings"
	"time"

	"makinadark/backend/internal/db"
	"makinadark/backend/internal/models"
)

// StartScheduler starts the background workers: the per-minute publisher and
// the watchdog that recovers jobs whose goroutine died mid-flight.
func StartScheduler() {
	log.Println("Starting background video posting scheduler...")

	// Generation, rendering and publishing all run in goroutines of THIS
	// process - after a restart/deploy nothing is running them anymore, so
	// anything still marked in-progress at boot is dead by definition.
	// Recovering it immediately beats leaving a frozen progress bar for up to
	// 20 minutes until the periodic watchdog notices.
	recoverInterruptedJobs(0)

	ticker := time.NewTicker(1 * time.Minute)
	go func() {
		for range ticker.C {
			processScheduledVideos()
		}
	}()

	// Periodic watchdog for goroutines that hang without the process dying.
	// A real 35-55 scene generation updates render_progress (and therefore
	// updated_at) after every scene, so 20+ minutes of silence means dead.
	watchdogTicker := time.NewTicker(5 * time.Minute)
	go func() {
		for range watchdogTicker.C {
			recoverInterruptedJobs(1)
		}
	}()
}

// recoverInterruptedJobs marks stuck in-progress rows as recoverable. scale
// 0 = boot (everything in progress is dead), 1 = periodic (only rows that
// have been silent longer than their stage's normal duration).
func recoverInterruptedJobs(scale time.Duration) {
	recoverStuckVideos(models.VideoStatusGenerating, scale*20*time.Minute)
	// A publish can legitimately take a long time (download + 1 GB upload)
	// without touching the row, hence the much longer window.
	recoverStuckVideos(models.VideoStatusPosting, scale*2*time.Hour)
	recoverStuckBookends(scale * 10 * time.Minute)
}

func recoverStuckVideos(status string, staleAfter time.Duration) {
	cutoff := time.Now().Add(-staleAfter)

	var stuck []models.Video
	if err := db.DB.Where("status = ? AND updated_at <= ?", status, cutoff).Find(&stuck).Error; err != nil {
		log.Printf("Watchdog: error fetching stuck %q videos: %v", status, err)
		return
	}

	for _, v := range stuck {
		log.Printf("Watchdog: video %s (%q) stuck in %q since %s, recovering", v.ID, v.Title, status, v.UpdatedAt.Format(time.RFC3339))
		switch status {
		case models.VideoStatusPosting:
			// The rendered file is still there - go back to "ready" so the
			// operator can reschedule, instead of a "failed" that offers a
			// re-render the (already purged) scene assets can't support.
			db.DB.Model(&models.Video{}).Where("id = ? AND status = ?", v.ID, status).Updates(map[string]interface{}{
				"status":        models.VideoStatusReady,
				"scheduled_for": nil,
				"error_message": "Publicação interrompida (reinício do servidor durante o envio) - agende novamente.",
			})
		default:
			db.DB.Model(&models.Video{}).Where("id = ? AND status = ?", v.ID, status).Updates(map[string]interface{}{
				"status":        models.VideoStatusFailed,
				"error_message": fmt.Sprintf("Geração/renderização interrompida inesperadamente (parou em %q) - tente novamente.", v.RenderStage),
			})
		}
	}
}

// recoverStuckBookends is the same watchdog for a channel's intro/outro
// clips (bookends.go), which live on the channels table.
func recoverStuckBookends(staleAfter time.Duration) {
	cutoff := time.Now().Add(-staleAfter)
	const stuckMsg = "Geração interrompida inesperadamente (reinício do servidor durante o render) - tente novamente."

	for _, kind := range []string{"intro", "outro"} {
		var stuck []models.Channel
		if err := db.DB.Where(kind+"_status = ? AND updated_at <= ?", "generating", cutoff).Find(&stuck).Error; err != nil {
			log.Printf("Watchdog: error fetching stuck %ss: %v", kind, err)
			continue
		}
		for _, c := range stuck {
			log.Printf("Watchdog: channel %s (%q) %s stuck in \"generating\", marking failed", c.ID, c.Name, kind)
			updateBookendProgress(c.ID, kind, "failed", 0, "Falha ao renderizar vídeo", stuckMsg)
		}
	}
}

func processScheduledVideos() {
	var due []models.Video
	err := db.DB.Select("id").
		Where("status = ? AND scheduled_for <= ?", models.VideoStatusScheduled, time.Now()).
		Find(&due).Error
	if err != nil {
		log.Printf("Scheduler error fetching videos: %v", err)
		return
	}

	for _, v := range due {
		// Atomic claim: only the tick that flips scheduled -> posting gets to
		// publish. Without this, a publish slower than one tick (any real
		// upload) was picked up again and posted twice.
		res := db.DB.Model(&models.Video{}).
			Where("id = ? AND status = ?", v.ID, models.VideoStatusScheduled).
			Updates(map[string]interface{}{"status": models.VideoStatusPosting, "error_message": ""})
		if res.Error != nil || res.RowsAffected != 1 {
			continue
		}
		log.Printf("Scheduler: publishing video %s", v.ID)
		go publishVideo(v.ID)
	}
}

// returnToReady ends a publish attempt that didn't reach any platform: the
// rendered files are still intact, so the video goes back to "ready" with
// the reason shown and its schedule cleared (leaving it "scheduled" would
// just retry - and fail - every minute forever).
func returnToReady(videoID, reason string) {
	log.Printf("Scheduler: video %s not published: %s", videoID, reason)
	db.DB.Model(&models.Video{}).Where("id = ?", videoID).Updates(map[string]interface{}{
		"status":        models.VideoStatusReady,
		"scheduled_for": nil,
		"error_message": reason,
	})
}

func publishVideo(videoID string) {
	var video models.Video
	if err := db.DB.First(&video, "id = ?", videoID).Error; err != nil {
		log.Printf("Scheduler: video %s vanished before publishing: %v", videoID, err)
		return
	}
	var channel models.Channel
	if err := db.DB.First(&channel, "id = ?", video.ChannelID).Error; err != nil {
		returnToReady(videoID, "Canal do vídeo não encontrado.")
		return
	}

	hasYouTube := channel.YTRefreshToken != ""
	hasTikTok := channel.TikTokRefreshToken != ""
	if !hasYouTube && !hasTikTok {
		// Used to be marked "posted" anyway, with nothing posted anywhere.
		returnToReady(videoID, "Nenhuma conta de postagem conectada ao canal - conecte o YouTube ou o TikTok e agende novamente.")
		return
	}

	// Materialize the long-form render locally (it may live in R2). The
	// publish gate inspects this exact file, so what's checked is what ships.
	longLocal, cleanupLong, err := localCopy(video.VideoPath, "publish_long_"+video.ID)
	if err != nil {
		returnToReady(videoID, "Bloqueado antes de publicar: "+err.Error())
		return
	}
	defer cleanupLong()

	// Last line of defense before this reaches a real audience.
	if ok, reason := videoPassesPublishGate(&video, longLocal); !ok {
		returnToReady(videoID, "Bloqueado antes de publicar: "+reason)
		return
	}

	var ytID, ttID string
	var problems []string

	if hasYouTube {
		log.Printf("Posting video %q to YouTube...", video.Title)
		ytID, err = UploadToYouTube(&channel, &video, longLocal)
		if err != nil {
			log.Printf("YouTube upload error for %s: %v", video.ID, err)
			problems = append(problems, "YouTube: "+err.Error())
		}
	}

	if hasTikTok {
		shortSource := video.ShortsPath
		if shortSource == "" {
			shortSource = video.VideoPath
		}
		shortLocal, cleanupShort, dlErr := localCopy(shortSource, "publish_short_"+video.ID)
		if dlErr != nil {
			problems = append(problems, "TikTok: "+dlErr.Error())
		} else {
			log.Printf("Posting shorts of video %q to TikTok...", video.Title)
			ttID, err = UploadToTikTok(&channel, &video, shortLocal)
			cleanupShort()
			if err != nil {
				log.Printf("TikTok upload error for %s: %v", video.ID, err)
				problems = append(problems, "TikTok: "+err.Error())
			}
		}
	}

	if ytID == "" && ttID == "" {
		returnToReady(videoID, "Falha ao publicar - "+strings.Join(problems, " | "))
		return
	}

	// At least one platform has it. The other's failure (if any) stays
	// visible as a warning on the posted video.
	now := time.Now()
	updates := map[string]interface{}{
		"status":            models.VideoStatusPosted,
		"posted_at":         &now,
		"youtube_video_id":  ytID,
		"tiktok_publish_id": ttID,
		"error_message":     strings.Join(problems, " | "),
	}

	// The content lives on YouTube/TikTok now, so free bucket storage.
	purgeVideoMedia(&video)
	updates["video_path"] = video.VideoPath
	updates["shorts_path"] = video.ShortsPath
	updates["subtitles_path"] = video.SubtitlesPath
	updates["media_purged"] = video.MediaPurged

	db.DB.Model(&models.Video{}).Where("id = ?", videoID).Updates(updates)
	log.Printf("Scheduler: video %q published (YouTube=%q TikTok=%q)", video.Title, ytID, ttID)
}

// purgeVideoMedia deletes the final rendered video/shorts files from storage
// (R2 bucket or local disk) after a successful auto-post, since the content
// now lives on YouTube/TikTok and keeping a duplicate copy just costs storage.
func purgeVideoMedia(video *models.Video) {
	for _, p := range []string{video.VideoPath, video.ShortsPath, video.SubtitlesPath} {
		if p == "" {
			continue
		}
		if err := DeleteMedia(p); err != nil {
			log.Printf("Warning: failed to purge %s for %s: %v", p, video.ID, err)
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
func videoPassesPublishGate(video *models.Video, localPath string) (bool, string) {
	if localPath == "" {
		return false, "nenhum arquivo de vídeo final encontrado"
	}

	const minDurationSeconds = 120.0
	duration, err := GetAudioDuration(localPath)
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

// ChannelCanPublish reports whether a channel has at least one linked
// posting account - shared by scheduling, the auto-pilot and the API.
func ChannelCanPublish(ch *models.Channel) bool {
	return ch.YTRefreshToken != "" || ch.TikTokRefreshToken != ""
}
