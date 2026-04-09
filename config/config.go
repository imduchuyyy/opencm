package config

import (
	"log"
	"os"

	"github.com/joho/godotenv"
)

type Config struct {
	BotToken         string
	DatabasePath     string
	DefaultModel     string
	OpenAIAPIKey     string
	LangSearchAPIKey string
}

func Load() *Config {
	if err := godotenv.Load(); err != nil {
		log.Println("No .env file found, reading from environment variables")
	}
	return &Config{
		BotToken:         getEnv("BOT_TOKEN", ""),
		DatabasePath:     getEnv("DATABASE_PATH", "opencm.db"),
		DefaultModel:     getEnv("DEFAULT_MODEL", "gpt-4o"),
		OpenAIAPIKey:     getEnv("OPENAI_API_KEY", ""),
		LangSearchAPIKey: getEnv("LANGSEARCH_API_KEY", ""),
	}
}

func getEnv(key, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}
