package csv

import (
	"bytes"
	"encoding/csv"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	entity "github.com/letsssgooo/giveaways_bot/internal/entity/reporting"
)

const csvBOM = "\xef\xbb\xbf"

var reportMoscow = time.FixedZone("Europe/Moscow", 3*60*60)

func ParticipantsCSV(rows []entity.Participant) ([]byte, error) {
	return renderCSV(len(rows), []string{
		"Telegram ID", "ID розыгрыша", "Тип розыгрыша", "Вес участника", "ID партнера", "Партнер", "ID дня партнера", "Дата", "Дата и время регистрации (МСК)",
	}, func(index int) []string {
		row := rows[index]
		return []string{
			integer(row.TelegramID), optionalID(row.DrawID), drawKind(row.DrawKind), strconv.Itoa(row.Weight),
			optionalID(row.PartnerID), row.PartnerName, optionalID(row.PartnerDayID),
			date(row.Date), timestamp(row.RegisteredAt),
		}
	})
}

func WinnersCSV(rows []entity.Winner) ([]byte, error) {
	return renderCSV(len(rows), []string{
		"ID розыгрыша", "Тип розыгрыша", "ID партнера", "Партнер", "ID дня партнера", "Дата",
		"Telegram ID", "Дата и время регистрации (МСК)", "Розыгрыш проведен (МСК)", "Результат утвержден (МСК)",
		"Вес участника", "Роль", "Позиция", "ID приза", "Приз", "Призовое место", "Количество единиц",
		"ID назначения приза", "Статус назначения", "Текущее назначение", "Получение подтверждено (МСК)",
		"Имя", "Фамилия", "Согласие получено (МСК)", "Версия согласия",
	}, func(index int) []string {
		row := rows[index]
		return []string{
			integer(row.DrawID), drawKind(row.DrawKind), optionalID(row.PartnerID), row.PartnerName, optionalID(row.PartnerDayID), date(row.Date),
			integer(row.TelegramID), timestamp(row.RegisteredAt), timestamp(row.PerformedAt), timestamp(row.ApprovedAt),
			strconv.Itoa(row.Weight), selectionRole(row.Role), strconv.Itoa(row.Position), optionalID(row.PrizeID), row.PrizeName,
			optionalID(int64(row.PlaceNumber)), optionalID(int64(row.Units)), optionalID(row.AwardID), row.AwardStatus,
			currentAward(row.AwardID, row.AwardCurrent), timestamp(row.ConfirmedAt), row.FirstName, row.LastName,
			timestamp(row.ConsentGivenAt), row.ConsentVersion,
		}
	})
}

func renderCSV(count int, header []string, row func(int) []string) ([]byte, error) {
	if count > entity.MaxExportRows {
		return nil, entity.ErrExportTooLarge
	}
	output := &limitedCSV{}
	if _, err := output.Write([]byte(csvBOM)); err != nil {
		return nil, err
	}
	writer := csv.NewWriter(output)
	writer.Comma = ';'
	if err := writer.Write(header); err != nil {
		return nil, err
	}
	for i := range count {
		fields := row(i)
		for j := range fields {
			fields[j] = spreadsheetText(fields[j])
		}
		if err := writer.Write(fields); err != nil {
			return nil, err
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return nil, err
	}
	return output.buffer.Bytes(), nil
}

type limitedCSV struct{ buffer bytes.Buffer }

func (w *limitedCSV) Write(value []byte) (int, error) {
	if len(value) > entity.MaxExportBytes-w.buffer.Len() {
		return 0, entity.ErrExportTooLarge
	}
	return w.buffer.Write(value)
}

// Начальная табуляция принуждает Excel воспринимать формулу как текст.
// encoding/csv заключает такую ячейку в кавычки и экранирует ее содержимое.
// https://community.owasp.org/attacks/CSV_Injection
func spreadsheetText(value string) string {
	trimmed := strings.TrimLeftFunc(value, func(char rune) bool { return unicode.IsSpace(char) || unicode.IsControl(char) })
	char, _ := utf8.DecodeRuneInString(trimmed)
	if strings.ContainsRune("=+-@＝＋－＠", char) {
		return "\t" + value
	}
	return value
}

func integer(value int64) string { return strconv.FormatInt(value, 10) }

func optionalID(value int64) string {
	if value == 0 {
		return ""
	}
	return integer(value)
}

func timestamp(value *time.Time) string {
	if value == nil {
		return ""
	}
	return timestampValue(*value)
}

func timestampValue(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.In(reportMoscow).Format(time.RFC3339)
}

func date(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.Format(time.DateOnly)
}

func drawKind(value string) string {
	switch value {
	case "daily":
		return "Ежедневный"
	case "final":
		return "Финальный"
	default:
		return value
	}
}

func selectionRole(value string) string {
	switch value {
	case "winner":
		return "Победитель"
	case "reserve":
		return "Резерв"
	default:
		return value
	}
}

func currentAward(id int64, current bool) string {
	if id == 0 {
		return ""
	}
	if current {
		return "Да"
	}
	return "Нет"
}
