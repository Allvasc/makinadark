package services

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"time"

	"makinadark/backend/internal/config"
	"makinadark/backend/internal/db"
	"makinadark/backend/internal/models"
)

const tiktokCodeVerifier = "makinadark_pkce_code_verifier_98765432101234567890_secret_string"

func generateCodeChallenge(verifier string) string {
	hash := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(hash[:])
}

// GetTikTokAuthURL generates TikTok OAuth2 login URL with PKCE S256
func GetTikTokAuthURL(channelID string) string {
	baseURL := "https://www.tiktok.com/v2/auth/authorize/"
	codeChallenge := generateCodeChallenge(tiktokCodeVerifier)

	params := url.Values{}
	params.Add("client_key", config.AppConfig.TikTokClientID)
	params.Add("response_type", "code")
	// TikTok Content Posting API scopes
	params.Add("scope", "user.info.basic,video.upload,video.publish")
	params.Add("redirect_uri", config.AppConfig.RedirectURI)
	params.Add("state", fmt.Sprintf("tiktok:%s", channelID))
	params.Add("code_challenge", codeChallenge)
	params.Add("code_challenge_method", "S256")

	return baseURL + "?" + params.Encode()
}

// ExchangeTikTokCode exchanges OAuth2 authorization code for TikTok tokens
func ExchangeTikTokCode(code string) (*TokenResponse, error) {
	tokenURL := "https://open.tiktokapis.com/v2/oauth/token/"

	params := url.Values{}
	params.Add("client_key", config.AppConfig.TikTokClientID)
	params.Add("client_secret", config.AppConfig.TikTokClientSec)
	params.Add("code", code)
	params.Add("grant_type", "authorization_code")
	params.Add("redirect_uri", config.AppConfig.RedirectURI)
	params.Add("code_verifier", tiktokCodeVerifier)

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
		return nil, fmt.Errorf("tiktok token exchange failed, status %d: %s", resp.StatusCode, string(respBody))
	}

	var tiktokTokenResp struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
		Error        string `json:"error"`
		ErrorDesc    string `json:"error_description"`
	}

	if err := json.Unmarshal(respBody, &tiktokTokenResp); err != nil {
		return nil, err
	}

	if tiktokTokenResp.Error != "" {
		return nil, fmt.Errorf("tiktok oauth error: %s - %s", tiktokTokenResp.Error, tiktokTokenResp.ErrorDesc)
	}

	return &TokenResponse{
		AccessToken:  tiktokTokenResp.AccessToken,
		RefreshToken: tiktokTokenResp.RefreshToken,
		ExpiresIn:    tiktokTokenResp.ExpiresIn,
	}, nil
}

// RefreshTikTokToken refreshes the TikTok OAuth2 access token
func RefreshTikTokToken(refreshToken string) (*TokenResponse, error) {
	tokenURL := "https://open.tiktokapis.com/v2/oauth/token/"

	params := url.Values{}
	params.Add("client_key", config.AppConfig.TikTokClientID)
	params.Add("client_secret", config.AppConfig.TikTokClientSec)
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
		return nil, fmt.Errorf("tiktok token refresh failed, status %d: %s", resp.StatusCode, string(respBody))
	}

	var tiktokTokenResp struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}

	if err := json.Unmarshal(respBody, &tiktokTokenResp); err != nil {
		return nil, err
	}

	return &TokenResponse{
		AccessToken:  tiktokTokenResp.AccessToken,
		RefreshToken: tiktokTokenResp.RefreshToken,
		ExpiresIn:    tiktokTokenResp.ExpiresIn,
	}, nil
}

type TikTokInitResponse struct {
	Data struct {
		PublishID string `json:"publish_id"`
		UploadURL string `json:"upload_url"`
	} `json:"data"`
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// UploadToTikTok uploads the video (preferably Shorts format) to TikTok API
func UploadToTikTok(channel *models.Channel, video *models.Video) (string, error) {
	// 1. Check/Refresh token
	if time.Now().After(channel.TikTokTokenExpiry) {
		log.Printf("TikTok access token expired for channel %s, refreshing...", channel.Name)
		tokenResp, err := RefreshTikTokToken(channel.TikTokRefreshToken)
		if err != nil {
			return "", fmt.Errorf("failed to refresh TikTok access token: %v", err)
		}
		channel.TikTokAccessToken = tokenResp.AccessToken
		if tokenResp.RefreshToken != "" {
			channel.TikTokRefreshToken = tokenResp.RefreshToken
		}
		channel.TikTokTokenExpiry = time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)
		db.DB.Save(channel)
	}

	// Shorts path is preferred, fallback to VideoPath
	videoPath := video.ShortsPath
	if videoPath == "" {
		videoPath = video.VideoPath
	}

	if _, err := os.Stat(videoPath); os.IsNotExist(err) {
		return "", fmt.Errorf("video file does not exist: %s", videoPath)
	}

	fileInfo, err := os.Stat(videoPath)
	if err != nil {
		return "", err
	}

	// Step 1: Initialize upload request on TikTok Creator API
	initURL := "https://open.tiktokapis.com/v2/post/publish/video/init/"

	initBody := map[string]interface{}{
		"post_info": map[string]interface{}{
			"title":            video.Title, // Caption
			"privacy_level":    "PUBLIC_TO_EVERYONE",
			"disable_duet":     false,
			"disable_stitch":   false,
			"disable_comment":  false,
			"video_cover_timestamp_ms": 1000,
		},
		"source_info": map[string]interface{}{
			"source":             "FILE_UPLOAD",
			"video_size":         fileInfo.Size(),
			"chunk_size":         fileInfo.Size(), // Single chunk upload for smaller short files
			"total_chunk_count":  1,
		},
	}

	jsonBody, err := json.Marshal(initBody)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequest("POST", initURL, bytes.NewBuffer(jsonBody))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+channel.TikTokAccessToken)
	req.Header.Set("Content-Type", "application/json; charset=UTF-8")

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

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("failed to init TikTok upload, status %d: %s", resp.StatusCode, string(respBody))
	}

	var initResp TikTokInitResponse
	if err := json.Unmarshal(respBody, &initResp); err != nil {
		return "", err
	}

	if initResp.Error.Code != "ok" && initResp.Error.Code != "" {
		return "", fmt.Errorf("tiktok api initialization error: %s - %s", initResp.Error.Code, initResp.Error.Message)
	}

	uploadURL := initResp.Data.UploadURL
	publishID := initResp.Data.PublishID

	// Step 2: Upload the video binary
	file, err := os.Open(videoPath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	// TikTok expects PUT to the upload_url with specific headers
	uploadReq, err := http.NewRequest("PUT", uploadURL, file)
	if err != nil {
		return "", err
	}
	// TikTok requires Content-Range header for the video chunk
	uploadReq.Header.Set("Content-Range", fmt.Sprintf("bytes 0-%d/%d", fileInfo.Size()-1, fileInfo.Size()))
	uploadReq.Header.Set("Content-Type", "video/mp4")

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

	// Status 201 Created or 200 OK is expected depending on chunk uploads
	if uploadResp.StatusCode != http.StatusOK && uploadResp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("failed to upload video binary to TikTok, status %d: %s", uploadResp.StatusCode, string(uploadRespBody))
	}

	log.Printf("Successfully published video to TikTok with Publish ID: %s", publishID)
	return publishID, nil
}
