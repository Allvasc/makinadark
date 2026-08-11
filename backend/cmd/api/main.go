package main

import (
	"log"
	"os"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"makinadark/backend/internal/config"
	"makinadark/backend/internal/db"
	"makinadark/backend/internal/handlers"
	"makinadark/backend/internal/models"
	"makinadark/backend/internal/services"
)

func main() {
	// Initialize configuration
	config.LoadConfig()

	// Initialize database
	db.InitDB()

	// Re-apply any operator-saved API keys (Settings tab) on top of .env
	var persistedSettings models.AppSettings
	if err := db.DB.First(&persistedSettings, "id = ?", models.SettingsRowID).Error; err == nil {
		config.ApplySettingsOverride(persistedSettings.OpenAIAPIKey, persistedSettings.GeminiAPIKey, persistedSettings.ElevenLabsAPIKey,
			persistedSettings.YouTubeClientID, persistedSettings.YouTubeClientSec, persistedSettings.TikTokClientID, persistedSettings.TikTokClientSec)
	}

	// Ensure media folder exists and create subfolders
	os.MkdirAll("./media/images", 0755)
	os.MkdirAll("./media/audio", 0755)
	os.MkdirAll("./media/music", 0755)
	os.MkdirAll("./media/output", 0755)

	// Start background post scheduler & Auto-Pilot daemon
	services.StartScheduler()
	services.StartAutoPilotEngine()

	// Initialize Fiber Web Application
	app := fiber.New(fiber.Config{
		BodyLimit: 100 * 1024 * 1024, // 100MB file upload limit
	})

	// Configure CORS middleware to connect with frontend
	app.Use(cors.New(cors.Config{
		AllowOrigins: "http://localhost:5173, http://localhost:8080, http://127.0.0.1:5173, http://localhost:3000",
		AllowHeaders: "Origin, Content-Type, Accept, Authorization",
		AllowMethods: "GET, POST, PUT, DELETE, OPTIONS", // PUT is used by /api/settings
	}))

	// Serve static generated assets (images, audio, videos)
	app.Static("/media", "./media")

	// Set up routing
	handlers.SetupRoutes(app)

	// Catch-all health check
	app.Get("/health", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{
			"status": "healthy",
			"time":   time.Now().Format(time.RFC3339),
			"ffmpeg": services.CheckFFmpeg(),
		})
	})

	// Start server
	port := config.AppConfig.Port
	log.Printf("Mákina Dark Backend server running on port :%s", port)
	if err := app.Listen(":" + port); err != nil {
		log.Fatalf("failed to start server: %v", err)
	}
}
