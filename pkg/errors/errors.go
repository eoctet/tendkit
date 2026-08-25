package errors

import (
	"fmt"
	"time"
)

type StartError struct{ Err error }

func (err *StartError) Error() string { return "start command: " + err.Err.Error() }
func (err *StartError) Unwrap() error { return err.Err }

type IdleTimeoutError struct{ Duration time.Duration }

func (err *IdleTimeoutError) Error() string {
	return fmt.Sprintf("command idle timeout after %s", err.Duration)
}

type UnclosedPlaceholderError struct{}

func (*UnclosedPlaceholderError) Error() string { return "unclosed template placeholder" }

type UnknownPlaceholderError struct{ Key string }

func (err *UnknownPlaceholderError) Error() string {
	return fmt.Sprintf("unknown template placeholder %q", err.Key)
}

type ExtraArgumentFormError struct{ Index int }

func (err *ExtraArgumentFormError) Error() string {
	return fmt.Sprintf("downloader extra argument %d must use --name=value", err.Index)
}

type UnsafeExtraArgumentError struct {
	Index int
	Name  string
}

func (err *UnsafeExtraArgumentError) Error() string {
	return fmt.Sprintf("unsafe downloader extra argument %d: %s", err.Index, err.Name)
}
