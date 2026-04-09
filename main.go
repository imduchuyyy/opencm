package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/imduchuyyy/opencm/agent"
	"github.com/imduchuyyy/opencm/config"
	"github.com/imduchuyyy/opencm/database"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	// Load configuration
	cfg := config.Load()

	if cfg.BotToken == "" {
		log.Fatal("BOT_TOKEN environment variable is required")
	}
	if cfg.OpenAIAPIKey == "" {
		log.Fatal("OPENAI_API_KEY environment variable is required")
	}

	// Initialize database
	db, err := database.New(cfg.DatabasePath)
	if err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}
	defer db.Close()
	log.Println("Database initialized")

	// Create the single agent bot
	bot, err := agent.New(cfg, db)
	if err != nil {
		log.Fatalf("Failed to create agent bot: %v", err)
	}

	// Create context with cancellation
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigChan
		log.Printf("Received signal %v, shutting down...", sig)
		bot.Stop()
		cancel()
	}()

	log.Println("OpenCM is running. Press Ctrl+C to stop.")
	bot.Start(ctx)
}
