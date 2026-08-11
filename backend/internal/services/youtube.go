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

// GetYouTubeAuthURL generates Google OAuth2 login URL
func GetYouTubeAuthURL(channelID string) string {
	baseURL := "https://accounts.google.com/o/oauth2/v2/auth"
	
	params := url.Values{}
	params.Add("client_id", config.AppConfig.YouTubeClientID)
	params.Add("redirect_uri", config.AppConfig.RedirectURI)
	params.Add("response_type", "code")
	// YouTube upload scopes
	params.Add("scope", "https://www.googleapis.com/auth/youtube.upload https://www.googleapis.com/auth/youtube.readonly")
	params.Add("access_type", "offline")
	params.Add("prompt", "consent")
	params.Add("state", fmt.Sprintf("youtube:%s", channelID)) // carry channel id

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

// UploadToYouTube uploads a video to YouTube Data API v3
func UploadToYouTube(channel *models.Channel, video *models.Video, isShorts bool) (string, error) {
	// 1. Check/Refresh token
	if time.Now().After(channel.YTTokenExpiry) {
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
		db.DB.Save(channel)
	}

	// Determine video path
	videoPath := video.VideoPath
	if isShorts && video.ShortsPath != "" {
		videoPath = video.ShortsPath
	}

	if _, err := os.Stat(videoPath); os.IsNotExist(err) {
		return "", fmt.Errorf("video file does not exist: %s", videoPath)
	}

	// YouTube Resumable Upload
	// Step 1: Initialize Upload Request
	initURL := "https://www.googleapis.com/upload/youtube/v3/videos?uploadType=resumable&part=snippet,status"
	
	// Create Metadata body
	privacyStatus := "public"
	if isShorts {
		// Shorts are public, and YouTube description/tags will categorize it
		privacyStatus = "public"
	}

	metadata := map[string]interface{}{
		"snippet": map[string]interface{}{
			"title":       video.Title,
			"description": video.Description,
			"tags":        strings.Split(video.Tags, ","),
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
	uploadReq.Header.Set("Content-Length", fmt.Sprintf("%d", fileInfo.Size()))

	// High timeout for large file uploads
	uploadClient := &http.Client{Timeout: 10 * time.Minute}
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
