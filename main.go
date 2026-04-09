package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/imduchuyyy/opencm/internal/agent"
	"github.com/imduchuyyy/opencm/internal/config"
	"github.com/imduchuyyy/opencm/internal/database"
	"github.com/imduchuyyy/opencm/internal/master"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	// Load configuration
	cfg := config.Load()

	if cfg.MasterBotToken == "" {
		log.Fatal("MASTER_BOT_TOKEN environment variable is required")
	}

	// Initialize database
	db, err := database.New(cfg.DatabasePath)
	if err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}
	defer db.Close()
	log.Println("Database initialized")

	// Create context with cancellation
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Initialize agent manager
	agentManager := agent.NewManager(db, cfg)

	// Start all existing agents
	if err := agentManager.StartAll(ctx); err != nil {
		log.Printf("Warning: Error starting existing agents: %v", err)
	}

	// Initialize and start master bot
	masterBot, err := master.New(cfg, db, agentManager)
	if err != nil {
		log.Fatalf("Failed to create master bot: %v", err)
	}

	// Handle graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigChan
		log.Printf("Received signal %v, shutting down...", sig)
		masterBot.Stop()
		agentManager.StopAll()
		cancel()
	}()

	log.Println("OpenCM is running. Press Ctrl+C to stop.")
	masterBot.Start(ctx)
}
