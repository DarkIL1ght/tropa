// Package dm — личные сообщения. Писать друг другу могут только друзья:
// дружба взаимная, поэтому незнакомец не может написать первым.
package dm

import (
	"fmt"
	"time"
)

type Message struct {
	ID        int64
	FromID    int64
	ToID      int64
	Name      string // имя отправителя
	Body      string
	CreatedAt time.Time
}

// Key — общий ключ переписки пары в любом порядке (для потоков SSE).
func Key(a, b int64) string { return fmt.Sprintf("dm:%d:%d", min(a, b), max(a, b)) }

// Thread — друг в списке переписок: последнее сообщение и сколько непрочитанных.
type Thread struct {
	UserID int64
	Name   string
	Last   *Message // nil — переписки ещё нет
	Unread int
}
