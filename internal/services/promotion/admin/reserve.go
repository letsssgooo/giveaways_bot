package admin

import "context"

func (s *AdminService) FinalReserveCount(ctx context.Context, actor int64) (int, error) {
	if _, err := s.requireAccess(ctx, actor, true, false); err != nil {
		return 0, err
	}
	return s.repository.AdminFinalReserveCount(ctx)
}

func (s *AdminService) SaveFinalReserveCount(ctx context.Context, actor int64, count int) error {
	if _, err := s.requireAccess(ctx, actor, true, true); err != nil {
		return err
	}
	if count < 1 || count > 10000 {
		return invalid("число резервистов должно быть от 1 до 10000")
	}
	return s.repository.UpdateFinalReserveCount(ctx, count)
}
