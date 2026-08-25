package component

import "time"

// MessageBubble holds one bounded-lifetime header notification.
type MessageBubble struct {
	Message string
	Error   bool
	Until   time.Time
}

func (bubble *MessageBubble) Set(message string, isError bool, now time.Time, normalDuration, errorDuration time.Duration) {
	bubble.Message = message
	bubble.Error = isError
	if message == "" {
		bubble.Until = time.Time{}
		return
	}
	duration := normalDuration
	if isError {
		duration = errorDuration
	}
	bubble.Until = now.Add(duration)
}

func (bubble *MessageBubble) Clear() { *bubble = MessageBubble{} }

func (bubble *MessageBubble) Expire(now time.Time) bool {
	if bubble.Message == "" || bubble.Until.IsZero() || now.Before(bubble.Until) {
		return false
	}
	bubble.Clear()
	return true
}
