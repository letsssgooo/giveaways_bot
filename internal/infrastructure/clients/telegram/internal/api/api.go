// Package api содержит проверку payload и безопасные ошибки Bot API.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
	"unicode/utf16"

	"github.com/go-telegram/bot"
)

func TextLength(text string) int {
	length := 0
	for _, character := range text {
		length += utf16.RuneLen(character)
	}
	return length
}

func DecodePayload(payload json.RawMessage, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return errors.New("unexpected trailing payload")
	}
	return nil
}

type Error struct {
	Method     string
	Code       int
	RetryDelay time.Duration
	Reason     string
	Cause      error
}

func (e *Error) Error() string {
	if e.Code != 0 {
		return fmt.Sprintf("telegram %s: %s (code %d)", e.Method, e.Reason, e.Code)
	}
	return "telegram " + e.Method + ": " + e.Reason
}

func (e *Error) Unwrap() error { return e.Cause }

// Wrap сохраняет только классификацию; текст SDK, URL и тело ответа могут содержать токен.
func Wrap(method string, err error) error {
	if err == nil {
		return nil
	}
	result := &Error{Method: method, Reason: "request failed"}
	if errors.Is(err, context.Canceled) {
		result.Cause = context.Canceled
		result.Reason = "request canceled"
		return result
	}
	if errors.Is(err, context.DeadlineExceeded) {
		result.Cause = context.DeadlineExceeded
		result.Reason = "request deadline exceeded"
		return result
	}
	if existing, ok := errors.AsType[*Error](err); ok {
		return &Error{Method: method, Code: existing.Code, RetryDelay: existing.RetryDelay, Reason: existing.Reason}
	}
	if limited, ok := errors.AsType[*bot.TooManyRequestsError](err); ok {
		result.Code = http.StatusTooManyRequests
		result.RetryDelay = RetryDuration(int64(limited.RetryAfter))
		return result
	}
	if _, ok := errors.AsType[*bot.MigrateError](err); ok {
		result.Code = http.StatusBadRequest
		return result
	}
	for _, known := range []struct {
		err  error
		code int
	}{
		{bot.ErrorBadRequest, http.StatusBadRequest}, {bot.ErrorForbidden, http.StatusForbidden},
		{bot.ErrorUnauthorized, http.StatusUnauthorized}, {bot.ErrorConflict, http.StatusConflict},
		{bot.ErrorNotFound, http.StatusNotFound},
	} {
		if errors.Is(err, known.err) {
			result.Code = known.code
			break
		}
	}
	return result
}

func RetryDuration(seconds int64) time.Duration {
	if seconds <= 0 {
		return 0
	}
	const maxSeconds = int64((1<<63 - 1) / time.Second)
	if seconds > maxSeconds {
		seconds = maxSeconds
	}
	return time.Duration(seconds) * time.Second
}

func RetryAfter(err error) time.Duration {
	if failure, ok := errors.AsType[*Error](err); ok {
		return failure.RetryDelay
	}
	return 0
}

func IsRateLimited(err error) bool {
	failure, ok := errors.AsType[*Error](err)
	return ok && failure.Code == http.StatusTooManyRequests
}

func IsPermanent(err error) bool {
	failure, ok := errors.AsType[*Error](err)
	return ok && (failure.Code == http.StatusBadRequest || failure.Code == http.StatusForbidden)
}

func IsFatal(err error) bool {
	failure, ok := errors.AsType[*Error](err)
	return ok && (failure.Code == http.StatusUnauthorized || failure.Code == http.StatusConflict)
}
