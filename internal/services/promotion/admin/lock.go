package admin

import "context"

// Lock сериализует изменение сценария с назначением и отзывом прав.
// Вызывается в той же транзакции, что и последующая проверка Access.
func (s *AdminService) Lock(ctx context.Context) error {
	return s.repository.LockAdmin(ctx)
}
