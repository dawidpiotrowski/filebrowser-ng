//go:build !linux && !darwin && !windows

package files

import (
	"errors"
	"os"
)

func preserveMetadata(*os.File, *os.File, os.FileInfo) error { return errors.ErrUnsupported }

func etagIdentity(os.FileInfo) string { return "" }
