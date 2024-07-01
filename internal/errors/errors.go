package errors

import (
	"errors"
	"fmt"
)

var (
	As  = errors.As
	Is  = errors.Is
	New = errors.New
)

func Wrap(err error, message string) error {
	if err != nil {
		err = fmt.Errorf("%s: %w", message, err)
	}
	return err
}

func Wrapf(err error, format string, args ...interface{}) error {
	if err != nil {
		err = fmt.Errorf("%s: %w", fmt.Sprintf(format, args...), err)
	}
	return err
}
