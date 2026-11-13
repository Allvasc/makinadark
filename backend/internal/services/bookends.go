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

// bookendSpec captures everything that differs between the intro and the
// outro, so both go through one implementation (they used to be two
// copy-pasted ~70 line functions that had already started to drift).
type bookendSpec struct {
	kind        string // "intro" | "outro" - also the DB column prefix
	ttsIndex    int
	reviewLabel string
	prompt      string // %s x4: name, niche, persona, tone
	fallback    string // %s: channel name
	startStage  string
	ttsStage    string
	renderStage string
}

var introSpec = bookendSpec{
	kind:        "intro",
	ttsIndex:    0,
	reviewLabel: "abertura do canal (intro)",
	prompt: `Você é o roteirista do canal '%s' (nicho: '%s', persona: '%s', tom de narração: '%s').
Crie um gancho de abertura envolvente e genérico de 2 a 3 frases que abre os vídeos do canal, prendendo a atenção nos primeiros segundos.
Retorne estritamente em JSON: {"text": "..."}`,
	fallback:    "Seja muito bem-vindo ao %s. Vamos ao vídeo de hoje!",
	startStage:  "Elaborando texto da abertura...",
	ttsStage:    "Gerando narração da abertura (TTS)...",
	renderStage: "Renderizando vídeo da abertura (FFmpeg)...",
}

var outroSpec = bookendSpec{
	kind:        "outro",
	ttsIndex:    1,
	reviewLabel: "encerramento do canal (chamada para ação)",
	prompt: `Você é o roteirista do canal '%s' (nicho: '%s', persona: '%s', tom de narração: '%s').
Crie uma chamada para ação de encerramento de 3 a 4 frases que fecha os vídeos do canal - peça para o espectador se inscrever, curtir e comentar o que achou.
Retorne estritamente em JSON: {"text": "..."}`,
	fallback:    "Se você gostou deste conteúdo, inscreva-se no %s, deixe seu gostei e comente qual tema você quer ver nos próximos vídeos!",
	startStage:  "Elaborando texto do encerramento...",
	ttsStage:    "Gerando narração do encerramento (TTS)...",
	renderStage: "Renderizando vídeo do encerramento (FFmpeg)...",
}

// updateBookendColumns writes ONLY the given kind's columns. Every write in
// this file goes through here - never db.Save(&channel) on a copy loaded
// minutes earlier, which silently reverted concurrent changes (a finished
// outro, a new logo, an auto-pilot toggle, fresh OAuth tokens...).
func updateBookendColumns(channelID, kind string, fields map[string]interface{}) {
	cols := make(map[string]interface{}, len(fields))
	for k, v := range fields {
		cols[kind+"_"+k] = v
	}
	if err := db.DB.Model(&models.Channel{}).Where("id = ?", channelID).Updates(cols).Error; err != nil {
		log.Printf("Bookends: failed to update %s columns for channel %s: %v", kind, channelID, err)
	}
}

func updateBookendProgress(channelID, kind, status string, progress int, stage, errStr string) {
	updateBookendColumns(channelID, kind, map[string]interface{}{
		"status":   status,
		"progress": progress,
		"stage":    stage,
		"error":    errStr,
	})
}

func updateIntroProgress(channelID string, status string, progress int, stage string, errStr string) {
	updateBookendProgress(channelID, "intro", status, progress, stage, errStr)
}

func updateOutroProgress(channelID string, status string, progress int, stage string, errStr string) {
	updateBookendProgress(channelID, "outro", status, progress, stage, errStr)
}

// GenerateIntro creates or updates only the intro clip for a channel.
// If customScript is provided, it is used directly; otherwise LLM generates the script.
func GenerateIntro(channelID string, customScript string) error {
	return generateBookend(introSpec, channelID, customScript)
}

// GenerateOutro creates or updates only the outro clip for a channel.
// If customScript is provided, it is used directly; otherwise LLM generates the script.
func GenerateOutro(channelID string, customScript string) error {
	return generateBookend(outroSpec, channelID, customScript)
}

func generateBookend(spec bookendSpec, channelID, customScript string) error {
	fail := func(stage string, err error) error {
		updateBookendProgress(channelID, spec.kind, "failed", 0, stage, err.Error())
		return fmt.Errorf("%s: %s: %w", spec.kind, stage, err)
	}

	var channel models.Channel
	if err := db.DB.First(&channel, "id = ?", channelID).Error; err != nil {
		return fail("Canal não encontrado", err)
	}

	updateBookendProgress(channelID, spec.kind, "generating", 15, spec.startStage, "")

	text := strings.TrimSpace(customScript)
	if text == "" {
		prompt := fmt.Sprintf(spec.prompt, channel.Name, channel.Niche, channel.Persona, channel.NarrationTone)
		var res struct {
			Text string `json:"text"`
		}
		jsonStr, err := CallLLM(channel.ID, "", prompt)
		if err == nil && json.Unmarshal([]byte(stripJSONFences(jsonStr)), &res) == nil {
			text = strings.TrimSpace(res.Text)
		}
		if text == "" {
			if err != nil {
				log.Printf("Bookends: LLM unavailable for %s of channel %s, using template text: %v", spec.kind, channelID, err)
			}
			text = fmt.Sprintf(spec.fallback, channel.Name)
		}
	}

	updateBookendProgress(channelID, spec.kind, "generating", 30, "Revisor de Textos: corrigindo texto...", "")
	text = reviewBookendText(channel.ID, spec.reviewLabel, text)

	updateBookendProgress(channelID, spec.kind, "generating", 50, spec.ttsStage, "")
	audioPath, err := GenerateVoiceOver(channel.ID, "", text, spec.ttsIndex)
	if err != nil {
		return fail("Falha na narração", err)
	}

	visualPath := channel.BannerPath
	if visualPath == "" {
		visualPath = channel.LogoPath
	}
	if visualPath == "" {
		_ = DeleteMedia(audioPath)
		return fail("Logotipo/Banner ausente", fmt.Errorf("adicione um logotipo ou capa ao canal primeiro"))
	}

	updateBookendProgress(channelID, spec.kind, "generating", 80, spec.renderStage, "")
	videoPath, err := renderBookendClip(channel.ID, spec.kind, visualPath, audioPath)
	// The narration is baked into the clip now (or the render failed) - either
	// way the standalone TTS file is no longer needed in storage.
	if delErr := DeleteMedia(audioPath); delErr != nil {
		log.Printf("Bookends: failed to delete temporary %s narration %s: %v", spec.kind, audioPath, delErr)
	}
	if err != nil {
		return fail("Falha ao renderizar vídeo", err)
	}

	// Replace the previous clip if it lived under a different path/URL.
	previous := channel.IntroVideoPath
	if spec.kind == "outro" {
		previous = channel.OutroVideoPath
	}
	if previous != "" && previous != videoPath {
		if delErr := DeleteMedia(previous); delErr != nil {
			log.Printf("Bookends: failed to delete previous %s clip %s: %v", spec.kind, previous, delErr)
		}
	}

	updateBookendColumns(channelID, spec.kind, map[string]interface{}{
		"script":     text,
		"video_path": videoPath,
		"status":     "ready",
		"progress":   100,
		"stage":      "Concluído",
		"error":      "",
	})
	db.DB.Model(&models.Channel{}).Where("id = ?", channelID).Update("bookends_ready", true)

	log.Printf("%s ready for channel '%s' (path=%s)", spec.kind, channel.Name, videoPath)
	return nil
}

func deleteBookend(channelID, kind string) error {
	var channel models.Channel
	if err := db.DB.First(&channel, "id = ?", channelID).Error; err != nil {
		return fmt.Errorf("channel %s not found: %w", channelID, err)
	}
	path, status := channel.IntroVideoPath, channel.IntroStatus
	if kind == "outro" {
		path, status = channel.OutroVideoPath, channel.OutroStatus
	}
	if status == "generating" {
		return ErrBookendBusy
	}
	// DeleteMedia handles both R2 URLs and local paths (the old code only
	// removed local files, so the R2 object leaked forever).
	if err := DeleteMedia(path); err != nil {
		log.Printf("Bookends: failed to delete %s clip %s: %v", kind, path, err)
	}
	updateBookendColumns(channelID, kind, map[string]interface{}{
		"script":     "",
		"video_path": "",
		"status":     "idle",
		"progress":   0,
		"stage":      "",
		"error":      "",
	})
	return nil
}

// ErrBookendBusy is returned when an intro/outro is still being generated.
var ErrBookendBusy = fmt.Errorf("aguarde a geração em andamento terminar")

// DeleteIntro removes the intro script and clip from the channel.
func DeleteIntro(channelID string) error { return deleteBookend(channelID, "intro") }

// DeleteOutro removes the outro script and clip from the channel.
func DeleteOutro(channelID string) error { return deleteBookend(channelID, "outro") }

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
// FFmpeg encode settings as a regular scene segment (video.go) - 1920x1080,
// 25fps, yuv420p, SAR 1, AAC 44.1kHz stereo - so it stream-copies cleanly
// alongside scene segments during the later concat step in CompileVideo.
//
// The edge of the clip that touches real video content - the END of the
// intro, the START of the outro - gets a longer, more dramatic fade
// (boundaryFade) than a normal scene-to-scene cut (shortFade, matching the
// 0.4s scenes use). CompileVideo disables the matching fade on scene 0 / the
// last scene whenever a bookend is present so the two don't double up. The
// ambient noise bed is mixed once across the whole final video, bookends
// included, so it needs no fade of its own here.
func renderBookendClip(channelID, kind, imagePath, audioPath string) (string, error) {
	if !CheckFFmpeg() {
		return "", fmt.Errorf("ffmpeg not available")
	}

	// Inputs may be R2 URLs - fetch them locally first instead of relying on
	// ffmpeg's network demuxers (the image2 "-loop 1" path in particular).
	localImage, cleanupImage, err := localCopy(imagePath, "bookend_img_"+channelID)
	if err != nil {
		return "", err
	}
	defer cleanupImage()
	localAudio, cleanupAudio, err := localCopy(audioPath, "bookend_aud_"+channelID)
	if err != nil {
		return "", err
	}
	defer cleanupAudio()

	duration, err := GetAudioDuration(localAudio)
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
	// scale+crop (not a bare scale=1920:1080) so a 3:1 Pollinations banner or
	// a square logo isn't stretched out of proportion.
	videoFilter := fmt.Sprintf("scale=1920:1080:force_original_aspect_ratio=increase,crop=1920:1080,setsar=1,fade=t=in:st=0:d=%f,fade=t=out:st=%f:d=%f", fadeInDur, fadeOutStart, fadeOutDur)
	audioFilter := fmt.Sprintf("afade=t=in:st=0:d=%f,afade=t=out:st=%f:d=%f", fadeInDur, fadeOutStart, fadeOutDur)

	// Rendered into media/output (not media/temp): with local storage this
	// file IS the permanent bookend, and temp/ is scratch space.
	if err := os.MkdirAll(filepath.Join("media", "output"), 0755); err != nil {
		return "", err
	}
	localPath := filepath.Join("media", "output", fmt.Sprintf("bookend_%s_%s_%d.mp4", kind, channelID, time.Now().Unix()))

	cmd := exec.Command("ffmpeg", "-y",
		"-loop", "1",
		"-i", localImage,
		"-i", localAudio,
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
		_ = os.Remove(localPath)
		return "", fmt.Errorf("ffmpeg bookend render failed: %v: %s", err, tail(string(out), 800))
	}

	// finalizeOutputFile uploads to R2 and removes the local copy when
	// storage is configured; otherwise it leaves the file at localPath.
	return finalizeOutputFile(localPath, filepath.ToSlash(filepath.Join("output", filepath.Base(localPath))), "video/mp4"), nil
}

// tail keeps error messages that embed ffmpeg's output readable in the UI -
// the useful part of ffmpeg's log is the end, not the multi-KB banner.
func tail(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return "..." + s[len(s)-max:]
}

// downloadClient is used for pulling stored media (R2 / Shotstack / stock
// clips) back to local disk. Rendered videos can be hundreds of MB, so the
// overall timeout is generous; a dead connection still fails fast via the
// response-header timeout.
var downloadClient = &http.Client{
	Timeout: 30 * time.Minute,
	Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		ResponseHeaderTimeout: 60 * time.Second,
	},
}

// downloadToLocal fetches a stored media path (an R2 URL or an already-local
// "media/..." path) into a fresh local file, since FFmpeg's concat demuxer
// needs plain local files alongside the scene segments it stitches together.
// It writes to a temp name and renames on success, so a failed/partial
// download never leaves a truncated file that a later fileExists() check
// would happily reuse.
func downloadToLocal(pathOrURL, destPath string) error {
	if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
		return err
	}
	tmpPath := destPath + ".part"
	defer os.Remove(tmpPath)

	if isRemoteURL(pathOrURL) {
		resp, err := downloadClient.Get(pathOrURL)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("status %d fetching %s", resp.StatusCode, pathOrURL)
		}
		out, err := os.Create(tmpPath)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, resp.Body); err != nil {
			out.Close()
			return err
		}
		if err := out.Close(); err != nil {
			return err
		}
		return os.Rename(tmpPath, destPath)
	}

	in, err := os.Open(pathOrURL)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(tmpPath)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, destPath)
}

func isRemoteURL(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

// localCopy returns a local filesystem path for a stored media value. Local
// paths are returned as-is (cleanup is a no-op); remote URLs (R2 bucket,
// Shotstack CDN) are downloaded into media/temp and cleanup deletes them.
//
// YouTube/TikTok uploads used to os.Stat() the stored value directly, which
// meant that as soon as R2 was configured - where every render is uploaded
// and the local copy deleted - every single publish failed with "video file
// does not exist: https://...".
func localCopy(pathOrURL, tag string) (string, func(), error) {
	noop := func() {}
	if pathOrURL == "" {
		return "", noop, fmt.Errorf("caminho de mídia vazio")
	}
	if !isRemoteURL(pathOrURL) {
		if !fileExists(pathOrURL) {
			return "", noop, fmt.Errorf("arquivo não encontrado: %s", pathOrURL)
		}
		return pathOrURL, noop, nil
	}
	ext := filepath.Ext(strings.SplitN(pathOrURL, "?", 2)[0])
	if ext == "" {
		ext = ".mp4"
	}
	dest := filepath.Join("media", "temp", fmt.Sprintf("dl_%s_%d%s", tag, time.Now().UnixNano(), ext))
	if err := downloadToLocal(pathOrURL, dest); err != nil {
		return "", noop, fmt.Errorf("falha ao baixar %s: %w", pathOrURL, err)
	}
	return dest, func() { _ = os.Remove(dest) }, nil
}
