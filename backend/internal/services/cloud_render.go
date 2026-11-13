package services

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"makinadark/backend/internal/config"
	"net/http"
	"time"

	"makinadark/backend/internal/models"
)

type ShotstackRenderRequest struct {
	Timeline ShotstackTimeline `json:"timeline"`
	Output   ShotstackOutput   `json:"output"`
}

type ShotstackTimeline struct {
	Tracks []ShotstackTrack `json:"tracks"`
}

type ShotstackTrack struct {
	Clips []ShotstackClip `json:"clips"`
}

type ShotstackClip struct {
	Asset  ShotstackAsset `json:"asset"`
	Start  float64        `json:"start"`
	Length float64        `json:"length"`
}

type ShotstackAsset struct {
	Type string `json:"type"`
	SRC  string `json:"src,omitempty"`
	Text string `json:"text,omitempty"`
}

type ShotstackOutput struct {
	Format      string `json:"format"`
	Resolution  string `json:"resolution"`
	AspectRatio string `json:"aspectRatio,omitempty"`
}

type ShotstackResponse struct {
	Success  bool `json:"success"`
	Response struct {
		ID      string `json:"id"`
		Message string `json:"message"`
	} `json:"response"`
}

type ShotstackStatusResponse struct {
	Success  bool `json:"success"`
	Response struct {
		ID     string `json:"id"`
		Status string `json:"status"` // "queued", "rendering", "done", "failed"
		URL    string `json:"url"`
		Error  string `json:"error"`
	} `json:"response"`
}

// RenderVideoInCloud triggers cloud rendering via Shotstack API (0% Local CPU)
func RenderVideoInCloud(video *models.Video, isShorts bool) (string, error) {
	if apiKey := config.AppConfig.ShotstackAPIKey; config.IsSet(apiKey) {
		return submitShotstackRender(video, isShorts, apiKey)
	}

	return "", fmt.Errorf("SHOTSTACK_API_KEY is not configured")
}

func submitShotstackRender(video *models.Video, isShorts bool, apiKey string) (string, error) {
	url := "https://api.shotstack.io/edit/v1/render"

	var videoClips []ShotstackClip
	var audioClips []ShotstackClip
	var currentTime float64 = 0.0

	for _, scene := range video.Scenes {
		duration := scene.Duration
		if duration <= 0 {
			duration = 5.0
		}

		// Visual track clip (Video or Image)
		assetType := "video"
		clipURL := scene.VideoClipURL
		if clipURL == "" && scene.ImagePath != "" {
			assetType = "image"
			clipURL = scene.ImagePath
		} else if clipURL == "" {
			clipURL = getFallbackStockVideoURL(isShorts)
		}

		videoClips = append(videoClips, ShotstackClip{
			Asset: ShotstackAsset{
				Type: assetType,
				SRC:  clipURL,
			},
			Start:  currentTime,
			Length: duration,
		})

		// Audio track clip (narration)
		if scene.AudioPath != "" {
			audioClips = append(audioClips, ShotstackClip{
				Asset: ShotstackAsset{
					Type: "audio",
					SRC:  scene.AudioPath,
				},
				Start:  currentTime,
				Length: duration,
			})
		}

		currentTime += duration
	}

	aspectRatio := "16:9"
	if isShorts {
		aspectRatio = "9:16"
	}

	reqPayload := ShotstackRenderRequest{
		Timeline: ShotstackTimeline{
			Tracks: []ShotstackTrack{
				{Clips: audioClips},
				{Clips: videoClips},
			},
		},
		Output: ShotstackOutput{
			Format:      "mp4",
			Resolution:  "1080",
			AspectRatio: aspectRatio,
		},
	}

	body, err := json.Marshal(reqPayload)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("shotstack api returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var renderResp ShotstackResponse
	if err := json.Unmarshal(respBody, &renderResp); err != nil {
		return "", err
	}

	jobID := renderResp.Response.ID
	log.Printf("Shotstack cloud render job submitted with ID: %s", jobID)

	// Poll until done
	return pollShotstackStatus(jobID, apiKey)
}

func pollShotstackStatus(jobID string, apiKey string) (string, error) {
	url := fmt.Sprintf("https://api.shotstack.io/edit/v1/render/%s", jobID)

	client := &http.Client{Timeout: 15 * time.Second}

	// Poll up to 60 minutes (360 * 10s) - a 20-30 minute long-form render
	// takes meaningfully longer than the short clips this used to be tuned
	// for, and the previous 5 minute cap timed those renders out.
	for i := 0; i < 360; i++ {
		time.Sleep(10 * time.Second)

		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			continue
		}
		req.Header.Set("x-api-key", apiKey)

		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		defer resp.Body.Close()

		respBody, _ := io.ReadAll(resp.Body)
		var statusResp ShotstackStatusResponse
		if err := json.Unmarshal(respBody, &statusResp); err == nil {
			if statusResp.Response.Status == "done" {
				log.Printf("Shotstack cloud render job %s COMPLETED: %s", jobID, statusResp.Response.URL)
				return statusResp.Response.URL, nil
			}
			if statusResp.Response.Status == "failed" {
				return "", fmt.Errorf("shotstack render failed: %s", statusResp.Response.Error)
			}
		}
	}

	return "", fmt.Errorf("shotstack render job timed out")
}
