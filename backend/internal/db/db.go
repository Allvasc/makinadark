package db

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
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

func InitDB() {
	var err error

	if config.AppConfig.PostgresHost != "" {
		dsn := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=disable",
			config.AppConfig.PostgresHost, config.AppConfig.PostgresPort,
			config.AppConfig.PostgresUser, config.AppConfig.PostgresPassword, config.AppConfig.PostgresDB)

		log.Printf("Connecting to Postgres at %s:%s/%s...", config.AppConfig.PostgresHost, config.AppConfig.PostgresPort, config.AppConfig.PostgresDB)
		DB, err = gorm.Open(postgres.Open(dsn), &gorm.Config{
			Logger: logger.Default.LogMode(logger.Info),
		})
		if err != nil {
			log.Fatalf("failed to connect to Postgres: %v", err)
		}
		log.Println("Database connection established (Postgres)")
	} else {
		dataDir := "./data"
		if err := os.MkdirAll(dataDir, 0755); err != nil {
			log.Fatalf("failed to create data directory: %v", err)
		}
		dbPath := filepath.Join(dataDir, "makinadark.db")

		DB, err = gorm.Open(sqlite.Open(dbPath), &gorm.Config{
			Logger: logger.Default.LogMode(logger.Info),
		})
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
		hash, err := auth.HashPassword("makinadark123")
		if err != nil {
			log.Printf("Warning: failed to hash default admin password, skipping seed: %v", err)
			return
		}
		defaultUser := models.User{
			ID:           "admin-001",
			Email:        "admin@makinadark.com",
			PasswordHash: hash,
			Name:         "Administrador Mákina Dark",
			CreatedAt:    time.Now(),
			UpdatedAt:    time.Now(),
		}
		DB.Create(&defaultUser)
		log.Println("Default Admin User seeded: admin@makinadark.com / makinadark123 - change this password after first login")
	}
}
