package errors

import (
	stderrors "errors"
	"strings"
	"testing"
	"time"
)

func TestStableErrorsExposeContextAndCauses(t *testing.T) {
	cause := stderrors.New("boom")
	values := []error{
		&StartError{Err: cause},
		&IdleTimeoutError{Duration: time.Second},
		&UnclosedPlaceholderError{},
		&UnknownPlaceholderError{Key: "name"},
		&ExtraArgumentFormError{Index: 2},
		&UnsafeExtraArgumentError{Index: 3, Name: "--output"},
	}
	for _, err := range values {
		if strings.TrimSpace(err.Error()) == "" {
			t.Fatalf("empty error text for %T", err)
		}
	}
	if !stderrors.Is(values[0], cause) {
		t.Fatal("StartError did not preserve its cause")
	}
}
