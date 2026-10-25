package csv

import (
	"bytes"
	"encoding/csv"
	"errors"
	"strings"
	"testing"
	"time"

	entity "github.com/letsssgooo/giveaways_bot/internal/entity/reporting"
)

func reportRecords(t *testing.T, data []byte) [][]string {
	t.Helper()
	if !bytes.HasPrefix(data, []byte(csvBOM)) {
		t.Fatal("CSV lacks UTF-8 BOM")
	}
	reader := csv.NewReader(bytes.NewReader(data[len(csvBOM):]))
	reader.Comma = ';'
	rows, err := reader.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestParticipantsCSVPreservesQuotedTextAndMoscowTime(t *testing.T) {
	date := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
	registered := time.Date(2026, 10, 24, 23, 4, 5, 0, time.UTC)
	name := "Сеть; \"А\"\nВторая строка"
	data, err := ParticipantsCSV([]entity.Participant{{TelegramID: 1234567890123, PartnerID: 7, PartnerDayID: 9,
		PartnerName: name, Date: date, RegisteredAt: &registered, DrawID: 10, DrawKind: "daily", Weight: 1}})
	if err != nil {
		t.Fatal(err)
	}
	rows := reportRecords(t, data)
	if len(rows) != 2 || len(rows[0]) != 9 || len(rows[1]) != 9 {
		t.Fatalf("unexpected CSV rows: %v", rows)
	}
	want := []string{"1234567890123", "10", "Ежедневный", "1", "7", name, "9", "2026-10-25", "2026-10-25T02:04:05+03:00"}
	for i := range want {
		if rows[1][i] != want[i] {
			t.Errorf("field %d = %q, want %q", i, rows[1][i], want[i])
		}
	}
}

func TestCSVQuotesAndNeutralizesSpreadsheetFormulas(t *testing.T) {
	for _, value := range []string{
		"=HYPERLINK(\"https://example.com\";\"open\")", "+1+2", "-1+2", "@SUM(1;2)",
		"＝1+2", "＋1+2", "－1+2", "＠SUM(1;2)", "  \t\r\n=1+2", "\x00+1+2", "\u00a0@SUM(1)",
	} {
		t.Run(value, func(t *testing.T) {
			data, err := ParticipantsCSV([]entity.Participant{{PartnerName: value}})
			if err != nil {
				t.Fatal(err)
			}
			rows := reportRecords(t, data)
			// encoding/csv normalizes CRLF when reading even inside quoted cells.
			if rows[1][5] != "\t"+strings.ReplaceAll(value, "\r\n", "\n") || !bytes.Contains(data, []byte(";\"\t")) {
				t.Fatalf("formula was not protected inside a quoted cell: %q", data)
			}
		})
	}
	if got := spreadsheetText("Обычное имя"); got != "Обычное имя" {
		t.Fatalf("ordinary text changed: %q", got)
	}
}

func TestParticipantsCSVPreservesFinalWeightWithoutRegistration(t *testing.T) {
	data, err := ParticipantsCSV([]entity.Participant{{TelegramID: 42, DrawID: 7, DrawKind: "final", Weight: 10}})
	if err != nil {
		t.Fatal(err)
	}
	rows := reportRecords(t, data)
	want := []string{"42", "7", "Финальный", "10", "", "", "", "", ""}
	for i := range want {
		if rows[1][i] != want[i] {
			t.Errorf("final field %d = %q, want %q", i, rows[1][i], want[i])
		}
	}
}

func TestWinnersCSVIncludesAssignmentAndConsentMetadata(t *testing.T) {
	moment := time.Date(2026, 10, 25, 10, 0, 0, 0, time.UTC)
	data, err := WinnersCSV([]entity.Winner{{DrawID: 5, DrawKind: "final", TelegramID: 42, Date: moment,
		PerformedAt: &moment, ApprovedAt: &moment, Weight: 10, Role: "reserve", Position: 2,
		PrizeID: 8, PrizeName: "Два билета", PlaceNumber: 3, Units: 2, AwardID: 11,
		AwardStatus: "confirmed", AwardCurrent: true, ConfirmedAt: &moment,
		FirstName: "=first", LastName: "Фамилия", ConsentGivenAt: &moment, ConsentVersion: "+consent-v1"}})
	if err != nil {
		t.Fatal(err)
	}
	rows := reportRecords(t, data)
	if len(rows) != 2 || len(rows[0]) != len(rows[1]) {
		t.Fatalf("unexpected CSV rows: %v", rows)
	}
	values := make(map[string]string, len(rows[0]))
	for i, header := range rows[0] {
		values[header] = rows[1][i]
	}
	for header, want := range map[string]string{
		"Тип розыгрыша": "Финальный", "Telegram ID": "42", "ID партнера": "",
		"Дата и время регистрации (МСК)": "", "Вес участника": "10", "Роль": "Резерв",
		"ID назначения приза": "11", "Призовое место": "3", "Количество единиц": "2",
		"Текущее назначение": "Да", "Статус назначения": "confirmed", "Имя": "\t=first",
		"Согласие получено (МСК)": "2026-10-25T13:00:00+03:00", "Версия согласия": "\t+consent-v1",
	} {
		if values[header] != want {
			t.Errorf("%s = %q, want %q", header, values[header], want)
		}
	}
}

func TestCSVEmptyExportsKeepHeaders(t *testing.T) {
	for _, render := range []func() ([]byte, error){
		func() ([]byte, error) { return ParticipantsCSV(nil) },
		func() ([]byte, error) { return WinnersCSV(nil) },
	} {
		data, err := render()
		if err != nil {
			t.Fatal(err)
		}
		if rows := reportRecords(t, data); len(rows) != 1 || len(rows[0]) == 0 {
			t.Fatalf("empty export lost headers: %v", rows)
		}
	}
}

func TestCSVRejectsOversizedExportsWithoutReturningPartialData(t *testing.T) {
	if data, err := ParticipantsCSV(make([]entity.Participant, entity.MaxExportRows+1)); !errors.Is(err, entity.ErrExportTooLarge) || data != nil {
		t.Fatalf("row limit: bytes=%d err=%v", len(data), err)
	}
	if data, err := WinnersCSV([]entity.Winner{{PrizeName: strings.Repeat("x", entity.MaxExportBytes)}}); !errors.Is(err, entity.ErrExportTooLarge) || data != nil {
		t.Fatalf("byte limit: bytes=%d err=%v", len(data), err)
	}
}
