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
	for _, dir := range []string{"images", "audio", "output", "temp"} {
		os.MkdirAll("./media/"+dir, 0755)
	}

	// Start background post scheduler & Auto-Pilot daemon
	services.StartScheduler()
	services.StartAutoPilotEngine()

	// Initialize Fiber Web Application
	app := fiber.New(fiber.Config{
		BodyLimit: 100 * 1024 * 1024, // 100MB file upload limit
	})

	// CORS only matters when the frontend is NOT behind the bundled nginx
	// proxy (e.g. `npm run dev` without its proxy) - same-origin needs none.
	app.Use(cors.New(cors.Config{
		AllowOrigins: config.AppConfig.CORSOrigins,
		AllowHeaders: "Origin, Content-Type, Accept, Authorization",
		AllowMethods: "GET, POST, PUT, DELETE, OPTIONS", // PUT is used by /api/settings
	}))

	// Serve static generated assets (images, audio, videos). ByteRange is
	// required for <video> previews: without Range support browsers can't
	// seek, and Safari won't play the file at all.
	app.Static("/media", "./media", fiber.Static{ByteRange: true})

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
