package database

import (
	"database/sql"
	"fmt"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

type DB struct {
	conn *sql.DB
}

func New(path string) (*DB, error) {
	conn, err := sql.Open("sqlite3", path+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	db := &DB{conn: conn}
	if err := db.migrate(); err != nil {
		return nil, fmt.Errorf("migrate database: %w", err)
	}
	return db, nil
}

func (db *DB) Close() error {
	return db.conn.Close()
}

func (db *DB) migrate() error {
	migrations := []string{
		`CREATE TABLE IF NOT EXISTS bots (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			owner_id INTEGER NOT NULL,
			bot_token TEXT NOT NULL UNIQUE,
			bot_id INTEGER NOT NULL UNIQUE,
			bot_name TEXT NOT NULL,
			is_active BOOLEAN NOT NULL DEFAULT 1,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS agent_configs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			bot_id INTEGER NOT NULL UNIQUE REFERENCES bots(bot_id),
			system_prompt TEXT NOT NULL DEFAULT '',
			bio TEXT NOT NULL DEFAULT '',
			topics TEXT NOT NULL DEFAULT '',
			message_examples TEXT NOT NULL DEFAULT '',
			chat_style TEXT NOT NULL DEFAULT 'friendly and helpful',
			model TEXT NOT NULL DEFAULT 'gpt-4o',
			vector_store_id TEXT NOT NULL DEFAULT '',
			can_reply BOOLEAN NOT NULL DEFAULT 1,
			can_ban BOOLEAN NOT NULL DEFAULT 0,
			can_pin BOOLEAN NOT NULL DEFAULT 0,
			can_poll BOOLEAN NOT NULL DEFAULT 0,
			can_react BOOLEAN NOT NULL DEFAULT 1,
			can_delete BOOLEAN NOT NULL DEFAULT 0,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS messages (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			bot_id INTEGER NOT NULL,
			chat_id INTEGER NOT NULL,
			chat_type TEXT NOT NULL DEFAULT 'group',
			message_id INTEGER NOT NULL,
			from_user_id INTEGER NOT NULL,
			from_username TEXT NOT NULL DEFAULT '',
			from_first_name TEXT NOT NULL DEFAULT '',
			text TEXT NOT NULL DEFAULT '',
			is_processed BOOLEAN NOT NULL DEFAULT 0,
			ai_response TEXT NOT NULL DEFAULT '',
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_messages_unprocessed ON messages(bot_id, is_processed) WHERE is_processed = 0`,
		`CREATE INDEX IF NOT EXISTS idx_messages_chat ON messages(bot_id, chat_id, created_at)`,
		`CREATE TABLE IF NOT EXISTS bot_groups (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			bot_id INTEGER NOT NULL,
			chat_id INTEGER NOT NULL,
			chat_title TEXT NOT NULL DEFAULT '',
			chat_type TEXT NOT NULL DEFAULT 'group',
			is_active BOOLEAN NOT NULL DEFAULT 1,
			joined_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(bot_id, chat_id)
		)`,
		`CREATE TABLE IF NOT EXISTS setup_states (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			bot_id INTEGER NOT NULL,
			owner_id INTEGER NOT NULL,
			step TEXT NOT NULL DEFAULT 'idle',
			UNIQUE(bot_id, owner_id)
		)`,
		`CREATE TABLE IF NOT EXISTS knowledge (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			bot_id INTEGER NOT NULL,
			source_type TEXT NOT NULL DEFAULT 'text',
			title TEXT NOT NULL DEFAULT '',
			content TEXT NOT NULL DEFAULT '',
			openai_file_id TEXT NOT NULL DEFAULT '',
			added_by INTEGER NOT NULL DEFAULT 0,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_knowledge_bot ON knowledge(bot_id)`,
	}
	for _, m := range migrations {
		if _, err := db.conn.Exec(m); err != nil {
			return fmt.Errorf("execute migration: %w\nSQL: %s", err, m)
		}
	}

	// Additive column migrations (ignore errors if column already exists)
	alterMigrations := []string{
		`ALTER TABLE messages ADD COLUMN reply_to_message_id INTEGER NOT NULL DEFAULT 0`,
	}
	for _, m := range alterMigrations {
		db.conn.Exec(m) // Ignore error - column may already exist
	}

	return nil
}

// ----- Bot CRUD -----

func (db *DB) CreateBot(bot *Bot) error {
	res, err := db.conn.Exec(
		`INSERT INTO bots (owner_id, bot_token, bot_id, bot_name) VALUES (?, ?, ?, ?)`,
		bot.OwnerID, bot.BotToken, bot.BotID, bot.BotName,
	)
	if err != nil {
		return err
	}
	id, _ := res.LastInsertId()
	bot.ID = id
	bot.IsActive = true
	bot.CreatedAt = time.Now()
	bot.UpdatedAt = time.Now()
	return nil
}

func (db *DB) GetBotByBotID(botID int64) (*Bot, error) {
	bot := &Bot{}
	err := db.conn.QueryRow(
		`SELECT id, owner_id, bot_token, bot_id, bot_name, is_active, created_at, updated_at FROM bots WHERE bot_id = ?`,
		botID,
	).Scan(&bot.ID, &bot.OwnerID, &bot.BotToken, &bot.BotID, &bot.BotName, &bot.IsActive, &bot.CreatedAt, &bot.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return bot, nil
}

func (db *DB) GetBotsByOwner(ownerID int64) ([]*Bot, error) {
	rows, err := db.conn.Query(
		`SELECT id, owner_id, bot_token, bot_id, bot_name, is_active, created_at, updated_at FROM bots WHERE owner_id = ?`,
		ownerID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var bots []*Bot
	for rows.Next() {
		bot := &Bot{}
		if err := rows.Scan(&bot.ID, &bot.OwnerID, &bot.BotToken, &bot.BotID, &bot.BotName, &bot.IsActive, &bot.CreatedAt, &bot.UpdatedAt); err != nil {
			return nil, err
		}
		bots = append(bots, bot)
	}
	return bots, nil
}

func (db *DB) GetAllActiveBots() ([]*Bot, error) {
	rows, err := db.conn.Query(
		`SELECT id, owner_id, bot_token, bot_id, bot_name, is_active, created_at, updated_at FROM bots WHERE is_active = 1`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var bots []*Bot
	for rows.Next() {
		bot := &Bot{}
		if err := rows.Scan(&bot.ID, &bot.OwnerID, &bot.BotToken, &bot.BotID, &bot.BotName, &bot.IsActive, &bot.CreatedAt, &bot.UpdatedAt); err != nil {
			return nil, err
		}
		bots = append(bots, bot)
	}
	return bots, nil
}

func (db *DB) DeactivateBot(botID int64) error {
	_, err := db.conn.Exec(`UPDATE bots SET is_active = 0, updated_at = CURRENT_TIMESTAMP WHERE bot_id = ?`, botID)
	return err
}

// ----- AgentConfig CRUD -----

func (db *DB) UpsertAgentConfig(cfg *AgentConfig) error {
	_, err := db.conn.Exec(
		`INSERT INTO agent_configs (bot_id, system_prompt, bio, topics, message_examples, chat_style, model, vector_store_id, can_reply, can_ban, can_pin, can_poll, can_react, can_delete)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(bot_id) DO UPDATE SET
			system_prompt=excluded.system_prompt,
			bio=excluded.bio,
			topics=excluded.topics,
			message_examples=excluded.message_examples,
			chat_style=excluded.chat_style,
			model=excluded.model,
			vector_store_id=excluded.vector_store_id,
			can_reply=excluded.can_reply,
			can_ban=excluded.can_ban,
			can_pin=excluded.can_pin,
			can_poll=excluded.can_poll,
			can_react=excluded.can_react,
			can_delete=excluded.can_delete,
			updated_at=CURRENT_TIMESTAMP`,
		cfg.BotID, cfg.SystemPrompt, cfg.Bio, cfg.Topics, cfg.MessageExamples, cfg.ChatStyle,
		cfg.Model, cfg.VectorStoreID,
		cfg.CanReply, cfg.CanBan, cfg.CanPin, cfg.CanPoll, cfg.CanReact, cfg.CanDelete,
	)
	return err
}

func (db *DB) GetAgentConfig(botID int64) (*AgentConfig, error) {
	cfg := &AgentConfig{}
	err := db.conn.QueryRow(
		`SELECT id, bot_id, system_prompt, bio, topics, message_examples, chat_style, model, vector_store_id,
			can_reply, can_ban, can_pin, can_poll, can_react, can_delete, created_at, updated_at
		 FROM agent_configs WHERE bot_id = ?`, botID,
	).Scan(&cfg.ID, &cfg.BotID, &cfg.SystemPrompt, &cfg.Bio, &cfg.Topics, &cfg.MessageExamples, &cfg.ChatStyle,
		&cfg.Model, &cfg.VectorStoreID,
		&cfg.CanReply, &cfg.CanBan, &cfg.CanPin, &cfg.CanPoll, &cfg.CanReact, &cfg.CanDelete,
		&cfg.CreatedAt, &cfg.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return cfg, nil
}

func (db *DB) UpdateAgentConfigField(botID int64, field, value string) error {
	// Only allow known fields to prevent SQL injection
	allowed := map[string]bool{
		"system_prompt": true, "bio": true, "topics": true,
		"message_examples": true, "chat_style": true,
		"model": true, "vector_store_id": true,
	}
	if !allowed[field] {
		return fmt.Errorf("unknown field: %s", field)
	}
	query := fmt.Sprintf(`UPDATE agent_configs SET %s = ?, updated_at = CURRENT_TIMESTAMP WHERE bot_id = ?`, field)
	_, err := db.conn.Exec(query, value, botID)
	return err
}

func (db *DB) UpdateAgentConfigBool(botID int64, field string, value bool) error {
	allowed := map[string]bool{
		"can_reply": true, "can_ban": true, "can_pin": true,
		"can_poll": true, "can_react": true, "can_delete": true,
	}
	if !allowed[field] {
		return fmt.Errorf("unknown field: %s", field)
	}
	query := fmt.Sprintf(`UPDATE agent_configs SET %s = ?, updated_at = CURRENT_TIMESTAMP WHERE bot_id = ?`, field)
	_, err := db.conn.Exec(query, value, botID)
	return err
}

// ----- Message CRUD -----

func (db *DB) SaveMessage(msg *Message) error {
	res, err := db.conn.Exec(
		`INSERT INTO messages (bot_id, chat_id, chat_type, message_id, reply_to_message_id, from_user_id, from_username, from_first_name, text, is_processed)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		msg.BotID, msg.ChatID, msg.ChatType, msg.MessageID, msg.ReplyToMessageID, msg.FromUserID, msg.FromUsername, msg.FromFirstName, msg.Text, msg.IsProcessed,
	)
	if err != nil {
		return err
	}
	id, _ := res.LastInsertId()
	msg.ID = id
	msg.CreatedAt = time.Now()
	return nil
}

func (db *DB) GetUnprocessedMessages(botID int64) ([]*Message, error) {
	rows, err := db.conn.Query(
		`SELECT id, bot_id, chat_id, chat_type, message_id, reply_to_message_id, from_user_id, from_username, from_first_name, text, is_processed, ai_response, created_at
		 FROM messages WHERE bot_id = ? AND is_processed = 0 ORDER BY created_at ASC`,
		botID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var msgs []*Message
	for rows.Next() {
		msg := &Message{}
		if err := rows.Scan(&msg.ID, &msg.BotID, &msg.ChatID, &msg.ChatType, &msg.MessageID, &msg.ReplyToMessageID,
			&msg.FromUserID, &msg.FromUsername, &msg.FromFirstName, &msg.Text,
			&msg.IsProcessed, &msg.AIResponse, &msg.CreatedAt); err != nil {
			return nil, err
		}
		msgs = append(msgs, msg)
	}
	return msgs, nil
}

func (db *DB) MarkMessagesProcessed(ids []int64, aiResponse string) error {
	if len(ids) == 0 {
		return nil
	}
	tx, err := db.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`UPDATE messages SET is_processed = 1, ai_response = ? WHERE id = ?`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, id := range ids {
		if _, err := stmt.Exec(aiResponse, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (db *DB) GetRecentMessages(botID, chatID int64, limit int) ([]*Message, error) {
	rows, err := db.conn.Query(
		`SELECT id, bot_id, chat_id, chat_type, message_id, reply_to_message_id, from_user_id, from_username, from_first_name, text, is_processed, ai_response, created_at
		 FROM messages WHERE bot_id = ? AND chat_id = ? ORDER BY created_at DESC LIMIT ?`,
		botID, chatID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var msgs []*Message
	for rows.Next() {
		msg := &Message{}
		if err := rows.Scan(&msg.ID, &msg.BotID, &msg.ChatID, &msg.ChatType, &msg.MessageID, &msg.ReplyToMessageID,
			&msg.FromUserID, &msg.FromUsername, &msg.FromFirstName, &msg.Text,
			&msg.IsProcessed, &msg.AIResponse, &msg.CreatedAt); err != nil {
			return nil, err
		}
		msgs = append(msgs, msg)
	}
	// Reverse to chronological order
	for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
		msgs[i], msgs[j] = msgs[j], msgs[i]
	}
	return msgs, nil
}

// ----- BotGroup CRUD -----

// GetMessageByTelegramID fetches a stored message by its Telegram message_id within a chat
func (db *DB) GetMessageByTelegramID(botID, chatID int64, messageID int) (*Message, error) {
	msg := &Message{}
	err := db.conn.QueryRow(
		`SELECT id, bot_id, chat_id, chat_type, message_id, reply_to_message_id, from_user_id, from_username, from_first_name, text, is_processed, ai_response, created_at
		 FROM messages WHERE bot_id = ? AND chat_id = ? AND message_id = ? LIMIT 1`,
		botID, chatID, messageID,
	).Scan(&msg.ID, &msg.BotID, &msg.ChatID, &msg.ChatType, &msg.MessageID, &msg.ReplyToMessageID,
		&msg.FromUserID, &msg.FromUsername, &msg.FromFirstName, &msg.Text,
		&msg.IsProcessed, &msg.AIResponse, &msg.CreatedAt)
	if err != nil {
		return nil, err
	}
	return msg, nil
}

func (db *DB) UpsertBotGroup(bg *BotGroup) error {
	_, err := db.conn.Exec(
		`INSERT INTO bot_groups (bot_id, chat_id, chat_title, chat_type)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(bot_id, chat_id) DO UPDATE SET
			chat_title=excluded.chat_title,
			chat_type=excluded.chat_type,
			is_active=1`,
		bg.BotID, bg.ChatID, bg.ChatTitle, bg.ChatType,
	)
	return err
}

func (db *DB) DeactivateBotGroup(botID, chatID int64) error {
	_, err := db.conn.Exec(`UPDATE bot_groups SET is_active = 0 WHERE bot_id = ? AND chat_id = ?`, botID, chatID)
	return err
}

func (db *DB) GetBotGroups(botID int64) ([]*BotGroup, error) {
	rows, err := db.conn.Query(
		`SELECT id, bot_id, chat_id, chat_title, chat_type, is_active, joined_at FROM bot_groups WHERE bot_id = ? AND is_active = 1`,
		botID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var groups []*BotGroup
	for rows.Next() {
		g := &BotGroup{}
		if err := rows.Scan(&g.ID, &g.BotID, &g.ChatID, &g.ChatTitle, &g.ChatType, &g.IsActive, &g.JoinedAt); err != nil {
			return nil, err
		}
		groups = append(groups, g)
	}
	return groups, nil
}

// ----- SetupState -----

func (db *DB) GetSetupState(botID, ownerID int64) (string, error) {
	var step string
	err := db.conn.QueryRow(
		`SELECT step FROM setup_states WHERE bot_id = ? AND owner_id = ?`,
		botID, ownerID,
	).Scan(&step)
	if err == sql.ErrNoRows {
		return "idle", nil
	}
	return step, err
}

func (db *DB) SetSetupState(botID, ownerID int64, step string) error {
	_, err := db.conn.Exec(
		`INSERT INTO setup_states (bot_id, owner_id, step) VALUES (?, ?, ?)
		 ON CONFLICT(bot_id, owner_id) DO UPDATE SET step=excluded.step`,
		botID, ownerID, step,
	)
	return err
}

// ----- Knowledge CRUD -----

func (db *DB) AddKnowledge(k *Knowledge) error {
	res, err := db.conn.Exec(
		`INSERT INTO knowledge (bot_id, source_type, title, content, openai_file_id, added_by) VALUES (?, ?, ?, ?, ?, ?)`,
		k.BotID, k.SourceType, k.Title, k.Content, k.OpenAIFileID, k.AddedBy,
	)
	if err != nil {
		return err
	}
	id, _ := res.LastInsertId()
	k.ID = id
	k.CreatedAt = time.Now()
	return nil
}

func (db *DB) ListKnowledge(botID int64) ([]*Knowledge, error) {
	rows, err := db.conn.Query(
		`SELECT id, bot_id, source_type, title, content, openai_file_id, added_by, created_at
		 FROM knowledge WHERE bot_id = ? ORDER BY created_at DESC`,
		botID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []*Knowledge
	for rows.Next() {
		k := &Knowledge{}
		if err := rows.Scan(&k.ID, &k.BotID, &k.SourceType, &k.Title, &k.Content, &k.OpenAIFileID, &k.AddedBy, &k.CreatedAt); err != nil {
			return nil, err
		}
		results = append(results, k)
	}
	return results, nil
}

func (db *DB) GetKnowledge(id int64) (*Knowledge, error) {
	k := &Knowledge{}
	err := db.conn.QueryRow(
		`SELECT id, bot_id, source_type, title, content, openai_file_id, added_by, created_at
		 FROM knowledge WHERE id = ?`, id,
	).Scan(&k.ID, &k.BotID, &k.SourceType, &k.Title, &k.Content, &k.OpenAIFileID, &k.AddedBy, &k.CreatedAt)
	if err != nil {
		return nil, err
	}
	return k, nil
}

func (db *DB) DeleteKnowledge(id, botID int64) error {
	_, err := db.conn.Exec(`DELETE FROM knowledge WHERE id = ? AND bot_id = ?`, id, botID)
	return err
}
