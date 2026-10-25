package view

import (
	"unicode/utf16"

	"github.com/go-telegram/bot/models"

	telegramprotocol "github.com/letsssgooo/giveaways_bot/internal/telegram"
)

const (
	HelpText                    = "Выберите действие в меню: получите промокод, участвуйте в розыгрыше или посмотрите материалы акции.\n\nДоступные команды:\n/start — приветствие\n/menu — главное меню\n/prizes — мои призы и получение\n/help — помощь"
	WelcomeText                 = "Добро пожаловать в Бот розыгрышей"
	InvalidInputText            = "Не удалось распознать сообщение.\n\n" + HelpText
	StaleButtonText             = "Кнопка устарела. Откройте /menu."
	NoPartnersText              = "Сейчас нет партнеров с открытым приемом. Попробуйте позже."
	ClosedPartnerText           = "Прием у выбранного партнера уже закрыт или расписание изменилось. Выберите партнера заново в меню."
	DefaultInstructionsTemplate = "Получите промокод партнера или зарегистрируйтесь в его ежедневном розыгрыше. Для регистрации потребуется подписка на канал или группу партнера.\n\nКаждый день, когда вы получили промокод или зарегистрировались, дает один шанс в финальном розыгрыше. Несколько действий в один день не увеличивают число шансов. Максимум — %d шансов."
	MissingRulesText            = "Правила акции пока не опубликованы. Загляните позже."
	ChoosePartnerText           = "Выберите партнера:"
	ChancesTextTemplate         = "Твой фестивальный пропуск\n\nНакоплено шансов: %d из %d"
	ChancesRuleText             = "Чем больше дней розыгрыша ты посетишь, тем выше вероятность выиграть главный приз.\n\nОдин активный день дает один шанс. Получение промокода и регистрация в один день не суммируются."
	ChancesLimitText            = "Максимум шансов достигнут! Можно продолжать получать промокоды и участвовать в ежедневных розыгрышах. Новые активные дни сохраняются в истории."
	ChancesMissingScheduleText  = "Расписание акции пока не опубликовано."
	ChancesRemainingTemplate    = "Осталось дней участия: %d\nПо расписанию акции; сегодня учитывается, если прием еще не завершен."
	ChancesMissingPrizeText     = "Главный приз: информация пока не опубликована."
	ChancesPrizeTemplate        = "• %s\nПобедителей: %d; каждому: %d шт."
	ChancesHistoryTemplate      = "Активные дни: %d (даты по МСК)"
	ChancesEmptyHistoryText     = "Активных дней пока нет. Получи промокод или зарегистрируйся в ежедневном розыгрыше."
	ChancesHistoryPageTemplate  = "История: страница %d из %d"
	ChooseCodeText              = "Выберите партнера или ранее полученный промокод:"
	NoCodesText                 = "Сейчас нет партнеров с открытой выдачей промокодов. Ранее выданных кодов у вас пока нет."
	IssuedCodeTextTemplate      = "Твой фестивальный бонус от %s готов 🎁\n\nПромокод: %s"
	RepeatedCodeTextTemplate    = "Твой промокод от %s за %s\n\nПромокод: %s"
	CodeValidUntilTextTemplate  = "Используй его до %s (МСК)."
	MissingCodeInstructions     = "Инструкция по применению пока не опубликована."
	CodeParticipationText       = "Теперь можешь принять участие в розыгрыше призов дня."
	CodeChancesTextTemplate     = "Накоплено шансов: %d из %d"
	ExhaustedCodesTextTemplate  = "Промокоды от %s уже закончились — их разобрали быстрее всех 🔥\n\nНо ты все еще можешь подписаться на партнера и принять участие в розыгрыше призов дня."
	CodeOptionTextTemplate      = "%s · %s"
	ReceivedCodeSuffix          = " · получен"
	PreviousPageButtonText      = "← Назад"
	NextPageButtonText          = "Далее →"
)

const (
	CodeButtonText    = "Получить промокод"
	JoinButtonText    = "Участвовать в розыгрыше"
	HowButtonText     = "Как участвовать"
	ChancesButtonText = "Мои шансы"
	RulesButtonText   = "Правила акции"
	HomeButtonText    = "В меню"
)

func MainKeyboard() *models.InlineKeyboardMarkup {
	return &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{{Text: CodeButtonText, CallbackData: ActionCode}},
		{{Text: JoinButtonText, CallbackData: ActionJoin}},
		{{Text: HowButtonText, CallbackData: ActionHow}},
		{{Text: ChancesButtonText, CallbackData: ActionChances}},
		{{Text: RulesButtonText, CallbackData: ActionRules}},
	}}
}

func HomeKeyboard() *models.InlineKeyboardMarkup {
	return &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{{Text: HomeButtonText, CallbackData: ActionHome}},
	}}
}

// Telegram ограничивает текст сообщения 4096 символами. Считаем UTF-16,
// чтобы и текст с emoji укладывался в лимит, не разрывая кодовую точку.
func SplitMessage(text string) []string {
	var parts []string
	start, units := 0, 0
	for i, character := range text {
		size := utf16.RuneLen(character)
		if units+size > telegramprotocol.MaxMessageLength {
			parts = append(parts, text[start:i])
			start, units = i, 0
		}
		units += size
	}
	return append(parts, text[start:])
}

const (
	MenuActionPrefix   = "menu:"
	PartnerActionGroup = "partner"
	PartnerActionCode  = "code"
	PartnerActionJoin  = "join"
	PartnerActionParts = 3

	ActionHelp    = "help"
	ActionHome    = MenuActionPrefix + "home"
	ActionCode    = MenuActionPrefix + PartnerActionCode
	ActionJoin    = MenuActionPrefix + PartnerActionJoin
	ActionHow     = MenuActionPrefix + "how"
	ActionChances = MenuActionPrefix + "chances"
	ActionRules   = MenuActionPrefix + "rules"
)
