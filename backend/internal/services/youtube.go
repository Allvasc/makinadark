package services

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"makinadark/backend/internal/config"
	"makinadark/backend/internal/db"
	"makinadark/backend/internal/models"
)

// GetYouTubeAuthURL generates the Google OAuth2 consent URL. state must be
// the signed value from auth.SignOAuthState - the callback verifies it.
func GetYouTubeAuthURL(state string) string {
	baseURL := "https://accounts.google.com/o/oauth2/v2/auth"

	params := url.Values{}
	params.Add("client_id", config.AppConfig.YouTubeClientID)
	params.Add("redirect_uri", config.AppConfig.RedirectURI)
	params.Add("response_type", "code")
	// YouTube upload scopes
	params.Add("scope", "https://www.googleapis.com/auth/youtube.upload https://www.googleapis.com/auth/youtube.readonly")
	params.Add("access_type", "offline")
	params.Add("prompt", "consent")
	params.Add("state", state)

	return baseURL + "?" + params.Encode()
}

type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
}

// ExchangeYouTubeCode exchanges OAuth2 authorization code for access/refresh tokens
func ExchangeYouTubeCode(code string) (*TokenResponse, error) {
	tokenURL := "https://oauth2.googleapis.com/token"

	params := url.Values{}
	params.Add("code", code)
	params.Add("client_id", config.AppConfig.YouTubeClientID)
	params.Add("client_secret", config.AppConfig.YouTubeClientSec)
	params.Add("redirect_uri", config.AppConfig.RedirectURI)
	params.Add("grant_type", "authorization_code")

	resp, err := http.PostForm(tokenURL, params)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token exchange failed, status %d: %s", resp.StatusCode, string(respBody))
	}

	var tokenResp TokenResponse
	if err := json.Unmarshal(respBody, &tokenResp); err != nil {
		return nil, err
	}

	return &tokenResp, nil
}

// RefreshYouTubeToken refreshes the YouTube OAuth2 access token using the refresh token
func RefreshYouTubeToken(refreshToken string) (*TokenResponse, error) {
	tokenURL := "https://oauth2.googleapis.com/token"

	params := url.Values{}
	params.Add("client_id", config.AppConfig.YouTubeClientID)
	params.Add("client_secret", config.AppConfig.YouTubeClientSec)
	params.Add("refresh_token", refreshToken)
	params.Add("grant_type", "refresh_token")

	resp, err := http.PostForm(tokenURL, params)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token refresh failed, status %d: %s", resp.StatusCode, string(respBody))
	}

	var tokenResp TokenResponse
	if err := json.Unmarshal(respBody, &tokenResp); err != nil {
		return nil, err
	}

	return &tokenResp, nil
}

// UploadToYouTube uploads a local video file to the YouTube Data API v3.
// localPath must be a file on disk (see localCopy) - the stored VideoPath
// may be an R2 URL.
func UploadToYouTube(channel *models.Channel, video *models.Video, localPath string) (string, error) {
	// 1. Check/Refresh token
	if channel.YTAccessToken == "" || time.Now().After(channel.YTTokenExpiry.Add(-2*time.Minute)) {
		log.Printf("YouTube access token expired for channel %s, refreshing...", channel.Name)
		tokenResp, err := RefreshYouTubeToken(channel.YTRefreshToken)
		if err != nil {
			return "", fmt.Errorf("failed to refresh YouTube access token: %v", err)
		}
		channel.YTAccessToken = tokenResp.AccessToken
		if tokenResp.RefreshToken != "" {
			channel.YTRefreshToken = tokenResp.RefreshToken
		}
		channel.YTTokenExpiry = time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)
		// Only the token columns - a full Save of this (possibly stale) copy
		// would overwrite anything edited on the channel meanwhile.
		db.DB.Model(&models.Channel{}).Where("id = ?", channel.ID).Updates(map[string]interface{}{
			"yt_access_token":  channel.YTAccessToken,
			"yt_refresh_token": channel.YTRefreshToken,
			"yt_token_expiry":  channel.YTTokenExpiry,
		})
	}

	videoPath := localPath
	if _, err := os.Stat(videoPath); err != nil {
		return "", fmt.Errorf("video file does not exist: %s", videoPath)
	}

	// YouTube Resumable Upload
	// Step 1: Initialize Upload Request
	initURL := "https://www.googleapis.com/upload/youtube/v3/videos?uploadType=resumable&part=snippet,status"

	// Create Metadata body
	privacyStatus := "public"

	metadata := map[string]interface{}{
		"snippet": map[string]interface{}{
			"title":       youtubeTitle(video.Title),
			"description": youtubeDescription(video.Description),
			"tags":        youtubeTags(video.Tags),
		},
		"status": map[string]interface{}{
			"privacyStatus": privacyStatus,
		},
	}

	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequest("POST", initURL, bytes.NewBuffer(metadataJSON))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+channel.YTAccessToken)
	req.Header.Set("Content-Type", "application/json; charset=UTF-8")
	req.Header.Set("X-Upload-Content-Type", "video/mp4")

	fileInfo, err := os.Stat(videoPath)
	if err != nil {
		return "", err
	}
	req.Header.Set("X-Upload-Content-Length", fmt.Sprintf("%d", fileInfo.Size()))

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("failed to initialize upload, status %d: %s", resp.StatusCode, string(respBody))
	}

	// Capture the resumable upload location URL
	uploadLocation := resp.Header.Get("Location")
	if uploadLocation == "" {
		return "", fmt.Errorf("resumable upload location URL not returned by YouTube API")
	}

	// Step 2: Upload the actual video file binary
	file, err := os.Open(videoPath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	// Using standard http request for file body upload
	uploadReq, err := http.NewRequest("PUT", uploadLocation, file)
	if err != nil {
		return "", err
	}
	uploadReq.Header.Set("Content-Type", "video/mp4")
	uploadReq.ContentLength = fileInfo.Size() // Go ignores a hand-set Content-Length header and would stream chunked

	// Long-form renders can be well over 1 GB - 10 minutes wasn't enough
	// on an ordinary uplink.
	uploadClient := &http.Client{Timeout: 90 * time.Minute}
	uploadResp, err := uploadClient.Do(uploadReq)
	if err != nil {
		return "", err
	}
	defer uploadResp.Body.Close()

	uploadRespBody, err := io.ReadAll(uploadResp.Body)
	if err != nil {
		return "", err
	}

	if uploadResp.StatusCode != http.StatusOK && uploadResp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("video binary upload failed, status %d: %s", uploadResp.StatusCode, string(uploadRespBody))
	}

	// Parse YouTube Video ID from response
	var youtubeVideo struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(uploadRespBody, &youtubeVideo); err != nil {
		return "", fmt.Errorf("failed to parse upload response: %v", err)
	}

	log.Printf("Successfully uploaded video to YouTube with ID: %s", youtubeVideo.ID)
	return youtubeVideo.ID, nil
}

// YouTube rejects (400 invalidTitle/invalidDescription/invalidTags) titles
// over 100 characters, descriptions over 5000 bytes, tag lists over 500
// characters, and any "<" or ">" - all of which LLM-written metadata hits
// regularly. These keep an otherwise-good upload from failing on metadata.
func youtubeSanitize(s string) string {
	return strings.NewReplacer("<", "", ">", "").Replace(strings.TrimSpace(s))
}

func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return strings.TrimSpace(string(r[:max]))
}

func youtubeTitle(title string) string {
	t := truncateRunes(youtubeSanitize(title), 100)
	if t == "" {
		t = "Novo vídeo"
	}
	return t
}

func youtubeDescription(desc string) string {
	d := youtubeSanitize(desc)
	for len(d) > 5000 {
		r := []rune(d)
		d = string(r[:len(r)-100])
	}
	return d
}

func youtubeTags(raw string) []string {
	var tags []string
	total := 0
	for _, t := range strings.Split(raw, ",") {
		t = strings.Trim(youtubeSanitize(t), "#\" ")
		if t == "" {
			continue
		}
		cost := len([]rune(t))
		if strings.Contains(t, " ") {
			cost += 2 // YouTube counts the quotes it adds around multi-word tags
		}
		if total+cost+len(tags) > 500 {
			break
		}
		total += cost
		tags = append(tags, t)
	}
	return tags
}
