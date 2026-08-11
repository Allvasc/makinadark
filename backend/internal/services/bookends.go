package services

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"makinadark/backend/internal/db"
	"makinadark/backend/internal/models"
)

type bookendScriptResult struct {
	IntroText string `json:"intro_text"`
	OutroText string `json:"outro_text"`
}

func updateIntroProgress(channelID string, status string, progress int, stage string, errStr string) {
	db.DB.Model(&models.Channel{}).Where("id = ?", channelID).Updates(map[string]interface{}{
		"intro_status":   status,
		"intro_progress": progress,
		"intro_stage":    stage,
		"intro_error":    errStr,
	})
}

func updateOutroProgress(channelID string, status string, progress int, stage string, errStr string) {
	db.DB.Model(&models.Channel{}).Where("id = ?", channelID).Updates(map[string]interface{}{
		"outro_status":   status,
		"outro_progress": progress,
		"outro_stage":    stage,
		"outro_error":    errStr,
	})
}

// GenerateIntro creates or updates only the intro clip for a channel.
// If customScript is provided, it is used directly; otherwise LLM generates the script.
func GenerateIntro(channelID string, customScript string) error {
	var channel models.Channel
	if err := db.DB.First(&channel, "id = ?", channelID).Error; err != nil {
		updateIntroProgress(channelID, "failed", 0, "Canal não encontrado", err.Error())
		return fmt.Errorf("channel %s not found: %w", channelID, err)
	}

	updateIntroProgress(channelID, "generating", 15, "Elaborando texto da abertura...", "")

	introText := strings.TrimSpace(customScript)
	if introText == "" {
		prompt := fmt.Sprintf(`Você é o roteirista do canal '%s' (nicho: '%s', persona: '%s', tom de narração: '%s').
Crie um gancho de abertura envolvente e genérico de 2 a 3 frases que abre os vídeos do canal, prendendo a atenção nos primeiros segundos.
Retorne estritamente em JSON: {"intro_text": "..."}`, channel.Name, channel.Niche, channel.Persona, channel.NarrationTone)

		type introResult struct {
			IntroText string `json:"intro_text"`
		}
		var res introResult
		jsonStr, err := CallLLM(channel.ID, "", prompt)
		if err == nil && json.Unmarshal([]byte(jsonStr), &res) == nil && strings.TrimSpace(res.IntroText) != "" {
			introText = strings.TrimSpace(res.IntroText)
		} else {
			introText = fmt.Sprintf("Seja muito bem-vindo ao %s. Vamos ao vídeo de hoje!", channel.Name)
		}
	}

	updateIntroProgress(channelID, "generating", 30, "Revisor de Textos: corrigindo texto da abertura...", "")
	introText = reviewBookendText(channel.ID, "abertura do canal (intro)", introText)

	updateIntroProgress(channelID, "generating", 50, "Gerando narração em áudio (TTS)...", "")

	introAudioPath, err := GenerateVoiceOver(channel.ID, "", introText, 0)
	if err != nil {
		updateIntroProgress(channelID, "failed", 0, "Falha na narração", err.Error())
		return fmt.Errorf("intro voiceover failed: %w", err)
	}

	visualPath := channel.BannerPath
	if visualPath == "" {
		visualPath = channel.LogoPath
	}
	if visualPath == "" {
		updateIntroProgress(channelID, "failed", 0, "Logotipo/Banner ausente", "Adicione um logotipo ou capa ao canal primeiro")
		return fmt.Errorf("visual path (banner or logo) is missing for channel")
	}

	updateIntroProgress(channelID, "generating", 80, "Renderizando vídeo da abertura (FFmpeg)...", "")

	introVideoPath, err := renderBookendClip(channel.ID, "intro", visualPath, introAudioPath)
	if err != nil {
		updateIntroProgress(channelID, "failed", 0, "Falha ao renderizar vídeo", err.Error())
		return fmt.Errorf("intro render failed: %w", err)
	}

	channel.IntroScript = introText
	channel.IntroVideoPath = introVideoPath
	channel.BookendsReady = true
	channel.IntroStatus = "ready"
	channel.IntroProgress = 100
	channel.IntroStage = "Concluído"
	channel.IntroError = ""
	if err := db.DB.Save(&channel).Error; err != nil {
		return fmt.Errorf("failed to save channel intro: %w", err)
	}

	log.Printf("Intro ready for channel '%s' (path=%s)", channel.Name, introVideoPath)
	return nil
}

// GenerateOutro creates or updates only the outro clip for a channel.
// If customScript is provided, it is used directly; otherwise LLM generates the script.
func GenerateOutro(channelID string, customScript string) error {
	var channel models.Channel
	if err := db.DB.First(&channel, "id = ?", channelID).Error; err != nil {
		updateOutroProgress(channelID, "failed", 0, "Canal não encontrado", err.Error())
		return fmt.Errorf("channel %s not found: %w", channelID, err)
	}

	updateOutroProgress(channelID, "generating", 15, "Elaborando texto do encerramento...", "")

	outroText := strings.TrimSpace(customScript)
	if outroText == "" {
		prompt := fmt.Sprintf(`Você é o roteirista do canal '%s' (nicho: '%s', persona: '%s', tom de narração: '%s').
Crie uma chamada para ação de encerramento de 3 a 4 frases que fecha os vídeos do canal - peça para o espectador se inscrever, curtir e comentar o que achou.
Retorne estritamente em JSON: {"outro_text": "..."}`, channel.Name, channel.Niche, channel.Persona, channel.NarrationTone)

		type outroResult struct {
			OutroText string `json:"outro_text"`
		}
		var res outroResult
		jsonStr, err := CallLLM(channel.ID, "", prompt)
		if err == nil && json.Unmarshal([]byte(jsonStr), &res) == nil && strings.TrimSpace(res.OutroText) != "" {
			outroText = strings.TrimSpace(res.OutroText)
		} else {
			outroText = fmt.Sprintf("Se você gostou deste conteúdo, inscreva-se no %s, deixe seu gostei e comente qual tema você quer ver nos próximos vídeos!", channel.Name)
		}
	}

	updateOutroProgress(channelID, "generating", 30, "Revisor de Textos: corrigindo texto do encerramento...", "")
	outroText = reviewBookendText(channel.ID, "encerramento do canal (chamada para ação)", outroText)

	updateOutroProgress(channelID, "generating", 50, "Gerando narração em áudio (TTS)...", "")

	outroAudioPath, err := GenerateVoiceOver(channel.ID, "", outroText, 1)
	if err != nil {
		updateOutroProgress(channelID, "failed", 0, "Falha na narração", err.Error())
		return fmt.Errorf("outro voiceover failed: %w", err)
	}

	visualPath := channel.BannerPath
	if visualPath == "" {
		visualPath = channel.LogoPath
	}
	if visualPath == "" {
		updateOutroProgress(channelID, "failed", 0, "Logotipo/Banner ausente", "Adicione um logotipo ou capa ao canal primeiro")
		return fmt.Errorf("visual path (banner or logo) is missing for channel")
	}

	updateOutroProgress(channelID, "generating", 80, "Renderizando vídeo do encerramento (FFmpeg)...", "")

	outroVideoPath, err := renderBookendClip(channel.ID, "outro", visualPath, outroAudioPath)
	if err != nil {
		updateOutroProgress(channelID, "failed", 0, "Falha ao renderizar vídeo", err.Error())
		return fmt.Errorf("outro render failed: %w", err)
	}

	channel.OutroScript = outroText
	channel.OutroVideoPath = outroVideoPath
	channel.BookendsReady = true
	channel.OutroStatus = "ready"
	channel.OutroProgress = 100
	channel.OutroStage = "Concluído"
	channel.OutroError = ""
	if err := db.DB.Save(&channel).Error; err != nil {
		return fmt.Errorf("failed to save channel outro: %w", err)
	}

	log.Printf("Outro ready for channel '%s' (path=%s)", channel.Name, outroVideoPath)
	return nil
}

// DeleteIntro removes the intro script and clip from the channel.
func DeleteIntro(channelID string) error {
	var channel models.Channel
	if err := db.DB.First(&channel, "id = ?", channelID).Error; err != nil {
		return fmt.Errorf("channel %s not found: %w", channelID, err)
	}

	if channel.IntroVideoPath != "" && !strings.HasPrefix(channel.IntroVideoPath, "http") {
		_ = os.Remove(channel.IntroVideoPath)
	}
	channel.IntroScript = ""
	channel.IntroVideoPath = ""
	channel.IntroStatus = "idle"
	channel.IntroProgress = 0
	channel.IntroStage = ""
	channel.IntroError = ""
	return db.DB.Save(&channel).Error
}

// DeleteOutro removes the outro script and clip from the channel.
func DeleteOutro(channelID string) error {
	var channel models.Channel
	if err := db.DB.First(&channel, "id = ?", channelID).Error; err != nil {
		return fmt.Errorf("channel %s not found: %w", channelID, err)
	}

	if channel.OutroVideoPath != "" && !strings.HasPrefix(channel.OutroVideoPath, "http") {
		_ = os.Remove(channel.OutroVideoPath)
	}
	channel.OutroScript = ""
	channel.OutroVideoPath = ""
	channel.OutroStatus = "idle"
	channel.OutroProgress = 0
	channel.OutroStage = ""
	channel.OutroError = ""
	return db.DB.Save(&channel).Error
}

// GenerateChannelBookends creates a channel's intro and outro clips once.
func GenerateChannelBookends(channelID string) {
	if err := GenerateIntro(channelID, ""); err != nil {
		log.Printf("GenerateChannelBookends intro error for channel %s: %v", channelID, err)
	}
	if err := GenerateOutro(channelID, ""); err != nil {
		log.Printf("GenerateChannelBookends outro error for channel %s: %v", channelID, err)
	}
}

// renderBookendClip composes one static-image-plus-audio clip using the same
// FFmpeg encode settings as a regular scene segment (video.go), so it
// stream-copies cleanly alongside scene segments during the later concat
// step in CompileVideo.
//
// The edge of the clip that touches real video content - the END of the
// intro, the START of the outro - gets a longer, more dramatic fade
// (boundaryFade) than a normal scene-to-scene cut (shortFade, matching the
// 0.4s scenes use). The outer edge (very start of the intro, very end of the
// outro) keeps the short fade. CompileVideo's own scene fade logic already
// disables the matching fade-in on scene 0 / fade-out on the last scene
// whenever a bookend is present specifically so the two don't double up -
// the transition happens entirely on the bookend's side. The ambient noise
// bed needs no fade of its own here: it's mixed once, continuously, across
// the ENTIRE final concatenated video (bookends included) at render time in
// CompileVideo, using that video's own MusicMood/MusicMixLevel - so it's
// already present at the right level under every bookend from its first
// frame, with no separate jump to smooth over.
func renderBookendClip(channelID, kind, imagePath, audioPath string) (string, error) {
	if !CheckFFmpeg() {
		return "", fmt.Errorf("ffmpeg not available")
	}

	duration, err := GetAudioDuration(audioPath)
	if err != nil || duration <= 0 {
		duration = 5.0
	}

	const shortFade = 0.4
	const boundaryFade = 1.2
	fadeInDur, fadeOutDur := shortFade, shortFade
	switch kind {
	case "intro":
		fadeOutDur = boundaryFade
	case "outro":
		fadeInDur = boundaryFade
	}
	if fadeInDur > duration/2 {
		fadeInDur = duration / 2
	}
	if fadeOutDur > duration/2 {
		fadeOutDur = duration / 2
	}
	fadeOutStart := duration - fadeOutDur
	if fadeOutStart < 0 {
		fadeOutStart = 0
	}
	videoFilter := fmt.Sprintf("scale=1920:1080,fade=t=in:st=0:d=%f,fade=t=out:st=%f:d=%f", fadeInDur, fadeOutStart, fadeOutDur)
	audioFilter := fmt.Sprintf("afade=t=in:st=0:d=%f,afade=t=out:st=%f:d=%f", fadeInDur, fadeOutStart, fadeOutDur)

	if err := os.MkdirAll("./media/temp", 0755); err != nil {
		return "", err
	}
	localPath := filepath.Join("media", "temp", fmt.Sprintf("bookend_%s_%s.mp4", kind, channelID))

	cmd := exec.Command("ffmpeg", "-y",
		"-loop", "1",
		"-i", imagePath,
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
		"-af", audioFilter,
		localPath,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("ffmpeg bookend render failed: %v: %s", err, string(out))
	}

	// finalizeOutputFile uploads to R2 and removes the local copy when
	// storage is configured; otherwise it leaves the file at localPath.
	return finalizeOutputFile(localPath, fmt.Sprintf("output/bookend_%s_%s.mp4", kind, channelID), "video/mp4"), nil
}

// downloadToLocal fetches a stored media path (an R2 URL or an already-local
// "media/..." path) into a fresh local file, since FFmpeg's concat demuxer
// needs plain local files alongside the scene segments it stitches together.
func downloadToLocal(pathOrURL, destPath string) error {
	if strings.HasPrefix(pathOrURL, "http://") || strings.HasPrefix(pathOrURL, "https://") {
		client := &http.Client{Timeout: 30 * time.Second}
		resp, err := client.Get(pathOrURL)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("status %d fetching %s", resp.StatusCode, pathOrURL)
		}
		out, err := os.Create(destPath)
		if err != nil {
			return err
		}
		defer out.Close()
		_, err = io.Copy(out, resp.Body)
		return err
	}

	data, err := os.ReadFile(pathOrURL)
	if err != nil {
		return err
	}
	return os.WriteFile(destPath, data, 0644)
}
