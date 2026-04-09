package agent

import (
	"context"
	"log"
	"sync"

	"github.com/imduchuyyy/opencm/internal/config"
	"github.com/imduchuyyy/opencm/internal/database"
)

// Manager manages all running agent instances
type Manager struct {
	db        *database.DB
	appConfig *config.Config
	agents    map[int64]*Agent // key: botID
	mu        sync.RWMutex
}

func NewManager(db *database.DB, appConfig *config.Config) *Manager {
	return &Manager{
		db:        db,
		appConfig: appConfig,
		agents:    make(map[int64]*Agent),
	}
}

// StartAll loads and starts all active bots from the database
func (m *Manager) StartAll(ctx context.Context) error {
	bots, err := m.db.GetAllActiveBots()
	if err != nil {
		return err
	}

	for _, bot := range bots {
		if err := m.StartAgent(ctx, bot.BotToken, bot.BotID); err != nil {
			log.Printf("[Manager] Failed to start agent %d (%s): %v", bot.BotID, bot.BotName, err)
			continue
		}
	}

	log.Printf("[Manager] Started %d agents", len(m.agents))
	return nil
}

// StartAgent starts a single agent
func (m *Manager) StartAgent(ctx context.Context, botToken string, botID int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Don't start if already running
	if _, exists := m.agents[botID]; exists {
		return nil
	}

	agent, err := NewAgent(botToken, botID, m.db, m.appConfig)
	if err != nil {
		return err
	}

	agent.Start(ctx)
	m.agents[botID] = agent
	return nil
}

// StopAgent stops a single agent
func (m *Manager) StopAgent(botID int64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if agent, exists := m.agents[botID]; exists {
		agent.Stop()
		delete(m.agents, botID)
	}
}

// StopAll stops all running agents
func (m *Manager) StopAll() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for id, agent := range m.agents {
		agent.Stop()
		delete(m.agents, id)
	}
	log.Println("[Manager] All agents stopped")
}

// IsRunning checks if an agent is currently running
func (m *Manager) IsRunning(botID int64) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, exists := m.agents[botID]
	return exists
}

// Count returns the number of running agents
func (m *Manager) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.agents)
}
