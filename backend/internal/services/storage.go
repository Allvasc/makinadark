package services

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"makinadark/backend/internal/config"
)

var r2Client *s3.Client

// storageConfigured reports whether Cloudflare R2 is set up. When false, all
// generated media (images/audio/video) is written to and served from local
// disk under ./media instead - no bucket is required for local/dev use.
func storageConfigured() bool {
	return config.AppConfig.R2Bucket != "" && config.AppConfig.R2AccountID != "" &&
		config.AppConfig.R2AccessKeyID != "" && config.AppConfig.R2SecretAccessKey != ""
}

func getR2Client() *s3.Client {
	if r2Client != nil {
		return r2Client
	}

	endpoint := fmt.Sprintf("https://%s.r2.cloudflarestorage.com", config.AppConfig.R2AccountID)
	cfg, err := awsconfig.LoadDefaultConfig(context.Background(),
		awsconfig.WithRegion("auto"),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			config.AppConfig.R2AccessKeyID, config.AppConfig.R2SecretAccessKey, "")),
	)
	if err != nil {
		log.Printf("R2: failed to load AWS config: %v", err)
		return nil
	}

	r2Client = s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
		o.UsePathStyle = true
	})
	return r2Client
}

// SaveMedia persists a generated file (image/audio/video) either to the
// Cloudflare R2 bucket (if configured) or to local disk under ./media, and
// returns a value suitable for storing directly in Video/Scene/Channel path
// fields: a public R2 URL, or a "media/..." relative path for the local
// fallback (already what the rest of the app expects).
func SaveMedia(data []byte, relativeKey string, contentType string) (string, error) {
	relativeKey = strings.ReplaceAll(relativeKey, "\\", "/")

	if storageConfigured() {
		client := getR2Client()
		if client == nil {
			return saveMediaLocal(data, relativeKey)
		}

		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()

		_, err := client.PutObject(ctx, &s3.PutObjectInput{
			Bucket:      aws.String(config.AppConfig.R2Bucket),
			Key:         aws.String(relativeKey),
			Body:        bytes.NewReader(data),
			ContentType: aws.String(contentType),
		})
		if err != nil {
			log.Printf("R2: upload failed for %s, falling back to local disk: %v", relativeKey, err)
			return saveMediaLocal(data, relativeKey)
		}

		return fmt.Sprintf("%s/%s", config.AppConfig.R2PublicBaseURL, relativeKey), nil
	}

	return saveMediaLocal(data, relativeKey)
}

func saveMediaLocal(data []byte, relativeKey string) (string, error) {
	fullPath := filepath.Join("media", relativeKey)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
		return "", err
	}
	if err := os.WriteFile(fullPath, data, 0644); err != nil {
		return "", err
	}
	return fullPath, nil
}

// SaveMediaFromReader is a convenience wrapper for callers that already have
// an io.Reader (e.g. an HTTP response body) instead of a []byte in hand.
func SaveMediaFromReader(r io.Reader, relativeKey string, contentType string) (string, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}
	return SaveMedia(data, relativeKey, contentType)
}

// DeleteMedia removes a previously saved file, whether it's an R2 public URL
// or a local "media/..." relative path. Missing files are not an error.
func DeleteMedia(pathOrURL string) error {
	if pathOrURL == "" {
		return nil
	}

	if storageConfigured() && strings.HasPrefix(pathOrURL, config.AppConfig.R2PublicBaseURL) && config.AppConfig.R2PublicBaseURL != "" {
		client := getR2Client()
		if client == nil {
			return fmt.Errorf("R2 client not available")
		}
		key := strings.TrimPrefix(pathOrURL, config.AppConfig.R2PublicBaseURL+"/")

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		_, err := client.DeleteObject(ctx, &s3.DeleteObjectInput{
			Bucket: aws.String(config.AppConfig.R2Bucket),
			Key:    aws.String(key),
		})
		return err
	}

	// Local fallback path (relative, e.g. "media/output/video_xyz.mp4")
	err := os.Remove(pathOrURL)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
