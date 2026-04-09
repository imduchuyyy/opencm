package database

import (
	"database/sql"
	"fmt"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/imduchuyyy/opencm/plan"
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
		`CREATE TABLE IF NOT EXISTS group_configs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			chat_id INTEGER NOT NULL UNIQUE,
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
			can_delete BOOLEAN NOT NULL DEFAULT 0,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS groups_ (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			chat_id INTEGER NOT NULL UNIQUE,
			chat_title TEXT NOT NULL DEFAULT '',
			chat_type TEXT NOT NULL DEFAULT 'group',
			is_active BOOLEAN NOT NULL DEFAULT 1,
			joined_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS messages (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			chat_id INTEGER NOT NULL,
			chat_type TEXT NOT NULL DEFAULT 'group',
			message_id INTEGER NOT NULL,
			reply_to_message_id INTEGER NOT NULL DEFAULT 0,
			from_user_id INTEGER NOT NULL,
			from_username TEXT NOT NULL DEFAULT '',
			from_first_name TEXT NOT NULL DEFAULT '',
			text TEXT NOT NULL DEFAULT '',
			media_type TEXT NOT NULL DEFAULT '',
			media_file_id TEXT NOT NULL DEFAULT '',
			is_processed BOOLEAN NOT NULL DEFAULT 0,
			ai_response TEXT NOT NULL DEFAULT '',
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_messages_unprocessed ON messages(is_processed) WHERE is_processed = 0`,
		`CREATE INDEX IF NOT EXISTS idx_messages_chat ON messages(chat_id, created_at)`,
		`CREATE TABLE IF NOT EXISTS setup_states (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER NOT NULL,
			chat_id INTEGER NOT NULL DEFAULT 0,
			step TEXT NOT NULL DEFAULT 'idle',
			UNIQUE(user_id)
		)`,
		`CREATE TABLE IF NOT EXISTS knowledge (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			chat_id INTEGER NOT NULL,
			source_type TEXT NOT NULL DEFAULT 'text',
			title TEXT NOT NULL DEFAULT '',
			content TEXT NOT NULL DEFAULT '',
			openai_file_id TEXT NOT NULL DEFAULT '',
			added_by INTEGER NOT NULL DEFAULT 0,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_knowledge_chat ON knowledge(chat_id)`,
		`CREATE TABLE IF NOT EXISTS group_members (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			chat_id INTEGER NOT NULL,
			user_id INTEGER NOT NULL,
			last_seen_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(chat_id, user_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_group_members_user ON group_members(user_id)`,
	}
	for _, m := range migrations {
		if _, err := db.conn.Exec(m); err != nil {
			return fmt.Errorf("execute migration: %w\nSQL: %s", err, m)
		}
	}

	// Additive column migrations (ignore error if column already exists)
	addColumns := []string{
		`ALTER TABLE group_configs ADD COLUMN plan TEXT NOT NULL DEFAULT 'free'`,
	}
	for _, m := range addColumns {
		db.conn.Exec(m) // ignore error (column may already exist)
	}

	// Additional tables that may be added after initial migration
	additionalTables := []string{
		`CREATE TABLE IF NOT EXISTS usage_logs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			chat_id INTEGER NOT NULL,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_usage_logs_chat_month ON usage_logs(chat_id, created_at)`,
	}
	for _, m := range additionalTables {
		if _, err := db.conn.Exec(m); err != nil {
			return fmt.Errorf("execute migration: %w\nSQL: %s", err, m)
		}
	}

	return nil
}

// ----- GroupConfig CRUD -----

func (db *DB) UpsertGroupConfig(cfg *GroupConfig) error {
	_, err := db.conn.Exec(
		`INSERT INTO group_configs (chat_id, plan, system_prompt, bio, topics, message_examples, chat_style, model, vector_store_id, can_reply, can_ban, can_pin, can_poll, can_delete)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(chat_id) DO UPDATE SET
			plan=excluded.plan,
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
			can_delete=excluded.can_delete,
			updated_at=CURRENT_TIMESTAMP`,
		cfg.ChatID, string(cfg.Plan), cfg.SystemPrompt, cfg.Bio, cfg.Topics, cfg.MessageExamples, cfg.ChatStyle,
		cfg.Model, cfg.VectorStoreID,
		cfg.CanReply, cfg.CanBan, cfg.CanPin, cfg.CanPoll, cfg.CanDelete,
	)
	return err
}

func (db *DB) GetGroupConfig(chatID int64) (*GroupConfig, error) {
	cfg := &GroupConfig{}
	var planStr string
	err := db.conn.QueryRow(
		`SELECT id, chat_id, plan, system_prompt, bio, topics, message_examples, chat_style, model, vector_store_id,
			can_reply, can_ban, can_pin, can_poll, can_delete, created_at, updated_at
		 FROM group_configs WHERE chat_id = ?`, chatID,
	).Scan(&cfg.ID, &cfg.ChatID, &planStr, &cfg.SystemPrompt, &cfg.Bio, &cfg.Topics, &cfg.MessageExamples, &cfg.ChatStyle,
		&cfg.Model, &cfg.VectorStoreID,
		&cfg.CanReply, &cfg.CanBan, &cfg.CanPin, &cfg.CanPoll, &cfg.CanDelete,
		&cfg.CreatedAt, &cfg.UpdatedAt)
	if err != nil {
		return nil, err
	}
	cfg.Plan = plan.Plan(planStr)
	if !cfg.Plan.Valid() {
		cfg.Plan = plan.Free
	}
	return cfg, nil
}

func (db *DB) UpdateGroupConfigField(chatID int64, field, value string) error {
	allowed := map[string]bool{
		"system_prompt": true, "bio": true, "topics": true,
		"message_examples": true, "chat_style": true,
		"model": true, "vector_store_id": true,
	}
	if !allowed[field] {
		return fmt.Errorf("unknown field: %s", field)
	}
	query := fmt.Sprintf(`UPDATE group_configs SET %s = ?, updated_at = CURRENT_TIMESTAMP WHERE chat_id = ?`, field)
	_, err := db.conn.Exec(query, value, chatID)
	return err
}

func (db *DB) UpdateGroupConfigBool(chatID int64, field string, value bool) error {
	allowed := map[string]bool{
		"can_reply": true, "can_ban": true, "can_pin": true,
		"can_poll": true, "can_delete": true,
	}
	if !allowed[field] {
		return fmt.Errorf("unknown field: %s", field)
	}
	query := fmt.Sprintf(`UPDATE group_configs SET %s = ?, updated_at = CURRENT_TIMESTAMP WHERE chat_id = ?`, field)
	_, err := db.conn.Exec(query, value, chatID)
	return err
}

// ----- Message CRUD -----

func (db *DB) SaveMessage(msg *Message) error {
	res, err := db.conn.Exec(
		`INSERT INTO messages (chat_id, chat_type, message_id, reply_to_message_id, from_user_id, from_username, from_first_name, text, media_type, media_file_id, is_processed)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		msg.ChatID, msg.ChatType, msg.MessageID, msg.ReplyToMessageID, msg.FromUserID, msg.FromUsername, msg.FromFirstName, msg.Text, msg.MediaType, msg.MediaFileID, msg.IsProcessed,
	)
	if err != nil {
		return err
	}
	id, _ := res.LastInsertId()
	msg.ID = id
	msg.CreatedAt = time.Now()
	return nil
}

func (db *DB) GetUnprocessedMessages() ([]*Message, error) {
	rows, err := db.conn.Query(
		`SELECT id, chat_id, chat_type, message_id, reply_to_message_id, from_user_id, from_username, from_first_name, text, media_type, media_file_id, is_processed, ai_response, created_at
		 FROM messages WHERE is_processed = 0 ORDER BY created_at ASC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var msgs []*Message
	for rows.Next() {
		msg := &Message{}
		if err := rows.Scan(&msg.ID, &msg.ChatID, &msg.ChatType, &msg.MessageID, &msg.ReplyToMessageID,
			&msg.FromUserID, &msg.FromUsername, &msg.FromFirstName, &msg.Text, &msg.MediaType, &msg.MediaFileID,
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

func (db *DB) GetRecentMessages(chatID int64, limit int) ([]*Message, error) {
	rows, err := db.conn.Query(
		`SELECT id, chat_id, chat_type, message_id, reply_to_message_id, from_user_id, from_username, from_first_name, text, media_type, media_file_id, is_processed, ai_response, created_at
		 FROM messages WHERE chat_id = ? ORDER BY created_at DESC LIMIT ?`,
		chatID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var msgs []*Message
	for rows.Next() {
		msg := &Message{}
		if err := rows.Scan(&msg.ID, &msg.ChatID, &msg.ChatType, &msg.MessageID, &msg.ReplyToMessageID,
			&msg.FromUserID, &msg.FromUsername, &msg.FromFirstName, &msg.Text, &msg.MediaType, &msg.MediaFileID,
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

func (db *DB) GetMessageByTelegramID(chatID int64, messageID int) (*Message, error) {
	msg := &Message{}
	err := db.conn.QueryRow(
		`SELECT id, chat_id, chat_type, message_id, reply_to_message_id, from_user_id, from_username, from_first_name, text, media_type, media_file_id, is_processed, ai_response, created_at
		 FROM messages WHERE chat_id = ? AND message_id = ? LIMIT 1`,
		chatID, messageID,
	).Scan(&msg.ID, &msg.ChatID, &msg.ChatType, &msg.MessageID, &msg.ReplyToMessageID,
		&msg.FromUserID, &msg.FromUsername, &msg.FromFirstName, &msg.Text, &msg.MediaType, &msg.MediaFileID,
		&msg.IsProcessed, &msg.AIResponse, &msg.CreatedAt)
	if err != nil {
		return nil, err
	}
	return msg, nil
}

// ----- Group CRUD -----

func (db *DB) UpsertGroup(g *Group) error {
	_, err := db.conn.Exec(
		`INSERT INTO groups_ (chat_id, chat_title, chat_type)
		 VALUES (?, ?, ?)
		 ON CONFLICT(chat_id) DO UPDATE SET
			chat_title=excluded.chat_title,
			chat_type=excluded.chat_type,
			is_active=1`,
		g.ChatID, g.ChatTitle, g.ChatType,
	)
	return err
}

func (db *DB) DeactivateGroup(chatID int64) error {
	_, err := db.conn.Exec(`UPDATE groups_ SET is_active = 0 WHERE chat_id = ?`, chatID)
	return err
}

func (db *DB) GetActiveGroups() ([]*Group, error) {
	rows, err := db.conn.Query(
		`SELECT id, chat_id, chat_title, chat_type, is_active, joined_at FROM groups_ WHERE is_active = 1`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var groups []*Group
	for rows.Next() {
		g := &Group{}
		if err := rows.Scan(&g.ID, &g.ChatID, &g.ChatTitle, &g.ChatType, &g.IsActive, &g.JoinedAt); err != nil {
			return nil, err
		}
		groups = append(groups, g)
	}
	return groups, nil
}

// ----- SetupState -----

func (db *DB) GetSetupState(userID int64) (*SetupState, error) {
	s := &SetupState{}
	err := db.conn.QueryRow(
		`SELECT id, user_id, chat_id, step FROM setup_states WHERE user_id = ?`, userID,
	).Scan(&s.ID, &s.UserID, &s.ChatID, &s.Step)
	if err == sql.ErrNoRows {
		return &SetupState{UserID: userID, Step: "idle"}, nil
	}
	if err != nil {
		return nil, err
	}
	return s, nil
}

func (db *DB) SetSetupState(userID, chatID int64, step string) error {
	_, err := db.conn.Exec(
		`INSERT INTO setup_states (user_id, chat_id, step) VALUES (?, ?, ?)
		 ON CONFLICT(user_id) DO UPDATE SET chat_id=excluded.chat_id, step=excluded.step`,
		userID, chatID, step,
	)
	return err
}

// ----- Knowledge CRUD -----

func (db *DB) AddKnowledge(k *Knowledge) error {
	res, err := db.conn.Exec(
		`INSERT INTO knowledge (chat_id, source_type, title, content, openai_file_id, added_by) VALUES (?, ?, ?, ?, ?, ?)`,
		k.ChatID, k.SourceType, k.Title, k.Content, k.OpenAIFileID, k.AddedBy,
	)
	if err != nil {
		return err
	}
	id, _ := res.LastInsertId()
	k.ID = id
	k.CreatedAt = time.Now()
	return nil
}

func (db *DB) ListKnowledge(chatID int64) ([]*Knowledge, error) {
	rows, err := db.conn.Query(
		`SELECT id, chat_id, source_type, title, content, openai_file_id, added_by, created_at
		 FROM knowledge WHERE chat_id = ? ORDER BY created_at DESC`,
		chatID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []*Knowledge
	for rows.Next() {
		k := &Knowledge{}
		if err := rows.Scan(&k.ID, &k.ChatID, &k.SourceType, &k.Title, &k.Content, &k.OpenAIFileID, &k.AddedBy, &k.CreatedAt); err != nil {
			return nil, err
		}
		results = append(results, k)
	}
	return results, nil
}

func (db *DB) GetKnowledge(id int64) (*Knowledge, error) {
	k := &Knowledge{}
	err := db.conn.QueryRow(
		`SELECT id, chat_id, source_type, title, content, openai_file_id, added_by, created_at
		 FROM knowledge WHERE id = ?`, id,
	).Scan(&k.ID, &k.ChatID, &k.SourceType, &k.Title, &k.Content, &k.OpenAIFileID, &k.AddedBy, &k.CreatedAt)
	if err != nil {
		return nil, err
	}
	return k, nil
}

func (db *DB) DeleteKnowledge(id, chatID int64) error {
	_, err := db.conn.Exec(`DELETE FROM knowledge WHERE id = ? AND chat_id = ?`, id, chatID)
	return err
}

// ----- Group Members -----

// UpsertGroupMember records that a user was seen in a group
func (db *DB) UpsertGroupMember(chatID, userID int64) error {
	_, err := db.conn.Exec(
		`INSERT INTO group_members (chat_id, user_id, last_seen_at)
		 VALUES (?, ?, CURRENT_TIMESTAMP)
		 ON CONFLICT(chat_id, user_id) DO UPDATE SET last_seen_at=CURRENT_TIMESTAMP`,
		chatID, userID,
	)
	return err
}

// GetUserGroups returns active groups where the given user has been seen
func (db *DB) GetUserGroups(userID int64) ([]*Group, error) {
	rows, err := db.conn.Query(
		`SELECT g.id, g.chat_id, g.chat_title, g.chat_type, g.is_active, g.joined_at
		 FROM groups_ g
		 INNER JOIN group_members gm ON g.chat_id = gm.chat_id
		 WHERE gm.user_id = ? AND g.is_active = 1
		 ORDER BY gm.last_seen_at DESC`,
		userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var groups []*Group
	for rows.Next() {
		g := &Group{}
		if err := rows.Scan(&g.ID, &g.ChatID, &g.ChatTitle, &g.ChatType, &g.IsActive, &g.JoinedAt); err != nil {
			return nil, err
		}
		groups = append(groups, g)
	}
	return groups, nil
}

// GetGroup returns a single group by chat ID
func (db *DB) GetGroup(chatID int64) (*Group, error) {
	g := &Group{}
	err := db.conn.QueryRow(
		`SELECT id, chat_id, chat_title, chat_type, is_active, joined_at FROM groups_ WHERE chat_id = ?`, chatID,
	).Scan(&g.ID, &g.ChatID, &g.ChatTitle, &g.ChatType, &g.IsActive, &g.JoinedAt)
	if err != nil {
		return nil, err
	}
	return g, nil
}

// ----- Usage Tracking -----

// LogUsage records one AI response for a group
func (db *DB) LogUsage(chatID int64) error {
	_, err := db.conn.Exec(
		`INSERT INTO usage_logs (chat_id) VALUES (?)`, chatID,
	)
	return err
}

// GetMonthlyUsage returns the number of AI responses for a group in the current calendar month
func (db *DB) GetMonthlyUsage(chatID int64) (int, error) {
	var count int
	err := db.conn.QueryRow(
		`SELECT COUNT(*) FROM usage_logs
		 WHERE chat_id = ? AND created_at >= strftime('%Y-%m-01', 'now')`,
		chatID,
	).Scan(&count)
	return count, err
}

// GetMinuteUsage returns the number of AI responses for a group in the last 60 seconds
func (db *DB) GetMinuteUsage(chatID int64) (int, error) {
	var count int
	err := db.conn.QueryRow(
		`SELECT COUNT(*) FROM usage_logs
		 WHERE chat_id = ? AND created_at >= datetime('now', '-60 seconds')`,
		chatID,
	).Scan(&count)
	return count, err
}
