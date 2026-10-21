//go:build !linux && !darwin && !windows

package files

import (
	"errors"
	"os"
)

func renameAt(*os.File, string, *os.File, string, bool) error { return errors.ErrUnsupported }
func syncDirectory(*os.File) error                            { return errors.ErrUnsupported }
