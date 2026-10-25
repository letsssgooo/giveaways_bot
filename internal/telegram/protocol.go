// Package telegram содержит общие идентификаторы и лимиты Telegram-интерфейса.
package telegram

const (
	CommandStart  = "start"
	CommandMenu   = "menu"
	CommandHelp   = "help"
	CommandAdmin  = "admin"
	CommandCancel = "cancel"
	CommandPrizes = "prizes"
)

const (
	MessageKindText           = "send_message"
	MessageKindPhoto          = "send_photo"
	MessageKindCallbackAnswer = "answer_callback_query"
	MessageKindReport         = "send_report"
	MessageKindCommands       = "sync_commands"
)

// CommandsRequest задает личный чат. Права читаются заново перед отправкой.
type CommandsRequest struct {
	ChatID int64 `json:"chat_id"`
}

const (
	// MaxMessageLength — предел длины текста в единицах UTF-16.
	MaxMessageLength = 4096
	WelcomeBannerRef = "builtin:welcome"
)
