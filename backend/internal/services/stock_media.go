package services

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"time"

	"makinadark/backend/internal/config"
)

func getPexelsAPIKey() string {
	apiKey := os.Getenv("PEXELS_API_KEY")
	if apiKey == "" || apiKey == "your_pexels_api_key_here" {
		apiKey = config.AppConfig.PexelsAPIKey
	}
	if apiKey == "your_pexels_api_key_here" {
		return ""
	}
	return apiKey
}

type PexelsPhotoResponse struct {
	Photos []struct {
		ID  int `json:"id"`
		Src struct {
			Large2x string `json:"large2x"`
			Large   string `json:"large"`
			Small   string `json:"small"` // small enough to score quickly against the image-ranker without downloading full-res for every candidate
		} `json:"src"`
	} `json:"photos"`
}

// FetchStockPhoto queries the Pexels Photo Search API for a real, watermark-free
// photo and saves it through the normal media pipeline (local disk or R2,
// whichever is configured) so it behaves exactly like an AI-generated image to
// the rest of the app. Returns an error if Pexels isn't configured or nothing
// reachable was found - callers should fall back to GenerateImage.
func FetchStockPhoto(channelID, videoID, query string) (string, error) {
	apiKey := getPexelsAPIKey()
	if apiKey == "" {
		return "", fmt.Errorf("pexels not configured")
	}

	searchURL := fmt.Sprintf("https://api.pexels.com/v1/search?query=%s&per_page=5&orientation=landscape", url.QueryEscape(query))

	req, err := http.NewRequest("GET", searchURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", apiKey)

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("pexels photo api status %d: %s", resp.StatusCode, string(respBody))
	}

	var photoResp PexelsPhotoResponse
	if err := json.NewDecoder(resp.Body).Decode(&photoResp); err != nil {
		return "", err
	}
	if len(photoResp.Photos) == 0 {
		return "", fmt.Errorf("no photos found for query: %s", query)
	}

	// Score every candidate against the scene's own text (CLIP similarity via
	// the image-ranker microservice) instead of blindly taking Pexels' first
	// result - Pexels' own search ranking is keyword/tag based, not a
	// judgment of what the image actually looks like.
	idx := bestPhotoCandidateIndex(query, photoResp.Photos)

	photoURL := photoResp.Photos[idx].Src.Large2x
	if photoURL == "" {
		photoURL = photoResp.Photos[idx].Src.Large
	}
	if photoURL == "" {
		photoURL = photoResp.Photos[0].Src.Large2x
	}
	if photoURL == "" {
		return "", fmt.Errorf("pexels photo result has no usable src")
	}

	photoResp2, err := http.Get(photoURL)
	if err != nil {
		return "", err
	}
	defer photoResp2.Body.Close()
	if photoResp2.StatusCode != http.StatusOK {
		return "", fmt.Errorf("failed to download pexels photo, status %d", photoResp2.StatusCode)
	}

	RecordStockMediaRequest(channelID, videoID, "pexels")

	fileName := fmt.Sprintf("photo_%d.jpg", time.Now().UnixNano())
	return SaveMediaFromReader(photoResp2.Body, "images/"+fileName, "image/jpeg")
}

// AcquireSceneImage prefers a real, free Pexels photo (cheaper, and more
// "factual" for a real location/subject than an AI illustration) and falls
// back to AI generation (gpt-image-1.5) only when Pexels isn't configured or
// returned nothing usable for the query.
func AcquireSceneImage(channelID, videoID, prompt, category string) (string, error) {
	if photoPath, err := FetchStockPhoto(channelID, videoID, prompt); err == nil && photoPath != "" {
		return photoPath, nil
	}
	imgPath, err := GenerateImage(channelID, videoID, prompt, category)
	if err == nil && imgPath != "" {
		return imgPath, nil
	}

	return fetchFallbackStockPhoto(prompt, category)
}

// AcquireSceneAsset picks the visual asset for one scene, alternating between
// a stock video clip (odd scene index) and a stock/AI image (even scene
// index) so every rendered video mixes both media types instead of leaning
// on one. Exactly one of the two return values is non-empty on success.
func AcquireSceneAsset(channelID, videoID, prompt, category string, index int) (imagePath, videoClipURL string) {
	if index%2 == 1 {
		if clipURL, err := FetchWatermarkFreeVideoClip(channelID, videoID, prompt, false); err == nil && clipURL != "" {
			return "", clipURL
		}
	}
	if imgPath, err := AcquireSceneImage(channelID, videoID, prompt, category); err == nil && imgPath != "" {
		return imgPath, ""
	}
	return "", ""
}

func fetchFallbackStockPhoto(prompt, category string) (string, error) {
	return generatePollinationsImage(prompt, category)
}

type PexelsVideoResponse struct {
	Videos []struct {
		ID         int    `json:"id"`
		Width      int    `json:"width"`
		Height     int    `json:"height"`
		Image      string `json:"image"` // static poster/preview frame Pexels generates for the video - used to score the clip without downloading or decoding any video
		VideoFiles []struct {
			ID       int    `json:"id"`
			Quality  string `json:"quality"`
			FileType string `json:"file_type"`
			Width    int    `json:"width"`
			Height   int    `json:"height"`
			Link     string `json:"link"`
		} `json:"video_files"`
	} `json:"videos"`
}

// FetchWatermarkFreeVideoClip queries Pexels Video API for a high quality HD
// video clip, downloads it and saves it through the normal media pipeline
// (local disk or R2, whichever is configured) so it behaves like any other
// asset the app owns - same as FetchStockPhoto does for images. This also
// means the render step no longer depends on the Pexels CDN staying up: once
// downloaded, the clip is served from our own storage. The candidate URL is
// verified reachable before downloading, since a dead link would otherwise
// only fail deep in the download/render step (the mixkit.co fallback URLs,
// for example, now return 403 from CloudFront/S3).
// Returns an error if nothing reachable was found, so callers can fall back to an image.
func FetchWatermarkFreeVideoClip(channelID, videoID, query string, isPortrait bool) (string, error) {
	apiKey := getPexelsAPIKey()
	if apiKey == "" {
		return "", fmt.Errorf("pexels not configured")
	}

	clipURL, err := callPexelsVideoAPI(query, isPortrait, apiKey)
	if err != nil || clipURL == "" || !isURLReachable(clipURL) {
		log.Printf("Pexels API error/empty/unreachable: %v", err)
		return "", fmt.Errorf("no stock video clip found for query %q", query)
	}

	resp, err := http.Get(clipURL)
	if err != nil {
		return "", fmt.Errorf("failed to download pexels video clip: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("failed to download pexels video clip, status %d", resp.StatusCode)
	}

	RecordStockMediaRequest(channelID, videoID, "pexels")

	fileName := fmt.Sprintf("clip_%d.mp4", time.Now().UnixNano())
	return SaveMediaFromReader(resp.Body, "videos/"+fileName, "video/mp4")
}

// isURLReachable does a lightweight HEAD check with a short timeout so we never
// hand FFmpeg a dead link (which would only fail deep into the render pipeline).
func isURLReachable(rawURL string) bool {
	client := &http.Client{Timeout: 6 * time.Second}
	req, err := http.NewRequest("HEAD", rawURL, nil)
	if err != nil {
		return false
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 400
}

func callPexelsVideoAPI(query string, isPortrait bool, apiKey string) (string, error) {
	orientation := "landscape"
	if isPortrait {
		orientation = "portrait"
	}

	searchURL := fmt.Sprintf("https://api.pexels.com/videos/search?query=%s&per_page=5&orientation=%s",
		url.QueryEscape(query), orientation)

	req, err := http.NewRequest("GET", searchURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", apiKey)

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("pexels api status %d: %s", resp.StatusCode, string(respBody))
	}

	var pexelsResp PexelsVideoResponse
	if err := json.NewDecoder(resp.Body).Decode(&pexelsResp); err != nil {
		return "", err
	}

	if len(pexelsResp.Videos) == 0 {
		return "", fmt.Errorf("no videos found for query: %s", query)
	}

	// One candidate per video (its best-quality mp4 file), not one per file -
	// a single video can list several resolutions of the same footage, and
	// scoring each of those separately would just waste ranker calls on
	// duplicates of the same content.
	var posterURLs []string
	var clipLinks []string
	for _, video := range pexelsResp.Videos {
		link := bestPexelsVideoFileLink(video.VideoFiles)
		if link == "" || video.Image == "" {
			continue
		}
		posterURLs = append(posterURLs, shrinkPexelsImageURL(video.Image, 150))
		clipLinks = append(clipLinks, link)
	}

	if len(clipLinks) == 0 {
		return "", fmt.Errorf("no valid mp4 link found")
	}

	// Score each video's poster/preview frame against the scene's prompt
	// (CLIP similarity via the image-ranker microservice) instead of always
	// taking Pexels' first result - same technique as bestPhotoCandidateIndex,
	// reused here without needing to download or decode any actual video.
	best, err := rankImageCandidates(query, posterURLs)
	if err != nil {
		log.Printf("Image ranker unavailable for video selection, using Pexels' default order: %v", err)
		best = 0
	}
	if best < 0 || best >= len(clipLinks) {
		best = 0
	}

	return clipLinks[best], nil
}

// bestPexelsVideoFileLink picks the highest-quality mp4 rendition of a single
// Pexels video result (prefers hd over sd; "uhd" files are skipped - they're
// large enough to meaningfully slow down the per-scene render for no visible
// benefit at the 1080p output this app renders to).
func bestPexelsVideoFileLink(files []struct {
	ID       int    `json:"id"`
	Quality  string `json:"quality"`
	FileType string `json:"file_type"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	Link     string `json:"link"`
}) string {
	for _, f := range files {
		if f.Quality == "hd" && f.FileType == "video/mp4" && f.Link != "" {
			return f.Link
		}
	}
	for _, f := range files {
		if f.Quality == "sd" && f.FileType == "video/mp4" && f.Link != "" {
			return f.Link
		}
	}
	return ""
}

// fallbackStockVideoURLs returns the candidate list in random order so retries
// (and different scenes) don't all pile onto the same, possibly-dead, URL.
func fallbackStockVideoURLs(isPortrait bool) []string {
	verticalClips := []string{
		"https://assets.mixkit.co/videos/preview/mixkit-stars-in-the-night-sky-4009-large.mp4",
		"https://assets.mixkit.co/videos/preview/mixkit-timelapse-of-the-starry-sky-41551-large.mp4",
		"https://assets.mixkit.co/videos/preview/mixkit-clouds-and-blue-sky-2408-large.mp4",
	}
	horizontalClips := []string{
		"https://assets.mixkit.co/videos/preview/mixkit-stars-in-the-night-sky-4009-large.mp4",
		"https://assets.mixkit.co/videos/preview/mixkit-fog-over-the-trees-in-a-forest-41549-large.mp4",
		"https://assets.mixkit.co/videos/preview/mixkit-storm-clouds-in-the-sky-41552-large.mp4",
		"https://assets.mixkit.co/videos/preview/mixkit-burning-fire-embers-in-the-dark-41550-large.mp4",
		"https://assets.mixkit.co/videos/preview/mixkit-dark-cloudy-sky-with-lightning-41553-large.mp4",
	}

	if isPortrait {
		return verticalClips
	}
	return horizontalClips
}

// getFallbackStockVideoURL is used by the Shotstack cloud-render path, which
// fetches the asset itself (not FFmpeg locally), so a single pick is enough there.
func getFallbackStockVideoURL(isPortrait bool) string {
	candidates := fallbackStockVideoURLs(isPortrait)
	return candidates[0]
}
