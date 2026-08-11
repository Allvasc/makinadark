package services

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"time"

	"makinadark/backend/internal/config"
)

// shrinkPexelsImageURL rewrites a Pexels CDN image URL (photo "small" src is
// already this small; video "image" poster frames default to 1200x630) to
// request a smaller render at the given height - Pexels' image CDN accepts
// h=/w= query params to resize server-side, so this avoids downloading and
// CLIP-decoding a full-size frame just to score it as one of several
// candidates. Falls back to the original URL if it doesn't parse.
func shrinkPexelsImageURL(rawURL string, maxHeight int) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	q := u.Query()
	q.Set("h", fmt.Sprintf("%d", maxHeight))
	q.Del("w") // drop the fixed width so height alone drives a proportional resize
	u.RawQuery = q.Encode()
	return u.String()
}

// bestPhotoCandidateIndex asks the image-ranker microservice (CLIP image-text
// similarity) which of the Pexels search results actually looks like the
// scene's prompt, instead of always taking index 0. Falls back to 0 whenever
// the ranker isn't configured or the call fails - this is a quality
// improvement layered on top of a working default, never a new hard
// dependency for scene image acquisition.
func bestPhotoCandidateIndex(query string, photos []struct {
	ID  int `json:"id"`
	Src struct {
		Large2x string `json:"large2x"`
		Large   string `json:"large"`
		Small   string `json:"small"`
	} `json:"src"`
}) int {
	if len(photos) <= 1 {
		return 0
	}

	candidates := make([]string, 0, len(photos))
	for _, p := range photos {
		thumb := p.Src.Small
		if thumb == "" {
			thumb = p.Src.Large2x
		}
		candidates = append(candidates, thumb)
	}

	best, err := rankImageCandidates(query, candidates)
	if err != nil {
		log.Printf("Image ranker unavailable, using Pexels' default order: %v", err)
		return 0
	}
	return best
}

type rankImagesRequest struct {
	Query      string   `json:"query"`
	Candidates []string `json:"candidates"`
}

type rankImagesResponse struct {
	BestIndex int       `json:"best_index"`
	Scores    []float64 `json:"scores"`
}

// rankImageCandidates calls the image-ranker microservice's /rank-images
// endpoint. This sits in the scene-generation critical path, but that path
// already takes minutes per video (LLM + TTS + downloads), so a generous
// timeout that lets a real ranking finish beats bailing out early and
// silently falling back to Pexels' default order - 5 video poster frames
// (larger than photo thumbnails) took over 8s under real load, so this
// leaves real headroom rather than just bumping past the observed failure.
func rankImageCandidates(query string, candidateURLs []string) (int, error) {
	baseURL := config.AppConfig.ImageRankerURL
	if baseURL == "" {
		return 0, nil // not configured - treat as "use the default order", not an error
	}

	body, err := json.Marshal(rankImagesRequest{Query: query, Candidates: candidateURLs})
	if err != nil {
		return 0, err
	}

	req, err := http.NewRequest("POST", baseURL+"/rank-images", bytes.NewBuffer(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 25 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("image-ranker returned status %d", resp.StatusCode)
	}

	var result rankImagesResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0, err
	}
	if result.BestIndex < 0 || result.BestIndex >= len(candidateURLs) {
		return 0, nil
	}

	return result.BestIndex, nil
}
