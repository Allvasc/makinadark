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

func generateCodeChallenge(verifier string) string {
	hash := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(hash[:])
}

// GetTikTokAuthURL generates the TikTok OAuth2 login URL with PKCE S256.
// state is the signed value from auth.SignOAuthState and codeVerifier the
// per-flow verifier derived from it (auth.PKCEVerifier).
func GetTikTokAuthURL(state, codeVerifier string) string {
	baseURL := "https://www.tiktok.com/v2/auth/authorize/"
	codeChallenge := generateCodeChallenge(codeVerifier)

	params := url.Values{}
	params.Add("client_key", config.AppConfig.TikTokClientID)
	params.Add("response_type", "code")
	// TikTok Content Posting API scopes
	params.Add("scope", "user.info.basic,video.upload,video.publish")
	params.Add("redirect_uri", config.AppConfig.RedirectURI)
	params.Add("state", state)
	params.Add("code_challenge", codeChallenge)
	params.Add("code_challenge_method", "S256")

	return baseURL + "?" + params.Encode()
}

// ExchangeTikTokCode exchanges OAuth2 authorization code for TikTok tokens
func ExchangeTikTokCode(code, codeVerifier string) (*TokenResponse, error) {
	tokenURL := "https://open.tiktokapis.com/v2/oauth/token/"

	params := url.Values{}
	params.Add("client_key", config.AppConfig.TikTokClientID)
	params.Add("client_secret", config.AppConfig.TikTokClientSec)
	params.Add("code", code)
	params.Add("grant_type", "authorization_code")
	params.Add("redirect_uri", config.AppConfig.RedirectURI)
	params.Add("code_verifier", codeVerifier)

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

// tiktokChunkPlan follows TikTok's FILE_UPLOAD rules: files under 5 MB (and
// up to 64 MB) go as one chunk; larger files are split into chunks of 5-64
// MB where total_chunk_count = floor(size / chunk_size) and the final chunk
// absorbs the remainder (it may be up to 128 MB). The old code always sent a
// single chunk, which TikTok rejects for anything over 64 MB - i.e. most
// real renders.
func tiktokChunkPlan(size int64) (chunkSize int64, chunkCount int64) {
	const maxSingle = 64 * 1024 * 1024
	const chunk = 10 * 1024 * 1024
	if size <= maxSingle {
		return size, 1
	}
	return chunk, size / chunk
}

// UploadToTikTok uploads a local video file (the 9:16 short) through the
// TikTok Content Posting API. localPath must be a file on disk (see
// localCopy) - the stored ShortsPath may be an R2 URL.
func UploadToTikTok(channel *models.Channel, video *models.Video, localPath string) (string, error) {
	// 1. Check/Refresh token
	if channel.TikTokAccessToken == "" || time.Now().After(channel.TikTokTokenExpiry.Add(-2*time.Minute)) {
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
		db.DB.Model(&models.Channel{}).Where("id = ?", channel.ID).Updates(map[string]interface{}{
			"tik_tok_access_token":  channel.TikTokAccessToken,
			"tik_tok_refresh_token": channel.TikTokRefreshToken,
			"tik_tok_token_expiry":  channel.TikTokTokenExpiry,
		})
	}

	fileInfo, err := os.Stat(localPath)
	if err != nil {
		return "", fmt.Errorf("video file does not exist: %s", localPath)
	}
	size := fileInfo.Size()
	chunkSize, chunkCount := tiktokChunkPlan(size)

	// Step 1: Initialize upload request on TikTok Creator API
	initURL := "https://open.tiktokapis.com/v2/post/publish/video/init/"

	initBody := map[string]interface{}{
		"post_info": map[string]interface{}{
			"title":                    truncateRunes(video.Title, 150), // caption limit is 2200 UTF-16 units; keep it short and readable
			"privacy_level":            "PUBLIC_TO_EVERYONE",
			"disable_duet":             false,
			"disable_stitch":           false,
			"disable_comment":          false,
			"video_cover_timestamp_ms": 1000,
		},
		"source_info": map[string]interface{}{
			"source":            "FILE_UPLOAD",
			"video_size":        size,
			"chunk_size":        chunkSize,
			"total_chunk_count": chunkCount,
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
	if uploadURL == "" {
		return "", fmt.Errorf("tiktok api did not return an upload_url")
	}

	// Step 2: Upload the video binary, chunk by chunk
	file, err := os.Open(localPath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	uploadClient := &http.Client{Timeout: 30 * time.Minute}
	for i := int64(0); i < chunkCount; i++ {
		start := i * chunkSize
		end := start + chunkSize - 1
		if i == chunkCount-1 {
			end = size - 1 // last chunk takes the remainder
		}
		length := end - start + 1

		uploadReq, err := http.NewRequest("PUT", uploadURL, io.NewSectionReader(file, start, length))
		if err != nil {
			return "", err
		}
		uploadReq.ContentLength = length
		uploadReq.Header.Set("Content-Type", "video/mp4")
		uploadReq.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, size))

		uploadResp, err := uploadClient.Do(uploadReq)
		if err != nil {
			return "", fmt.Errorf("tiktok chunk %d/%d upload failed: %w", i+1, chunkCount, err)
		}
		body, _ := io.ReadAll(uploadResp.Body)
		uploadResp.Body.Close()

		// 206 Partial Content for intermediate chunks, 201 Created when done.
		if uploadResp.StatusCode != http.StatusOK && uploadResp.StatusCode != http.StatusCreated && uploadResp.StatusCode != http.StatusPartialContent {
			return "", fmt.Errorf("failed to upload chunk %d/%d to TikTok, status %d: %s", i+1, chunkCount, uploadResp.StatusCode, string(body))
		}
	}

	log.Printf("Successfully published video to TikTok with Publish ID: %s", publishID)
	return publishID, nil
}
