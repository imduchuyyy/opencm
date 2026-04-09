package config

import (
	"log"
	"os"

	"github.com/joho/godotenv"
)

type Config struct {
	// Master bot token - the main bot users interact with to create agents
	MasterBotToken string
	// Database path
	DatabasePath string
	// Default OpenAI model (can be overridden per agent)
	DefaultModel string
	// OpenAI API key
	OpenAIAPIKey string
}

func Load() *Config {
	// Load .env file if it exists (does not override existing env vars)
	if err := godotenv.Load(); err != nil {
		log.Println("No .env file found, reading from environment variables")
	}

	return &Config{
		MasterBotToken: getEnv("MASTER_BOT_TOKEN", ""),
		DatabasePath:   getEnv("DATABASE_PATH", "opencm.db"),
		DefaultModel:   getEnv("DEFAULT_MODEL", "gpt-4o"),
		OpenAIAPIKey:   getEnv("OPENAI_API_KEY", ""),
	}
}

func getEnv(key, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}
