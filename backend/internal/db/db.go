package db

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"makinadark/backend/internal/auth"
	"makinadark/backend/internal/config"
	"makinadark/backend/internal/models"
)

var DB *gorm.DB

// gormLogLevel defaults to Warn: at Info, GORM logs every single query, and
// the scheduler/watchdog/auto-pilot tickers alone buried every useful log
// line under a wall of SELECTs. Set DB_LOG_LEVEL=info to debug SQL.
func gormLogLevel() logger.LogLevel {
	switch strings.ToLower(os.Getenv("DB_LOG_LEVEL")) {
	case "info":
		return logger.Info
	case "error":
		return logger.Error
	case "silent":
		return logger.Silent
	default:
		return logger.Warn
	}
}

func InitDB() {
	var err error
	gormCfg := &gorm.Config{Logger: logger.Default.LogMode(gormLogLevel())}

	if config.AppConfig.PostgresHost != "" {
		dsn := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=disable",
			config.AppConfig.PostgresHost, config.AppConfig.PostgresPort,
			config.AppConfig.PostgresUser, config.AppConfig.PostgresPassword, config.AppConfig.PostgresDB)

		// Postgres routinely needs a few seconds after its container starts
		// before it accepts connections - failing hard on the first attempt
		// made the backend crash-loop on every fresh `docker compose up`.
		const maxAttempts = 30
		for attempt := 1; attempt <= maxAttempts; attempt++ {
			log.Printf("Connecting to Postgres at %s:%s/%s (attempt %d/%d)...",
				config.AppConfig.PostgresHost, config.AppConfig.PostgresPort, config.AppConfig.PostgresDB, attempt, maxAttempts)
			DB, err = gorm.Open(postgres.Open(dsn), gormCfg)
			if err == nil {
				break
			}
			if attempt == maxAttempts {
				log.Fatalf("failed to connect to Postgres: %v", err)
			}
			time.Sleep(2 * time.Second)
		}
		log.Println("Database connection established (Postgres)")
	} else {
		dataDir := "./data"
		if err := os.MkdirAll(dataDir, 0755); err != nil {
			log.Fatalf("failed to create data directory: %v", err)
		}
		dbPath := filepath.Join(dataDir, "makinadark.db")

		DB, err = gorm.Open(sqlite.Open(dbPath), gormCfg)
		if err != nil {
			log.Fatalf("failed to connect database: %v", err)
		}
		log.Println("Database connection established (SQLite fallback - set POSTGRES_HOST to use Postgres)")
	}

	// Auto-Migrate all SaaS schemas
	err = DB.AutoMigrate(
		&models.User{},
		&models.AppSettings{},
		&models.Channel{},
		&models.AutoPilotConfig{},
		&models.Video{},
		&models.Scene{},
		&models.AnalyticsData{},
		&models.ApiUsageLog{},
	)
	if err != nil {
		log.Fatalf("failed to auto-migrate database schemas: %v", err)
	}

	log.Println("Database schemas auto-migrated successfully (SaaS Multi-tenant ready)")

	// Seed default admin user if empty
	var userCount int64
	DB.Model(&models.User{}).Count(&userCount)
	if userCount == 0 {
		password := config.AppConfig.AdminPassword
		generated := false
		if password == "" {
			// No hardcoded default: a publicly known password on an
			// internet-facing deployment is an open door.
			buf := make([]byte, 9)
			if _, err := rand.Read(buf); err != nil {
				log.Fatalf("failed to generate admin password: %v", err)
			}
			password = base64.RawURLEncoding.EncodeToString(buf)
			generated = true
		}
		hash, err := auth.HashPassword(password)
		if err != nil {
			log.Printf("Warning: failed to hash admin password, skipping seed: %v", err)
			return
		}
		defaultUser := models.User{
			ID:           "admin-001",
			Email:        config.AppConfig.AdminEmail,
			PasswordHash: hash,
			Name:         "Administrador Mákina Dark",
			CreatedAt:    time.Now(),
			UpdatedAt:    time.Now(),
		}
		DB.Create(&defaultUser)
		if generated {
			log.Printf("Admin user seeded: %s / %s (generated - set ADMIN_PASSWORD to choose it; change it in Configurações after login)", defaultUser.Email, password)
		} else {
			log.Printf("Admin user seeded: %s (password from ADMIN_PASSWORD)", defaultUser.Email)
		}
	}
}
