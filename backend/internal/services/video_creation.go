package services

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"
	"makinadark/backend/internal/db"
	"makinadark/backend/internal/models"
)

// GenerateVideoScenes runs the whole script+scenes creation pipeline (topic
// pick, LLM script, per-scene image/clip + narration) in the background and
// writes progress to the video row as it goes - the same render_progress/
// render_stage fields and "generating" status the render progress bar already
// polls, so script generation gets a real progress bar for free.
//
// This used to run synchronously inside the CreateVideo HTTP handler, which
// could take several minutes (LLM call + up to 8 scenes each with an image
// and a TTS call) with zero feedback - indistinguishable from a hang.
func GenerateVideoScenes(channel models.Channel, videoID string, topic string) {
	updateRenderProgress(videoID, 2, "Escolhendo tema do vídeo...")

	if strings.TrimSpace(topic) == "" {
		var existingTitles []string
		db.DB.Model(&models.Video{}).Where("channel_id = ? AND id <> ?", channel.ID, videoID).Pluck("title", &existingTitles)

		liveTrends, _ := FetchRealTimeTrends(channel.ID, channel.Niche)
		trendsStr := strings.Join(liveTrends, ", ")

		prompt := fmt.Sprintf(`Escolha um assunto altamente viral e em tendência de busca hoje no nicho de '%s'.
Tendências de busca ao vivo hoje: [%s]
Vídeos já criados anteriormente neste canal: %v

REGRA OBRIGATÓRIA: Escolha um assunto inédito que NUNCA foi abordado nos vídeos anteriores listados acima para evitar repetição. Retorne apenas o assunto em texto simples.`,
			channel.Niche, trendsStr, existingTitles)

		newTopic, err := CallLLM(channel.ID, videoID, prompt)
		if err == nil && newTopic != "" {
			topic = strings.TrimSpace(newTopic)
		} else if len(liveTrends) > 0 {
			topic = liveTrends[0]
		} else {
			topic = fmt.Sprintf("Segredos e curiosidades ocultas de %s", channel.Niche)
		}
		log.Printf("Auto-selected non-repeating trend topic for channel '%s': %s", channel.Name, topic)
	}

	_, err := generateScriptScenesAndThumbnail(channel, videoID, topic, func(pct int, stage string) {
		updateRenderProgress(videoID, pct, stage)
	})
	if err != nil {
		markVideoFailed(videoID, err.Error())
		return
	}

	db.DB.Model(&models.Video{}).Where("id = ?", videoID).Updates(map[string]interface{}{
		"status":          "draft",
		"render_progress": 100,
		"render_stage":    "Roteiro pronto",
	})
	log.Printf("Video %s script+scenes ready (draft)", videoID)
}

// generateScriptScenesAndThumbnail generates the script, creates every scene
// (narration audio + alternating video/image asset via AcquireSceneAsset),
// and generates the video thumbnail. Shared by the manual flow above
// (GenerateVideoScenes) and Auto-Pilot (processAutoPilotChannel in
// autopilot.go) - these used to duplicate this whole loop independently and
// had drifted out of sync: Auto-Pilot never generated a thumbnail at all.
// progressFn is called after each major step; pass a no-op if the caller has
// no UI progress bar watching it live (e.g. Auto-Pilot).
func generateScriptScenesAndThumbnail(channel models.Channel, videoID, topic string, progressFn func(pct int, stage string)) (*VideoScriptResult, error) {
	// 1. Roteirista - writes the script (own JSON-validity + content-quality
	// supervisor retry loop lives inside GenerateVideoScript, ai.go).
	progressFn(6, "Roteirista: gerando roteiro por IA...")
	scriptResult, err := GenerateVideoScript(channel.ID, videoID, channel.Name, channel.Niche, topic)
	if err != nil {
		return nil, fmt.Errorf("falha ao gerar roteiro: %w", err)
	}

	// 2. Revisor de Textos - proofreads narration for spelling/grammar/encoding
	// errors before they reach TTS and burned-in subtitles. Falls back to the
	// unreviewed script on any failure (see agents.go), never blocks the video.
	progressFn(10, "Revisor de Textos: corrigindo roteiro...")
	scriptResult = reviewScriptText(channel.ID, videoID, scriptResult)

	// 3. Diretor de Cinema - stronger per-scene image prompts, music mood +
	// fade-transition decision, and a connective transition line prepended to
	// any scene that jumps to a new subject/chapter without one - fixes the
	// "cenas sem fluidez" issue where the Roteirista strings unrelated topics
	// together with no bridge between them.
	progressFn(14, "Diretor de Cinema: definindo direção visual e continuidade...")
	director := applyDirectorNotes(channel.ID, videoID, scriptResult, channel.Niche)
	if director != nil {
		imagePromptByIndex := make(map[int]string, len(director.Scenes))
		transitionByIndex := make(map[int]string, len(director.Scenes))
		for _, s := range director.Scenes {
			if s.ImagePrompt != "" {
				imagePromptByIndex[s.Index] = s.ImagePrompt
			}
			if strings.TrimSpace(s.TransitionText) != "" {
				transitionByIndex[s.Index] = strings.TrimSpace(s.TransitionText)
			}
		}
		for i := range scriptResult.Scenes {
			if prompt, ok := imagePromptByIndex[scriptResult.Scenes[i].Index]; ok {
				scriptResult.Scenes[i].ImagePrompt = prompt
			}
			if transition, ok := transitionByIndex[scriptResult.Scenes[i].Index]; ok {
				scriptResult.Scenes[i].NarrationText = joinTransition(transition, scriptResult.Scenes[i].NarrationText)
			}
		}
	}

	// 4. Sonoplastia - confirms/adjusts music mood and picks the mix level.
	progressFn(17, "Sonoplastia: definindo trilha sonora...")
	directorMood := ""
	if director != nil {
		directorMood = director.MusicMood
	}
	soundDesign := applySoundDesign(channel.ID, videoID, scriptResult, channel.Niche, directorMood)

	musicMood := directorMood
	musicMixLevel := 0.12
	if soundDesign != nil {
		musicMood = soundDesign.MusicMood
		musicMixLevel = soundDesign.MusicMixLevel
	}
	useFadeTransitions := director == nil || director.UseFadeTransitions

	// 5. Especialista em YouTube/Tendências - refines title/description/tags
	// and crafts the thumbnail prompt now that the full script exists.
	progressFn(20, "Especialista em YouTube: otimizando título e tags...")
	youtube := optimizeForYouTube(channel.ID, videoID, scriptResult, channel.Niche)
	if youtube != nil {
		scriptResult.Title = youtube.Title
		scriptResult.Description = youtube.Description
		scriptResult.Tags = youtube.Tags
	}

	db.DB.Model(&models.Video{}).Where("id = ?", videoID).Updates(map[string]interface{}{
		"title":                scriptResult.Title,
		"description":          scriptResult.Description,
		"tags":                 scriptResult.Tags,
		"script":               scriptResult.Script,
		"viral_score":          scriptResult.ViralScore,
		"hook_power_score":     scriptResult.HookPowerScore,
		"retention_score":      scriptResult.RetentionScore,
		"ctr_score":            scriptResult.CTRScore,
		"originality_score":    scriptResult.OriginalityScore,
		"score_notes":          scriptResult.ScoreNotes,
		"music_mood":           musicMood,
		"music_mix_level":      musicMixLevel,
		"use_fade_transitions": useFadeTransitions,
	})

	totalScenes := len(scriptResult.Scenes)
	for i, sceneOut := range scriptResult.Scenes {
		if strings.TrimSpace(sceneOut.NarrationText) == "" {
			log.Printf("Skipping empty scene %d with no narration text", sceneOut.Index)
			continue
		}
		pct := 22
		if totalScenes > 0 {
			pct = 22 + int(float64(i)/float64(totalScenes)*68)
		}
		progressFn(pct, fmt.Sprintf("Gerando cena %d de %d (imagem/áudio)...", i+1, totalScenes))

		audioPath, err := GenerateVoiceOver(channel.ID, videoID, sceneOut.NarrationText, sceneOut.Index)
		if err != nil {
			log.Printf("VoiceOver warning (Scene %d): %v", sceneOut.Index, err)
		}

		// Alternates video clips and images across scenes (see AcquireSceneAsset).
		imagePath, videoClipURL := AcquireSceneAsset(channel.ID, videoID, sceneOut.ImagePrompt, fmt.Sprintf("scene_%d", sceneOut.Index), sceneOut.Index)

		scene := models.Scene{
			ID:            uuid.New().String(),
			VideoID:       videoID,
			Index:         sceneOut.Index,
			NarrationText: sceneOut.NarrationText,
			ImagePath:     imagePath,
			VideoClipURL:  videoClipURL,
			AudioPath:     audioPath,
			Duration:      5.0,
			CreatedAt:     time.Now(),
		}
		if err := db.DB.Create(&scene).Error; err != nil {
			return nil, fmt.Errorf("falha ao salvar cena %d: %w", sceneOut.Index, err)
		}
	}

	progressFn(92, "Gerando thumbnail...")
	thumbPrompt := fmt.Sprintf("High CTR YouTube thumbnail for %s: %s, 4k cinematic composition, vibrant lighting, no text", channel.Niche, scriptResult.Title)
	if youtube != nil && youtube.ThumbnailPrompt != "" {
		thumbPrompt = youtube.ThumbnailPrompt
	}
	if thumbPath, err := GenerateImage(channel.ID, videoID, thumbPrompt, "thumbnail"); err == nil && thumbPath != "" {
		db.DB.Model(&models.Video{}).Where("id = ?", videoID).Update("thumbnail_path", thumbPath)
	} else if err != nil {
		log.Printf("Thumbnail generation warning for video %s: %v", videoID, err)
	}

	return scriptResult, nil
}

// joinTransition prepends the Diretor's transition line to a scene's
// narration. The Diretor is instructed to always write transition_text as a
// complete, self-contained sentence ending in terminal punctuation (./!/?),
// specifically so the narration's own capitalization never needs touching
// here - an earlier version of this function lowercased the narration's
// first letter whenever the transition ended in a comma/dash, which silently
// broke proper nouns ("Galileu" -> "galileu") whenever a scene happened to
// open with a name. Simple concatenation is the safe option; the grammar
// constraint lives in the prompt (agents.go), not in string-mangling here.
func joinTransition(transition, narration string) string {
	transition = strings.TrimSpace(transition)
	narration = strings.TrimSpace(narration)
	if transition == "" {
		return narration
	}
	return transition + " " + narration
}

func markVideoFailed(videoID, msg string) {
	log.Printf("Video %s generation FAILED: %s", videoID, msg)
	db.DB.Model(&models.Video{}).Where("id = ?", videoID).Updates(map[string]interface{}{
		"status":        "failed",
		"error_message": msg,
	})
}
