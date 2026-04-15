# OpenCM - AI Community Manager for Telegram

A single-process, multi-group AI community manager bot for Telegram. One bot instance manages multiple groups with independent per-group configuration, personality, knowledge base, and subscription plans. Built with Go, OpenAI Responses API, and SQLite.

## Table of Contents

- [Architecture Overview](#architecture-overview)
- [Agentic Workflow](#agentic-workflow)
- [Tools](#tools)
- [Message Processing Pipeline](#message-processing-pipeline)
- [LLM Integration](#llm-integration)
- [Knowledge Base](#knowledge-base)
- [Proactive Features](#proactive-features)
- [Moderation](#moderation)
- [Business Model & Plans](#business-model--plans)
- [DM Setup Flow](#dm-setup-flow)
- [Admin Commands](#admin-commands)
- [Super Admin](#super-admin)
- [Project Structure](#project-structure)
- [Database Schema](#database-schema)
- [Setup & Running](#setup--running)
- [Environment Variables](#environment-variables)
- [Design Decisions](#design-decisions)

---

## Architecture Overview

```
                    Telegram API (Long Polling)
                            |
                     +------v------+
                     |   main.go   |  Startup, graceful shutdown (SIGINT/SIGTERM)
                     +------+------+
                            |
                     +------v------+
                     |    Agent    |  Single bot instance (agent/agent.go)
                     +------+------+
                            |
              +-------------+-------------+------------------+
              |             |             |                  |
       pollMessages    processLoop   scheduledPostLoop  engagementLoop
       (goroutine)     (5s ticker)    (60s ticker)      (5min ticker)
              |             |             |                  |
              v             v             v                  v
        handleUpdate   processBatch   processScheduled   checkInactivity
              |             |          Posts               AndEngage
              |    +--------+--------+
              |    |  per-chat (max 5 concurrent)
              |    |        |
              |    v        v
              |  isMentioned? ─── No ──> mark processed, skip
              |    │ Yes
              |    v
              |  Rate limit check ──> Over limit ──> skip
              |    │ OK
              |    v
              |  Build system prompt + user messages
              |    │
              |    v
              |  OpenAI Responses API (with tools + file_search)
              |    │
              |    v
              |  Tool execution loop (max 5 iterations)
              |    │
              |    v
              |  Send LLM text output as reply
              |
         +----+----+----+----+----+
         |    |    |    |    |    |
        DM  Group  New  Pay  Pre  Bot
       setup msgs  Mem  ment Check Added/
       flow  save  bers      out  Removed
```

**Key architectural properties:**

- **Single process, single bot token** - one `tgbotapi.BotAPI` instance manages all groups
- **Per-group isolation** - each group has its own config, personality, knowledge, plan, and usage counters
- **Long polling** (not webhooks) for Telegram updates
- **SQLite** with WAL mode for persistence (`CGO_ENABLED=1` required for `go-sqlite3`)
- **Concurrent batch processing** - up to 5 chats processed in parallel per batch cycle
- **Race condition prevention** - `sync.Mutex.TryLock()` skips overlapping `processBatch` runs

---

## Agentic Workflow

The core agentic loop processes group messages in batch every 5 seconds:

### 1. Message Ingestion (`pollMessages`)
Every Telegram update is received via long polling. Group messages are saved to SQLite with a UNIQUE index on `(chat_id, message_id)` using `INSERT OR IGNORE` to prevent duplicates.

### 2. Batch Processing (`processBatch` - every 5s)
Unprocessed messages are fetched, grouped by chat, and processed concurrently (max 5 chats):

```
For each chat with unprocessed messages:
  1. Check mention/reply trigger
  2. Enforce plan limits (monthly cap, per-minute rate)
  3. Load reply context + recent media (last 5 photos/animations)
  4. Fetch live group context from Telegram API
  5. Build system prompt from group config
  6. Resolve media file IDs to temporary URLs for vision
  7. Call OpenAI Responses API with tools + file_search
  8. Execute tool calls in a loop (max 5 iterations)
  9. Send final LLM text output as reply
  10. Log usage, mark messages processed
```

### 3. Mention Detection (`isMentioned`)
The bot only calls the LLM when explicitly triggered:

- **@username mention** - `@botname` in message text
- **Name mention** - fuzzy whole-word matching on bot's first name (word-boundary check, min 3 chars)
- **Reply to bot** - when a user replies to one of the bot's messages

### 4. Tool Execution Loop
After the initial LLM call, the agent enters a tool execution loop (max 5 iterations):

```
while tool_calls present AND iterations < 5:
  1. Update "Thinking..." message with tool status
  2. Execute each tool call via Executor
  3. Send results back to LLM via ContinueWithToolResults
  4. Check for more tool calls in response
```

### 5. "Thinking..." UX
When the agent is processing:
1. Send a "Thinking..." message immediately
2. Edit it in-place as tools execute ("Searching the web...", "Reading a webpage...", etc.)
3. Delete the thinking message when the final reply is sent

### 6. Response Delivery
The LLM's final text output is sent as a reply to the triggering message. Markdown is attempted first with a plain-text fallback. The bot's own reply is saved to the database for future context.

---

## Tools

All 9 tools are always included in the LLM tool definitions so the model understands the full capability set. Plan-restricted tools return descriptive error messages at execution time.

### Always Available

| Tool | Description |
|------|-------------|
| `send_poll` | Create a poll in the chat (question + options, anonymous/multi-answer) |
| `search_chat_history` | Sub-agent search: fetches last 50 messages, uses gpt-4o-mini to filter for relevance |
| `get_config` | Read current bot config (system_prompt, bio, topics, style, examples) |
| `set_config` | Update a config field (admin-only, verified via Telegram `getChatAdministrators`) |
| `delete_message` | Delete a message by ID (requires bot admin permissions in group) |
| `warn_user` | Log a warning against a user (included in the reply to explain the warning) |
| `ban_user` | Ban a user from the group (requires bot admin permissions) |

### Plan-Gated (Pro+)

| Tool | Description | Execution on Free plan |
|------|-------------|----------------------|
| `web_search` | Search via LangSearch API (POST, returns titles + summaries) | Returns upgrade prompt |
| `web_fetch` | Fetch URL content via Jina Reader (`r.jina.ai/`) | Returns upgrade prompt |

### Built-in (OpenAI)

| Tool | Description |
|------|-------------|
| `file_search` | OpenAI file search against the group's vector store (auto-added when vector store exists) |

### Tool Status Display

During tool execution, the "Thinking..." message is updated with a human-readable status:

```
web_search    -> "Searching the web..."
web_fetch     -> "Reading a webpage..."
search_chat_history -> "Searching chat history..."
send_poll     -> "Creating poll..."
delete_message -> "Moderating..."
(multiple)    -> Shows the highest-priority tool's status
```

---

## Message Processing Pipeline

```
Telegram Update
    |
    v
handleUpdate()
    |
    +-- PreCheckoutQuery? -> handlePreCheckoutQuery (Telegram Stars)
    +-- MyChatMember?     -> handleMyChatMember (bot added/removed)
    +-- SuccessfulPayment?-> handleSuccessfulPayment (subscription created)
    +-- NewChatMembers?   -> handleNewChatMembers (LLM-generated welcome)
    +-- Private message?  -> handlePrivateMessage (DM setup flow)
    +-- Group message?    -> Save to DB, track member
                               |
                               v
                          processBatch() [every 5s]
                               |
                               v
                          processChatMessages()
                               |
                               v
                          isMentioned() -> No: skip
                               |
                              Yes
                               |
                               v
                          Rate/usage check -> Over limit: skip
                               |
                              OK
                               |
                               v
                          Build context:
                            - System prompt (config + group context)
                            - User messages (new batch + reply context + recent media)
                            - Resolve media URLs for vision
                               |
                               v
                          OpenAI Responses API
                               |
                               v
                          Tool loop (0-5 iterations)
                               |
                               v
                          sendReply() -> Markdown with plain-text fallback
```

---

## LLM Integration

### OpenAI Responses API (not Chat Completions)

Uses the official `openai-go` v3 SDK with the Responses API (`/v1/responses`):

- **Main model**: Configured via `DEFAULT_MODEL` (default: `gpt-4o`)
- **Sub-agents**: `gpt-4o-mini` for cheaper tasks:
  - Welcome message generation
  - Chat history search filtering
  - Chat summary generation (in `/report`)
  - Search query generation (for scheduled posts)
  - Engagement content generation
- **Multi-turn**: Uses `PreviousResponseID` for tool result continuation
- **Multimodal**: Photos and animations are passed as `ResponseInputImageParam` with `ImageURL` and `Detail: Auto`
- **File search**: OpenAI vector stores with `FileSearchToolParam` for per-group knowledge

### System Prompt Construction

The system prompt is built dynamically from:

1. Base identity ("You are an AI community manager bot for a Telegram group")
2. Live group context (name, description, pinned message, owner) from Telegram API
3. Per-group config (system_prompt, bio, topics, chat_style, message_examples)
4. Behavior guidelines (response style, tool usage rules, knowledge base rules, moderation rules)

### User Message Construction

Messages sent to the LLM are structured in sections:

1. `=== REFERENCED MESSAGES ===` - Messages being replied to (for context)
2. `=== RECENT IMAGES ===` - Last 5 photos/animations auto-included for vision
3. `=== NEW MESSAGES ===` - The current batch that triggered the mention

Each message includes metadata: `[time, MsgID, ReplyTo] Name (@username, UserID): text`

---

## Knowledge Base

Per-group knowledge via OpenAI Vector Stores + File Search:

- Each group gets its own vector store (created on first knowledge upload)
- **File upload**: PDF, Markdown (.md), Text (.txt) via `/add_knowledge`
- **URL scraping**: Fetch and index web pages via `/add_url`
- File search is automatically included as a tool when a vector store exists
- The system prompt tells the LLM to only use file_search for specific questions, not casual chat
- **Plan restriction**: Knowledge uploads require the Max plan

---

## Proactive Features

### Welcome Messages (LLM-Generated)

When a new member joins, the bot generates a personalized welcome using gpt-4o-mini:

- Inputs: member name, group bio, topics, style, rules, admin guidance (`/set_welcome`)
- Style: 1-3 sentences, matches group tone, mentions rules if set
- Fallback: `"Welcome <name>!"` if LLM call fails

### Proactive Posting

**Manual** (`/create_post <link/keyword>`, Pro+):
1. Research via LangSearch API (web search)
2. If URL provided, also fetch content via Jina Reader
3. Generate post with main LLM model using group style/topics
4. Send to group or configured channel
5. Markdown with plain-text fallback

**Scheduled** (`/set_schedule <hours>`, Max+):
1. Every 60s, check for due scheduled posts
2. Use gpt-4o-mini to generate a timely search query from group topics
3. Avoid repeating recent post topics
4. Follow the same research -> generate -> send pipeline
5. Schedule always advances even on error (prevents retry storms)

### Post Channels

`/set_channel <channel_id>` routes generated posts to a Telegram channel instead of the group chat. The bot must be an admin of the channel.

### Inactivity Engagement

Every 5 minutes, the bot checks all configured groups:
- If no messages for 12+ hours and no recent engagement post
- Uses gpt-4o-mini to generate a conversation starter based on group topics
- Posts directly to the group (not to channels)

---

## Moderation

The bot has three moderation tools available to the LLM when it's mentioned:

- `delete_message` - Delete a specific message (by message_id)
- `warn_user` - Log a warning (the LLM includes the warning reason in its reply)
- `ban_user` - Ban a user from the group

All moderation actions are logged in the `mod_actions` table with reason, timestamp, and acting context.

The system prompt instructs the LLM to:
- Prefer warning before banning
- Always explain why an action was taken
- Only use for clear rule violations (spam, scam, phishing = immediate action)

---

## Business Model & Plans

| Feature | Free | Pro ($19/mo) | Max ($49/mo) | Custom |
|---------|------|-------------|-------------|--------|
| AI responses/month | 1,000 | 2,500 | 10,000 | 100,000 |
| Rate limit/min | 10 | 30 | 60 | 120 |
| Bot config | Yes | Yes | Yes | Yes |
| Web search | No | Yes | Yes | Yes |
| Web fetch | No | Yes | Yes | Yes |
| Create post | No | Yes | Yes | Yes |
| Knowledge upload | No | No | Yes (10MB) | Yes (50MB) |
| Scheduled posts | No | No | Yes | Yes |

### Payment via Telegram Stars

- Currency: `XTR` (Telegram Stars)
- Star pricing: 1 Star ~ $0.013
- Pro: ~1,500 Stars/mo or ~15,000 Stars/yr
- Max: ~3,750 Stars/mo or ~37,500 Stars/yr
- Flow: `/subscribe_pro` or `/subscribe_max` -> invoice -> pre-checkout validation -> payment confirmation -> subscription record
- Annual billing: append `yearly` to the command (saves ~17%)
- Plan is derived from active subscriptions (`GetEffectivePlan`), not a stored plan column

---

## DM Setup Flow

Admins configure the bot by DMing it directly:

```
1. User sends /setup to the bot in DM
2. Bot lists groups where user is admin (via getChatAdministrators)
3. User picks a group by number
4. All subsequent /set_* commands apply to that group
5. /setup again to switch groups
```

Setup state is tracked per-user in the `setup_states` table (user_id, chat_id, step).

---

## Admin Commands

### Configuration
| Command | Description |
|---------|-------------|
| `/setup` | Select a group to configure |
| `/config` | View current configuration |
| `/set_system_prompt` | Core AI instructions |
| `/set_bio` | Bot identity/description |
| `/set_topics` | Topics to cover |
| `/set_examples` | Example messages for style |
| `/set_style` | Chat tone and style |

### Knowledge Base
| Command | Description |
|---------|-------------|
| `/add_knowledge` | Upload a file (PDF, .md, .txt) |
| `/add_url` | Add knowledge from a URL |
| `/list_knowledge` | View all knowledge entries |
| `/delete_knowledge <id>` | Delete a knowledge entry |

### Welcome & Moderation
| Command | Description |
|---------|-------------|
| `/set_welcome` | Set AI welcome guidance for new members |
| `/set_rules` | Set group rules |

### Proactive Posting
| Command | Description |
|---------|-------------|
| `/create_post <link/keyword>` | Research and post content (Pro+) |
| `/set_channel <channel_id>` | Set post destination channel |
| `/set_schedule <hours>` | Auto-post on schedule (Max+) |
| `/stop_schedule` | Stop auto-posting |
| `/post_status` | View posting status |

### Analytics
| Command | Description |
|---------|-------------|
| `/report` | View analytics + LLM-generated chat summary |

### Subscription
| Command | Description |
|---------|-------------|
| `/plan` | View plan and usage |
| `/subscribe_pro` | Upgrade to Pro (~1,500 Stars/mo) |
| `/subscribe_max` | Upgrade to Max (~3,750 Stars/mo) |

---

## Super Admin

A single Telegram username configured via `SUPER_ADMIN_USERNAME` gets elevated privileges:

| Command | Description |
|---------|-------------|
| `/admin_search <name>` | Search all groups by name |
| `/admin_select <chat_id>` | Select any group to configure (bypasses admin check) |
| `/admin_set_plan <chat_id> <plan>` | Set a group's plan without payment (creates 10-year subscription) |
| `/admin_help` | Show super admin commands |

Super admin also sees all groups in `/setup` and `/groups`, with plan labels.

---

## Project Structure

```
opencm/
+-- main.go                 # Entrypoint, graceful shutdown
+-- Makefile                # Build targets (CGO_ENABLED=1)
+-- .env.example            # Environment variable template
+-- config/
|   +-- config.go           # Config struct, env loading
+-- database/
|   +-- models.go           # All data models (GroupConfig, Message, etc.)
|   +-- database.go         # SQLite schema, migrations, all CRUD
+-- llm/
|   +-- client.go           # OpenAI Responses API client, vector store ops
+-- agent/
|   +-- agent.go            # Core: polling, batch processing, LLM pipeline, system prompt
|   +-- messages.go         # All command constants + user-facing strings
|   +-- setup.go            # DM setup flow, knowledge handlers, posting commands, super admin
|   +-- moderation.go       # Welcome (LLM), report/analytics, engagement
|   +-- posts.go            # Proactive post generation (manual + scheduled)
|   +-- payments.go         # Telegram Stars invoice/payment flow
+-- tools/
|   +-- tools.go            # 9 tool definitions + Executor
+-- plan/
    +-- plan.go             # Plan tiers, limits, Star pricing
```

---

## Database Schema

SQLite with WAL mode. All tables use `CREATE TABLE IF NOT EXISTS` for safe migrations.

| Table | Purpose |
|-------|---------|
| `group_configs` | Per-group AI config (prompt, bio, topics, style, vector store ID, welcome, rules) |
| `groups_` | Groups the bot is in (title, type, active status) |
| `messages` | Every message received (text, media, processed status, AI response) |
| `setup_states` | Per-user DM setup flow state (selected group, current step) |
| `knowledge` | Knowledge base entries (file/URL, OpenAI file ID, preview) |
| `group_members` | Member tracking (username, message count, personality notes, first/last seen) |
| `usage_logs` | AI response usage tracking (per-group, timestamped) |
| `subscriptions` | Paid plan subscriptions (plan, billing period, Stars paid, expiry) |
| `post_channels` | Optional post destination channels (one per group) |
| `scheduled_posts` | Scheduled posting configs (interval, active, next post time) |
| `generated_posts` | Record of every generated post (source, query, content, message ID) |
| `mod_actions` | Moderation action log (delete, warn, ban with reasons) |
| `chat_summaries` | Generated chat summaries (period, text) |

Key indexes:
- `UNIQUE(chat_id, message_id)` on messages (deduplication)
- `idx_messages_unprocessed` partial index for batch processing
- `idx_messages_chat` for efficient per-chat queries
- `idx_scheduled_posts_next` for due post lookup

---

## Setup & Running

### Prerequisites

- Go 1.24+
- C compiler (for `go-sqlite3`, CGO is required)
- Telegram Bot Token (from [@BotFather](https://t.me/BotFather))
- OpenAI API Key
- (Optional) LangSearch API Key for web search ([langsearch.com](https://langsearch.com/dashboard))

### Steps

```bash
# Clone
git clone https://github.com/imduchuyyy/opencm.git
cd opencm

# Configure
cp .env.example .env
# Edit .env with your tokens

# Build
make build

# Run
./opencm

# Or run directly
make dev
```

### Telegram Bot Setup

1. Create a bot via [@BotFather](https://t.me/BotFather)
2. Enable inline mode and payments if needed
3. Add the bot to your group(s)
4. Make the bot an admin (for moderation tools: delete messages, ban users)
5. DM the bot with `/setup` to configure

---

## Environment Variables

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `BOT_TOKEN` | Yes | - | Telegram bot token from @BotFather |
| `OPENAI_API_KEY` | Yes | - | OpenAI API key |
| `DATABASE_PATH` | No | `opencm.db` | SQLite database file path |
| `DEFAULT_MODEL` | No | `gpt-4o` | OpenAI model for main LLM calls |
| `LANGSEARCH_API_KEY` | No | - | LangSearch API key for web search |
| `SUPER_ADMIN_USERNAME` | No | - | Telegram username (no @) for super admin |

---

## Design Decisions

### Why Responses API, not Chat Completions?
The Responses API (`/v1/responses`) provides built-in file search, multi-turn with `PreviousResponseID`, and a cleaner tool execution model. It's OpenAI's newer API surface.

### Why all tools always in definitions?
The LLM needs to know tools exist to provide contextual responses. A Free plan user asking "search the web for X" should get "web search requires Pro plan" from the tool, not silence. Plan gating happens at execution time, not definition time.

### Why batch processing instead of per-message?
Batching every 5 seconds groups rapid-fire messages into a single LLM call, reducing API costs and providing better context. The concurrent-per-chat model (max 5) prevents one busy group from blocking others.

### Why gpt-4o-mini for sub-tasks?
Welcome messages, chat history filtering, summary generation, and search query generation are simpler tasks. Using gpt-4o-mini reduces cost by ~10x while maintaining quality for these narrow uses.

### Why no automatic spam detection?
LLM-based spam classification was removed because the per-message token cost is too high at scale. Instead, the bot has moderation tools (delete, warn, ban) that the LLM can use when explicitly mentioned about spam. This is reactive instead of proactive, but drastically cheaper.

### Why SQLite?
Single-process architecture means no need for a database server. SQLite with WAL mode handles concurrent reads/writes well. The `go-sqlite3` driver requires CGO but is battle-tested.

### Why `groups_` table name?
`groups` is a reserved word in SQLite. Using `groups_` avoids quoting issues.

### Why Telegram Stars for payments?
Native Telegram payment method - no external payment processor needed. Users pay with Stars they already have in Telegram. The bot handles the full invoice -> pre-checkout -> payment flow.
