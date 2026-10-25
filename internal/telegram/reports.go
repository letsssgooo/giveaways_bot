package telegram

// ReportRequest сохраняет только параметры выгрузки. Содержимое файла
// формируется при отправке после повторной проверки текущих прав получателя.
type ReportRequest struct {
	ActorID   int64  `json:"actor_id"`
	ChatID    int64  `json:"chat_id"`
	PartnerID int64  `json:"partner_id"`
	Kind      string `json:"kind"`
}
