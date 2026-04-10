package agent

// Command constants
const (
	CmdStart           = "/start"
	CmdSetup           = "/setup"
	CmdConfig          = "/config"
	CmdSetSystemPrompt = "/set_system_prompt"
	CmdSetBio          = "/set_bio"
	CmdSetTopics       = "/set_topics"
	CmdSetExamples     = "/set_examples"
	CmdSetStyle        = "/set_style"
	CmdAddKnowledge    = "/add_knowledge"
	CmdAddURL          = "/add_url"
	CmdListKnowledge   = "/list_knowledge"
	CmdDeleteKnowledge = "/delete_knowledge"
	CmdGroups          = "/groups"
	CmdPlan            = "/plan"
	CmdHelp            = "/help"
	CmdCancel          = "/cancel"
)

// User-facing message constants
const (
	MsgSelectGroupFirst     = "Please select a group first. Use " + CmdSetup + " to choose a group."
	MsgNoGroupSelected      = "No group selected. Use " + CmdSetup + " to select a group first."
	MsgNoLongerAdmin        = "You are no longer an admin of the selected group. Use " + CmdSetup + " to select a new group."
	MsgNotAdminAnyGroup     = "You are not an admin of any groups I'm in.\n\nPlease add me to a group first and make sure you are an admin there."
	MsgNoGroupsFound        = "No groups found. Add me to a group first."
	MsgNoGroupsAdmin        = "I'm not in any groups where you're an admin.\n\nAdd me to a group and make sure you're an admin there!"
	MsgNoConfigYet          = "No configuration yet. Use " + CmdSetup + " to get started."
	MsgSetupGroupFirst      = "Please set up the group first with " + CmdSetup
	MsgUnknownCommand       = "Unknown command. Use " + CmdHelp + " to see available commands."
	MsgErrorSaveConfig      = "Error saving configuration. Please try again."
	MsgErrorDeleteEntry     = "Error deleting knowledge entry."
	MsgErrorKnowledgeNone   = "No knowledge entries yet.\n\nUse " + CmdAddKnowledge + " to upload a file or " + CmdAddURL + " to add from a URL."
	MsgCancelled            = "Cancelled. Use " + CmdSetup + " to select a group and configure."
	MsgUseSetupOrHelp       = "Use " + CmdSetup + " to configure bot settings for your group, or " + CmdHelp + " for available commands."
	MsgDocNeedStep          = "To upload a file as knowledge, first use " + CmdAddKnowledge + " then send the file."
	MsgSendFilePrompt       = "Please send a file (PDF, .md, or .txt).\n\nSend " + CmdCancel + " to abort."
	MsgErrorDownloadTG      = "Error downloading file from Telegram. Please try again."
	MsgErrorDownload        = "Error downloading file. Please try again."
	MsgErrorReadContent     = "Error reading file content. Please try again."
	MsgErrorKnowledgeStore  = "Error creating knowledge store. Please try again."
	MsgErrorUploadKB        = "Error uploading file to knowledge base. Please try again."
	MsgErrorUploadLocal     = "File uploaded to OpenAI but failed to save local record."
	MsgUploadingKB          = "Uploading to knowledge base..."
	MsgFetchingURL          = "Fetching content from URL..."
	MsgErrorUploadURL       = "Error uploading knowledge. Please try again."
	MsgErrorSaveRecord      = "Error saving knowledge record."
	MsgKnowledgeNotFound    = "Knowledge entry not found."
	MsgInvalidID            = "Invalid ID. Use " + CmdListKnowledge + " to see valid IDs."
	MsgDeleteUsage          = "Usage: " + CmdDeleteKnowledge + " <id>\n\nUse " + CmdListKnowledge + " to see IDs."
	MsgInvalidURL           = "Invalid URL. Please try again with " + CmdAddURL
	MsgFailedReadURL        = "Failed to read URL content. Try again with " + CmdAddURL
	MsgNoTextFromURL        = "Could not extract text from that URL. Try a different URL or use " + CmdAddKnowledge + " to upload a file instead."
	MsgUnsupportedFileType  = "Unsupported file type: %s\n\nPlease send a PDF, Markdown (.md), or Text (.txt) file."
	MsgFileTooLarge         = "File is too large (max %s on the %s plan). Please send a smaller file."
	MsgDownloading          = "Downloading %s..."
	MsgKnowledgeNoUpload    = "Knowledge uploads are not available on the %s plan.\n\nUpgrade to Pro or Max to use the knowledge base."
	MsgFieldUpdated         = "%s updated successfully!\n\nUse " + CmdConfig + " to see current configuration or " + CmdSetup + " to continue configuring."
	MsgInvalidGroupNum      = "Please send a number between 1 and %d."
	MsgGroupSelected        = "Selected group: %s\n\nYou can now configure the bot for this group. Use " + CmdSetup + " to see the menu, or use any command directly."
	MsgKnowledgeDeleted     = "Knowledge entry %d deleted (\"%s\")."
	MsgFailedFetchURL       = "Failed to fetch URL: %v\n\nTry again with " + CmdAddURL
	MsgFileUploaded         = "File uploaded to knowledge base! (ID: %d)\n\nFilename: %s\nSize: %s\n\nUse " + CmdListKnowledge + " to see all entries or " + CmdAddKnowledge + " to upload more."
	MsgURLKnowledgeAdded    = "Knowledge from URL added! (ID: %d)\n\nSource: %s\nContent preview: %s\n\nUse " + CmdListKnowledge + " to see all entries."
	MsgSelectGroupPrompt    = "Select a group to configure:\n\n%s\n\nSend the number of the group you want to configure:"
	MsgGroupsListHeader     = "Groups where you're admin:\n\n%s"
	MsgCurrentlyConfiguring = "\n\nCurrently configuring: %s"
	MsgWelcome              = "Welcome! I'm @%s, your AI community manager bot.\n\n" +
		"To get started:\n" +
		"1. Add me to your Telegram group\n" +
		"2. Make me an admin\n" +
		"3. Use " + CmdSetup + " here to configure my behavior for your group\n\n" +
		"I manage each group independently with its own personality, knowledge, and settings.\n\n" +
		"Use " + CmdHelp + " to see all available commands."
	MsgKnowledgeListFmt = "Knowledge Base (%d entries)\n\n%s\n\nTo delete: " + CmdDeleteKnowledge + " <id>"

	MsgPromptSystemPrompt = "Send me the system prompt for the bot in this group.\n\n" +
		"This is the core instruction that tells the AI how to behave. Example:\n\n" +
		"\"You are a helpful community manager for a crypto trading group. Keep discussions on topic, help newcomers, and moderate spam.\""

	MsgPromptBio = "Send me the bot's bio/description.\n\n" +
		"This helps the AI understand its identity. Example:\n\n" +
		"\"CryptoBot - Your friendly crypto community assistant, helping since 2024\""

	MsgPromptTopics = "Send me the topics the bot should cover (comma-separated).\n\n" +
		"Example:\n\"cryptocurrency, trading, DeFi, market analysis, technical analysis\""

	MsgPromptExamples = "Send me example messages that show the bot's style.\n\n" +
		"Put each example on a new line. Example:\n\n" +
		"\"Welcome aboard! Feel free to ask anything about trading.\"\n" +
		"\"Great question! Here's what I think about BTC...\"\n" +
		"\"Please keep the discussion civil, folks.\""

	MsgPromptStyle = "Describe the chat style for your bot.\n\n" +
		"Example:\n\"Friendly and casual, uses occasional emojis, speaks like a knowledgeable friend rather than a formal assistant\""

	MsgPromptKnowledgeFile = "Send me a file to add to the knowledge base.\n\n" +
		"Supported formats: PDF, Markdown (.md), Text (.txt)\n\n" +
		"The file will be uploaded to the AI knowledge store so the bot can reference it when answering questions.\n\n" +
		"Send " + CmdCancel + " to abort."

	MsgPromptKnowledgeURL = "Send me a URL to fetch and store as knowledge.\n\n" +
		"I'll download the page content and upload it to the knowledge base."

	MsgSetupMenu = "Bot Setup Menu\n\n" +
		"Configure your bot's personality and behavior:\n\n" +
		CmdSetSystemPrompt + " - Core AI instructions\n" +
		CmdSetBio + " - Bot identity/description\n" +
		CmdSetTopics + " - Topics to cover\n" +
		CmdSetExamples + " - Example messages for style\n" +
		CmdSetStyle + " - Chat tone and style\n\n" +
		"Knowledge Base:\n" +
		CmdAddKnowledge + " - Upload a file (PDF, .md, .txt)\n" +
		CmdAddURL + " - Add knowledge from a URL\n" +
		CmdListKnowledge + " - View all knowledge entries\n\n" +
		CmdConfig + " - View current configuration\n" +
		CmdGroups + " - View groups I'm in where you're admin\n" +
		CmdSetup + " - Switch to a different group"

	MsgHelp = "Available Commands\n\n" +
		"Setup:\n" +
		CmdSetup + " - Select a group and configure\n" +
		CmdConfig + " - View current config\n\n" +
		"Configuration (for selected group):\n" +
		CmdSetSystemPrompt + " - Core AI instructions\n" +
		CmdSetBio + " - Bot identity\n" +
		CmdSetTopics + " - Topics to cover\n" +
		CmdSetExamples + " - Example messages\n" +
		CmdSetStyle + " - Chat style\n\n" +
		"Knowledge Base:\n" +
		CmdAddKnowledge + " - Upload a file (PDF, .md, .txt)\n" +
		CmdAddURL + " - Add knowledge from URL\n" +
		CmdListKnowledge + " - List all knowledge\n" +
		CmdDeleteKnowledge + " <id> - Delete a knowledge entry\n\n" +
		"Info:\n" +
		CmdGroups + " - View your admin groups\n" +
		CmdPlan + " - View plan and usage\n" +
		CmdHelp + " - Show this message"
)
