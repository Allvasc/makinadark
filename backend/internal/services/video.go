package services

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"makinadark/backend/internal/config"
	"makinadark/backend/internal/db"
	"makinadark/backend/internal/models"
)

// CheckFFmpeg verifies if ffmpeg and ffprobe are available in the system path
func CheckFFmpeg() bool {
	_, err1 := exec.LookPath("ffmpeg")
	_, err2 := exec.LookPath("ffprobe")
	return err1 == nil && err2 == nil
}

// GetAudioDuration probe the duration of an audio file using ffprobe
func GetAudioDuration(filePath string) (float64, error) {
	if !CheckFFmpeg() {
		return 5.0, nil // default fallback
	}

	cmd := exec.Command("ffprobe",
		"-v", "error",
		"-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1",
		filePath,
	)

	out, err := cmd.Output()
	if err != nil {
		return 0, err
	}

	var duration float64
	_, err = fmt.Sscanf(strings.TrimSpace(string(out)), "%f", &duration)
	if err != nil {
		return 0, err
	}

	return duration, nil
}

// CompileVideo orchestrates the complete rendering pipeline in a background goroutine
func CompileVideo(videoID string) {
	// 1. Fetch Video, Scenes, and the owning Channel (for its intro/outro bookends)
	var video models.Video
	if err := db.DB.Preload("Scenes").First(&video, "id = ?", videoID).Error; err != nil {
		log.Printf("Error fetching video for render: %v", err)
		return
	}
	var channel models.Channel
	db.DB.First(&channel, "id = ?", video.ChannelID)

	video.Status = "generating"
	video.RenderProgress = 0
	video.RenderStage = "Preparando renderização..."
	db.DB.Save(&video)

	log.Printf("Starting video rendering for ID: %s", video.ID)

	if len(video.Scenes) == 0 {
		failVideo(&video, "Nenhuma cena para renderizar - gere o roteiro novamente.")
		return
	}

	// Rendering runs entirely on this server (FFmpeg in the backend container),
	// never in the browser - totalSteps is scenes + concat + mix/subtitles + shorts.
	totalSteps := len(video.Scenes) + 3

	// Ensure media directories exist
	os.MkdirAll("./media/output", 0755)
	os.MkdirAll("./media/temp", 0755)

	// Background bed is procedurally generated noise (via ffmpeg's anoisesrc),
	// not a downloaded music track - see noiseColorForMood for why. The
	// Sonoplastia agent's mood decision (video.MusicMood) takes precedence
	// over niche keyword-matching when present; if neither is set, the niche
	// text itself is classified into a mood so there's always a real choice
	// behind the color, never a hardcoded default.
	noiseColor := noiseColorForMood(video.MusicMood, channel.Niche)
	musicMixLevel := video.MusicMixLevel
	if musicMixLevel <= 0 {
		musicMixLevel = 0.12
	}

	// Check if FFmpeg is installed.
	if !CheckFFmpeg() {
		log.Println("ERROR: FFmpeg is not installed on this system.")
		video.Status = "failed"
		video.ErrorMessage = "FFmpeg is not installed on this system"
		db.DB.Save(&video)
		return
	}

	var segmentPaths []string
	var subtitleLines []string
	var currentTime float64 = 0.0
	subtitleIdx := 0

	// 2a. Prepend the channel's intro bookend, if it has one (see GenerateChannelBookends)
	if channel.IntroVideoPath != "" {
		introLocal := filepath.Join("media", "temp", fmt.Sprintf("intro_%s.mp4", video.ID))
		if err := downloadToLocal(channel.IntroVideoPath, introLocal); err != nil {
			log.Printf("Warning: failed to fetch intro clip for video %s: %v", video.ID, err)
		} else {
			introDuration, durErr := GetAudioDuration(introLocal)
			if durErr != nil || introDuration <= 0 {
				introDuration = 5.0
			}
			segmentPaths = append(segmentPaths, introLocal)
			if channel.IntroScript != "" {
				currentTime = appendSubtitleEntries(&subtitleLines, channel.IntroScript, currentTime, introDuration, &subtitleIdx)
			} else {
				currentTime += introDuration
			}
		}
	}

	// 2b. Generate each scene's segment. Every scene is independent (its own
	// asset download + ffmpeg encode, writing to its own segment file), so
	// they run in a small worker pool instead of one at a time - this used to
	// be fully sequential, which meant a 35-55 scene / 20-30 minute video
	// took as long as the sum of every single scene's encode time. Segment
	// order and subtitle timing are still assembled sequentially afterward,
	// since subtitle timestamps depend on the cumulative duration of every
	// prior scene.
	type sceneResult struct {
		segmentPath string
		duration    float64
		degraded    bool
		err         error
	}
	results := make([]sceneResult, len(video.Scenes))

	workers := runtime.NumCPU()
	if workers > 4 {
		workers = 4 // each ffmpeg encode already uses multiple threads internally
	}
	if workers < 1 {
		workers = 1
	}

	var dbMu sync.Mutex // serialize DB writes/progress updates across workers
	var doneCount int   // guarded by dbMu
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup

	for i, scene := range video.Scenes {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, scene models.Scene) {
			defer wg.Done()
			defer func() { <-sem }()

			degraded := false

			// 1. Prepare local visual asset (download if remote HTTP)
			visualPath := ensureLocalFile(scene.VideoClipURL, "video", fmt.Sprintf("%s_%d", video.ID, i))
			isImage := false
			if visualPath == "" {
				visualPath = ensureLocalFile(scene.ImagePath, "image", fmt.Sprintf("%s_%d", video.ID, i))
				isImage = true
			}
			var fallbackErr error
			if visualPath == "" {
				// Fallback asset - no real scene visual was available or reachable.
				degraded = true
				visualPath, isImage, fallbackErr = resolveFallbackVisual(video.ID, i)
			}

			// 2. Prepare local audio asset
			audioPath := ensureLocalFile(scene.AudioPath, "audio", fmt.Sprintf("aud_%s_%d", video.ID, i))
			if audioPath == "" || !fileExists(audioPath) {
				// No narration audio for this scene - it renders silent.
				degraded = true
				audioPath = filepath.Join("media", "temp", fmt.Sprintf("silent_%s_%d.mp3", video.ID, i))
				_ = generateSilentAudio(audioPath, 5.0)
			}

			// Verify audio duration
			duration, err := GetAudioDuration(audioPath)
			if err != nil || duration <= 0 {
				duration = 5.0
			}

			// Create segment filepath
			segmentPath := filepath.Join("media", "temp", fmt.Sprintf("seg_%s_%d.mp4", video.ID, i))

			if fallbackErr != nil || visualPath == "" {
				if fallbackErr == nil {
					fallbackErr = fmt.Errorf("missing visual asset path")
				}
				log.Printf("Fallback visual error (Scene %d): %v", i+1, fallbackErr)
				dbMu.Lock()
				scene.Duration = duration
				scene.Degraded = degraded
				db.DB.Save(&scene)
				doneCount++
				updateRenderProgress(video.ID, progressPercent(doneCount, totalSteps), fmt.Sprintf("Renderizando cena %d de %d", doneCount, len(video.Scenes)))
				dbMu.Unlock()
				results[i] = sceneResult{
					segmentPath: segmentPath,
					duration:    duration,
					degraded:    degraded,
					err:         fmt.Errorf("failed to generate scene segment %d: %w", i+1, fallbackErr),
				}
				return
			}

			// Scale and crop filter with Ken Burns zoompan effect on all scenes (images and videos)
			fps := 25.0
			totalFrames := int(duration * fps)
			if totalFrames < 50 {
				totalFrames = 50
			}
			zoomStep := 0.10 / float64(totalFrames)

			var zoomFilter string
			if isImage {
				zoomFilter = fmt.Sprintf("zoompan=z='min(pzoom+%f,1.12)':d=1:x='iw/2-(iw/zoom/2)':y='ih/2-(ih/zoom/2)':s=1920x1080:fps=25", zoomStep)
			} else {
				zoomFilter = fmt.Sprintf("zoompan=z='min(zoom+%f,1.12)':d=1:x='iw/2-(iw/zoom/2)':y='ih/2-(ih/zoom/2)':s=1920x1080:fps=25", zoomStep)
			}

			videoFilter := "scale=1920:1080:force_original_aspect_ratio=increase,crop=1920:1080," + zoomFilter

			fadeOutStart := duration - 0.4
			if fadeOutStart < 0 {
				fadeOutStart = 0
			}

			// Fade transitions between internal scenes.
			// NEVER apply fade-in on scene 0 when intro is present, and NEVER apply fade-out on the last scene when outro is present,
			// guaranteeing a hard cut (corte seco) at the intro-to-video and video-to-outro boundaries.
			applyFadeIn := video.UseFadeTransitions && (i > 0 || channel.IntroVideoPath == "")
			applyFadeOut := video.UseFadeTransitions && (i < len(video.Scenes)-1 || channel.OutroVideoPath == "")

			var videoFades []string
			var audioFades []string

			if applyFadeIn {
				videoFades = append(videoFades, "fade=t=in:st=0:d=0.4")
				audioFades = append(audioFades, "afade=t=in:st=0:d=0.4")
			}
			if applyFadeOut {
				videoFades = append(videoFades, fmt.Sprintf("fade=t=out:st=%f:d=0.4", fadeOutStart))
				audioFades = append(audioFades, fmt.Sprintf("afade=t=out:st=%f:d=0.4", fadeOutStart))
			}

			if len(videoFades) > 0 {
				videoFilter += "," + strings.Join(videoFades, ",")
			}

			var audioFadeArgs []string
			if len(audioFades) > 0 {
				audioFadeArgs = []string{"-af", strings.Join(audioFades, ",")}
			}

			var cmd *exec.Cmd
			if isImage {
				args := []string{"-y",
					"-loop", "1",
					"-i", visualPath,
					"-i", audioPath,
					"-map", "0:v:0",
					"-map", "1:a:0",
					"-c:v", "libx264",
					"-crf", "18",
					"-preset", "fast",
					"-b:v", "12M",
					"-maxrate", "16M",
					"-bufsize", "24M",
					"-r", "25",
					"-c:a", "aac",
					"-b:a", "192k",
					"-ar", "44100",
					"-ac", "2",
					"-t", fmt.Sprintf("%f", duration),
					"-shortest",
					"-pix_fmt", "yuv420p",
					"-vf", videoFilter,
				}
				args = append(args, audioFadeArgs...)
				args = append(args, segmentPath)
				cmd = exec.Command("ffmpeg", args...)
			} else {
				args := []string{"-y",
					"-stream_loop", "-1",
					"-i", visualPath,
					"-i", audioPath,
					"-map", "0:v:0",
					"-map", "1:a:0",
					"-c:v", "libx264",
					"-crf", "18",
					"-preset", "fast",
					"-b:v", "12M",
					"-maxrate", "16M",
					"-bufsize", "24M",
					"-r", "25",
					"-c:a", "aac",
					"-b:a", "192k",
					"-ar", "44100",
					"-ac", "2",
					"-t", fmt.Sprintf("%f", duration),
					"-shortest",
					"-pix_fmt", "yuv420p",
					"-vf", videoFilter,
				}
				args = append(args, audioFadeArgs...)
				args = append(args, segmentPath)
				cmd = exec.Command("ffmpeg", args...)
			}

			var cmdErr error
			if out, err := cmd.CombinedOutput(); err != nil {
				log.Printf("FFmpeg segment error (Scene %d): %v\nOutput: %s", i+1, err, string(out))
				cmdErr = fmt.Errorf("failed to generate scene segment %d: %w", i+1, err)
			} else {
				// Measure exact physical duration of the encoded segment to guarantee 100% audio/video/SRT sync
				if segDur, durErr := GetAudioDuration(segmentPath); durErr == nil && segDur > 0 {
					duration = segDur
				}
			}

			dbMu.Lock()
			scene.Duration = duration
			scene.Degraded = degraded
			db.DB.Save(&scene)
			doneCount++
			updateRenderProgress(video.ID, progressPercent(doneCount, totalSteps), fmt.Sprintf("Renderizando cena %d de %d", doneCount, len(video.Scenes)))
			dbMu.Unlock()

			results[i] = sceneResult{segmentPath: segmentPath, duration: duration, degraded: degraded, err: cmdErr}
		}(i, scene)
	}
	wg.Wait()

	degradedScenes := 0
	for i, scene := range video.Scenes {
		r := results[i]
		if r.err != nil {
			failVideo(&video, r.err.Error())
			return
		}
		if r.degraded {
			degradedScenes++
		}

		segmentPaths = append(segmentPaths, r.segmentPath)

		// Build subtitle timestamps for SRT - split long narration into short,
		// readable 3-6 word subtitle chunks distributed across scene duration.
		if strings.TrimSpace(scene.NarrationText) != "" {
			currentTime = appendSubtitleEntries(&subtitleLines, scene.NarrationText, currentTime, r.duration, &subtitleIdx)
		} else {
			currentTime += r.duration
		}
	}

	// 2c. Append the channel's outro bookend, if it has one
	if channel.OutroVideoPath != "" {
		outroLocal := filepath.Join("media", "temp", fmt.Sprintf("outro_%s.mp4", video.ID))
		if err := downloadToLocal(channel.OutroVideoPath, outroLocal); err != nil {
			log.Printf("Warning: failed to fetch outro clip for video %s: %v", video.ID, err)
		} else {
			outroDuration, durErr := GetAudioDuration(outroLocal)
			if durErr != nil || outroDuration <= 0 {
				outroDuration = 5.0
			}
			segmentPaths = append(segmentPaths, outroLocal)
			if channel.OutroScript != "" {
				currentTime = appendSubtitleEntries(&subtitleLines, channel.OutroScript, currentTime, outroDuration, &subtitleIdx)
			}
		}
	}

	// 3. Write Subtitles file (SRT) to media/output/ for permanent export
	os.MkdirAll(filepath.Join("media", "output"), 0755)
	srtPath := filepath.Join("media", "output", fmt.Sprintf("subtitles_%s.srt", video.ID))
	err := os.WriteFile(srtPath, []byte(strings.Join(subtitleLines, "\n")), 0644)
	if err != nil {
		log.Printf("Error writing subtitles file: %v", err)
	}

	// 4. Concat all segments
	updateRenderProgress(video.ID, progressPercent(len(video.Scenes), totalSteps), "Concatenando cenas...")
	concatListPath := filepath.Join("media", "temp", fmt.Sprintf("list_%s.txt", video.ID))
	var concatContent strings.Builder
	for _, path := range segmentPaths {
		concatContent.WriteString(fmt.Sprintf("file '%s'\n", filepath.Base(path)))
	}

	err = os.WriteFile(concatListPath, []byte(concatContent.String()), 0644)
	if err != nil {
		failVideo(&video, fmt.Sprintf("Failed to create concat file list: %v", err))
		return
	}

	rawMergedPath := filepath.Join("media", "temp", fmt.Sprintf("raw_merged_%s.mp4", video.ID))
	concatCmd := exec.Command("ffmpeg", "-y",
		"-f", "concat",
		"-safe", "0",
		"-i", concatListPath,
		"-c", "copy",
		rawMergedPath,
	)

	if out, err := concatCmd.CombinedOutput(); err != nil {
		log.Printf("FFmpeg concat error: %v\nOutput: %s", err, string(out))
		failVideo(&video, fmt.Sprintf("Failed to merge video segments: %v", err))
		return
	}

	// 5-6. Apply subtitles + background music to the long-form output, and
	// build the Shorts/TikTok (9:16) cropped version directly from the raw concat.
	updateRenderProgress(video.ID, progressPercent(len(video.Scenes)+1, totalSteps), "Aplicando trilha sonora, legendas e gerando Shorts...")
	finalLongPath := filepath.Join("media", "output", fmt.Sprintf("video_long_%s.mp4", video.ID))
	finalShortPath := filepath.Join("media", "output", fmt.Sprintf("video_shorts_%s.mp4", video.ID))

	// Format SRT path for FFmpeg subtitles filter safely
	var subFilter string
	var shortsVideoFilter string
	if video.BurnSubtitles {
		formattedSrt := strings.ReplaceAll(filepath.ToSlash(srtPath), ":", "\\:")
		// Subtitles for 16:9 main video
		subFilter = fmt.Sprintf("subtitles='%s':force_style='Alignment=2,FontSize=20,Bold=1,PrimaryColour=&H00FFFFFF,OutlineColour=&H00000000,Outline=2,MarginV=25'", formattedSrt)
		// Crop filter for TikTok (9:16) without blur background + larger subtitles (FontSize=32)
		cropShorts := "scale=1080:1920:force_original_aspect_ratio=increase,crop=1080:1920"
		shortsSubFilter := fmt.Sprintf("subtitles='%s':force_style='Alignment=2,FontSize=32,Bold=1,PrimaryColour=&H00FFFFFF,OutlineColour=&H00000000,Outline=3,MarginV=65'", formattedSrt)
		shortsVideoFilter = cropShorts + "," + shortsSubFilter
	} else {
		subFilter = "null"
		shortsVideoFilter = "scale=1080:1920:force_original_aspect_ratio=increase,crop=1080:1920"
	}

	// The 9:16 cut is what goes to TikTok, so it's capped to short-form
	// length (SHORTS_MAX_SECONDS, default 60s - the hook-first opening of
	// the script). It used to be the ENTIRE 20-30 minute video cropped
	// vertically, which is neither a "Short" nor something TikTok accepts.
	var shortsDurationArgs []string
	if max := config.AppConfig.ShortsMaxSeconds; max > 0 {
		shortsDurationArgs = []string{"-t", fmt.Sprintf("%d", max)}
	}

	// Procedural noise source with lower background noise amplitude (0.25)
	noiseSource := fmt.Sprintf("anoisesrc=color=%s:sample_rate=44100:amplitude=0.25", noiseColor)
	// Reduced background music volume (musicMixLevel * 0.35) and boosted narrator voice ([0:a]volume=1.45)
	bgMusicVolume := musicMixLevel * 0.35

	mixCmd := exec.Command("ffmpeg", "-y",
		"-i", rawMergedPath,
		"-f", "lavfi", "-i", noiseSource,
		"-filter_complex", fmt.Sprintf("[0:a]volume=1.45[a0];[1:a]volume=%f[a1];[a0][a1]amix=inputs=2:duration=first[a]", bgMusicVolume),
		"-map", "0:v",
		"-map", "[a]",
		"-c:v", "libx264",
		"-crf", "18",
		"-preset", "fast",
		"-b:v", "12M",
		"-maxrate", "16M",
		"-bufsize", "24M",
		"-c:a", "aac",
		"-b:a", "192k",
		"-ar", "44100",
		"-ac", "2",
		"-vf", subFilter,
		"-movflags", "+faststart", // moov atom first: the browser preview can start playing before the whole file downloads
		finalLongPath,
	)
	shortsArgs := []string{"-y",
		"-i", rawMergedPath,
		"-f", "lavfi", "-i", noiseSource,
		"-filter_complex", fmt.Sprintf("[0:v]%s[vout];[0:a]volume=1.45[a0];[1:a]volume=%f[a1];[a0][a1]amix=inputs=2:duration=first[aout]", shortsVideoFilter, bgMusicVolume),
		"-map", "[vout]",
		"-map", "[aout]",
	}
	shortsArgs = append(shortsArgs, shortsDurationArgs...)
	shortsArgs = append(shortsArgs,
		"-c:v", "libx264",
		"-crf", "20",
		"-preset", "fast",
		"-b:v", "8M",
		"-maxrate", "10M",
		"-bufsize", "16M",
		"-c:a", "aac",
		"-b:a", "192k",
		"-ar", "44100",
		"-ac", "2",
		"-movflags", "+faststart",
		finalShortPath,
	)
	shortsCmd := exec.Command("ffmpeg", shortsArgs...)

	var mixOut, shortsOut []byte
	var mixErr, shortsErr error
	var wg2 sync.WaitGroup
	wg2.Add(2)
	go func() {
		defer wg2.Done()
		mixOut, mixErr = mixCmd.CombinedOutput()
	}()
	go func() {
		defer wg2.Done()
		shortsOut, shortsErr = shortsCmd.CombinedOutput()
	}()
	wg2.Wait()

	if mixErr != nil {
		log.Printf("FFmpeg mix/subtitles error: %v\nOutput: %s", mixErr, string(mixOut))
		failVideo(&video, fmt.Sprintf("Failed to compile final video with audio/subtitles: %v", mixErr))
		return
	}

	if shortsErr != nil {
		log.Printf("FFmpeg Shorts error: %v\nOutput: %s", shortsErr, string(shortsOut))
		// We don't fail the whole video if only shorts fails, but log it
		video.ErrorMessage = fmt.Sprintf("Shorts generation failed: %v", shortsErr)
	} else {
		video.ShortsPath = finalizeOutputFile(finalShortPath, fmt.Sprintf("output/video_shorts_%s.mp4", video.ID), "video/mp4")
	}

	// Clean up temporary files
	cleanTempFiles(video.ID)

	// Save completed state (uploads the long-form render to R2 when configured, otherwise keeps it local)
	video.VideoPath = finalizeOutputFile(finalLongPath, fmt.Sprintf("output/video_long_%s.mp4", video.ID), "video/mp4")
	video.SubtitlesPath = finalizeOutputFile(srtPath, fmt.Sprintf("output/subtitles_%s.srt", video.ID), "text/plain")
	video.Status = "ready"
	video.RenderProgress = 100
	video.RenderStage = "Concluído"
	video.DegradedSceneCount = degradedScenes
	video.TotalSceneCount = len(video.Scenes)
	if video.ShortsPath != "" {
		// Only clear a stale error once the video is actually fully usable -
		// a shorts-generation warning set above must survive this save.
		video.ErrorMessage = ""
	}
	db.DB.Save(&video)

	// Source scene assets (images/audio/video clips) are no longer needed once
	// they've been baked into the final render - only the rendered outputs
	// (VideoPath/ShortsPath) need to persist in the bucket from here on.
	purgeSceneAssets(&video)

	log.Printf("Video rendering successfully finished for ID: %s", video.ID)
}

// purgeSceneAssets deletes every scene's source assets (image, narration
// audio, stock video clip) from storage once the final video has been
// rendered, and clears the DB fields so nothing downstream (e.g. a manual
// DeleteVideo) tries to delete them again.
func purgeSceneAssets(video *models.Video) {
	for _, scene := range video.Scenes {
		for _, path := range []string{scene.ImagePath, scene.AudioPath, scene.VideoClipURL} {
			if path == "" {
				continue
			}
			if err := DeleteMedia(path); err != nil {
				log.Printf("Warning: failed to purge scene asset %s for video %s: %v", path, video.ID, err)
			}
		}
	}
	db.DB.Model(&models.Scene{}).Where("video_id = ?", video.ID).
		Updates(map[string]interface{}{"image_path": "", "audio_path": "", "video_clip_url": ""})
}

// finalizeOutputFile uploads a locally-rendered file to R2 (when configured)
// and removes the local copy, or leaves it in place under ./media otherwise.
func finalizeOutputFile(localPath, relativeKey, contentType string) string {
	if !storageConfigured() {
		return localPath
	}
	data, err := os.ReadFile(localPath)
	if err != nil {
		log.Printf("R2: failed to read render output %s, keeping local file: %v", localPath, err)
		return localPath
	}
	url, err := SaveMedia(data, relativeKey, contentType)
	if err != nil {
		log.Printf("R2: failed to upload render output %s, keeping local file: %v", localPath, err)
		return localPath
	}
	_ = os.Remove(localPath)
	return url
}

// progressPercent turns a "completed N of totalSteps" count into 0-99 (100 is
// reserved for the final "Concluído" update once the video is actually saved).
func progressPercent(stepsDone, totalSteps int) int {
	if totalSteps <= 0 {
		return 0
	}
	pct := int(float64(stepsDone) / float64(totalSteps) * 100)
	if pct > 99 {
		pct = 99
	}
	return pct
}

// updateRenderProgress writes a lightweight progress update so the frontend
// can show a real (step-based) progress bar instead of a generic spinner.
func updateRenderProgress(videoID string, percent int, stage string) {
	db.DB.Model(&models.Video{}).Where("id = ?", videoID).Updates(map[string]interface{}{
		"render_progress": percent,
		"render_stage":    stage,
	})
}

func failVideo(video *models.Video, errMsg string) {
	log.Printf("Video rendering FAILED: %s", errMsg)
	video.Status = "failed"
	video.ErrorMessage = errMsg
	db.DB.Save(video)
	cleanTempFiles(video.ID)
}

func cleanTempFiles(videoID string) {
	files, err := filepath.Glob(filepath.Join("media", "temp", "*"+videoID+"*"))
	if err != nil {
		return
	}
	for _, f := range files {
		_ = os.Remove(f)
	}
}

func formatSRTTime(seconds float64) string {
	hours := int(seconds / 3600)
	minutes := int((seconds - float64(hours*3600)) / 60)
	secs := int(seconds - float64(hours*3600) - float64(minutes*60))
	millis := int((seconds - float64(int(seconds))) * 1000)
	return fmt.Sprintf("%02d:%02d:%02d,%03d", hours, minutes, secs, millis)
}

// splitTextIntoSubtitleChunks splits a long script/narration into clean, short
// subtitle chunks of ~3-6 words (max ~35 characters per chunk).
// It respects sentence punctuation (, . ! ? ; :) so clauses break naturally.
func splitTextIntoSubtitleChunks(text string) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}

	words := strings.Fields(text)
	if len(words) == 0 {
		return nil
	}

	const maxWordsPerChunk = 6
	const maxCharsPerChunk = 35

	var chunks []string
	var currentChunk []string
	currentLen := 0

	for _, word := range words {
		wordLen := len([]rune(word))
		shouldSplit := false

		if len(currentChunk) > 0 {
			if len(currentChunk) >= maxWordsPerChunk || (currentLen+1+wordLen) > maxCharsPerChunk {
				shouldSplit = true
			} else {
				prevWord := currentChunk[len(currentChunk)-1]
				if len(currentChunk) >= 3 && (strings.HasSuffix(prevWord, ",") ||
					strings.HasSuffix(prevWord, ".") ||
					strings.HasSuffix(prevWord, "!") ||
					strings.HasSuffix(prevWord, "?") ||
					strings.HasSuffix(prevWord, ";") ||
					strings.HasSuffix(prevWord, ":")) {
					shouldSplit = true
				}
			}
		}

		if shouldSplit {
			chunks = append(chunks, strings.Join(currentChunk, " "))
			currentChunk = nil
			currentLen = 0
		}

		currentChunk = append(currentChunk, word)
		if currentLen == 0 {
			currentLen = wordLen
		} else {
			currentLen += 1 + wordLen
		}
	}

	if len(currentChunk) > 0 {
		chunks = append(chunks, strings.Join(currentChunk, " "))
	}

	return chunks
}

// appendSubtitleEntries chunks text and appends formatted SRT entries to subtitleLines.
// It allocates timing for each chunk proportionally based on character length across duration.
func appendSubtitleEntries(subtitleLines *[]string, text string, startSec, duration float64, subtitleIdx *int) float64 {
	text = strings.TrimSpace(text)
	if text == "" || duration <= 0 {
		return startSec + duration
	}

	chunks := splitTextIntoSubtitleChunks(text)
	if len(chunks) == 0 {
		return startSec + duration
	}

	totalChars := 0
	for _, chunk := range chunks {
		totalChars += len([]rune(chunk))
	}
	if totalChars == 0 {
		return startSec + duration
	}

	currSec := startSec
	for i, chunk := range chunks {
		(*subtitleIdx)++
		var chunkDuration float64
		if i == len(chunks)-1 {
			chunkDuration = (startSec + duration) - currSec
		} else {
			ratio := float64(len([]rune(chunk))) / float64(totalChars)
			chunkDuration = duration * ratio
		}

		endSec := currSec + chunkDuration
		startTimeStr := formatSRTTime(currSec)
		endTimeStr := formatSRTTime(endSec)

		*subtitleLines = append(*subtitleLines, fmt.Sprintf("%d\n%s --> %s\n%s\n", *subtitleIdx, startTimeStr, endTimeStr, chunk))
		currSec = endSec
	}

	return startSec + duration
}

// fetchBGMusicForNiche picks a mood-appropriate royalty-free track (Kevin
// MacLeod/incompetech, CC BY 4.0) for the channel's niche.
//
// This used to download the exact same 1-2 generic SoundHelix demo tracks
// for every mood and just rename the file to "background_suspense.mp3" /
// "background_business.mp3" / "background_ambient.mp3" - the filename lied
// about the content, so e.g. a true-crime channel could easily end up with
// upbeat demo music. The tracks below are genuinely different per mood, and
// "crime"/"criminal"/"policial" now route to the suspense set too (a crime
// niche isn't literally "terror"/"mistério"/"dark" but needs the same mood).
// noiseColorForMood maps a video's classification to an ffmpeg anoisesrc
// noise "color" - a procedurally generated ambient bed instead of a
// downloaded music track. This used to fetch royalty-free music from
// incompetech.com; switched to generated noise because (a) melodic tracks
// loop-fatigue badly over a 20-30 minute video and compete with narration,
// (b) a continuous noise bed is the norm for true-crime/documentary-style
// content instead of music, and (c) it removes a network dependency and any
// licensing surface entirely - anoisesrc has no failure mode, so this
// function (unlike its predecessor) can't fail.
//
// Color choice per mood:
//   - suspense (dark/mystery/crime/terror): blue noise - its emphasis on
//     higher frequencies reads as sharp, hissy unease, more unsettling than
//     brown/white for this content.
//   - business (finance/negócios): pink noise - neutral, even, unobtrusive.
//   - ambient (everything else): brown noise - deep, warm, calm default.
//
// The Sonoplastia agent's mood decision (moodOverride) takes precedence when
// present and valid; otherwise the niche text itself is classified via
// keyword matching, so there's always a real classification behind the
// color choice, never a hardcoded fallback color.
func noiseColorForMood(moodOverride, niche string) string {
	colorByMood := map[string]string{
		"suspense": "blue",
		"business": "pink",
		"ambient":  "brown",
	}

	mood := moodOverride
	if _, ok := colorByMood[mood]; !ok {
		nicheLower := strings.ToLower(niche)
		switch {
		case strings.Contains(nicheLower, "terror") || strings.Contains(nicheLower, "mistério") ||
			strings.Contains(nicheLower, "dark") || strings.Contains(nicheLower, "crime") ||
			strings.Contains(nicheLower, "criminal") || strings.Contains(nicheLower, "policial"):
			mood = "suspense"
		case strings.Contains(nicheLower, "finanças") || strings.Contains(nicheLower, "negócios"):
			mood = "business"
		default:
			mood = "ambient"
		}
	}

	return colorByMood[mood]
}

func fileExists(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func generateSilentAudio(destPath string, duration float64) error {
	os.MkdirAll(filepath.Dir(destPath), 0755)
	cmd := exec.Command("ffmpeg", "-y",
		"-f", "lavfi",
		"-i", "anullsrc=channel_layout=stereo:sample_rate=44100",
		"-t", fmt.Sprintf("%f", duration),
		"-c:a", "libmp3lame",
		destPath,
	)
	return cmd.Run()
}

// resolveFallbackVisual guarantees a non-empty local visual file when a scene
// has no generated image/clip. External stock fallback URLs (e.g. Mixkit) can
// return HTTP 403, so when downloading fails we synthesize a local 1920x1080
// dark frame via FFmpeg instead of handing "" to ffmpeg (exit status 254).
func resolveFallbackVisual(videoID string, sceneIdx int) (path string, isImage bool, err error) {
	if fallbackURL := getFallbackStockVideoURL(false); fallbackURL != "" {
		if local := ensureLocalFile(fallbackURL, "video", fmt.Sprintf("fallback_%s_%d", videoID, sceneIdx)); local != "" {
			return local, false, nil
		}
	}
	fallbackImg := filepath.Join("media", "temp", fmt.Sprintf("fallback_img_%s_%d.png", videoID, sceneIdx))
	if err := generateFallbackFrame(fallbackImg); err != nil {
		return "", true, err
	}
	return fallbackImg, true, nil
}

func generateFallbackFrame(destPath string) error {
	if fileExists(destPath) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
		return err
	}
	cmd := exec.Command("ffmpeg", "-y",
		"-f", "lavfi",
		"-i", "color=c=#0b0f17:s=1920x1080:d=1",
		"-vf", "vignette=PI/4",
		"-frames:v", "1",
		"-update", "1",
		destPath,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg fallback frame failed: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func ensureLocalFile(urlOrPath, category, tempID string) string {
	if urlOrPath == "" {
		return ""
	}
	if !strings.HasPrefix(urlOrPath, "http://") && !strings.HasPrefix(urlOrPath, "https://") {
		if fileExists(urlOrPath) {
			return urlOrPath
		}
		return ""
	}

	ext := ".mp4"
	if category == "image" {
		ext = ".jpg"
	} else if category == "audio" {
		ext = ".mp3"
	}

	localPath := filepath.Join("media", "temp", fmt.Sprintf("dl_%s_%s%s", tempID, category, ext))
	if fileExists(localPath) {
		return localPath
	}

	err := downloadToLocal(urlOrPath, localPath)
	if err == nil {
		return localPath
	}
	log.Printf("Failed to download remote asset %s: %v", urlOrPath, err)
	return ""
}
