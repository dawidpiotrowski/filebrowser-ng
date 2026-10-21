package files

import (
	"io"
	"io/fs"
	"os"
)

// Filter at the filesystem boundary so every walker (including archives and
// search) skips staging rather than exposing names or failing on hidden entries.
type visibleDirectory struct{ *os.File }

func (d *visibleDirectory) Readdir(n int) ([]os.FileInfo, error) {
	var result []os.FileInfo
	for n <= 0 || len(result) < n {
		count := n
		if n > 0 {
			count = n - len(result)
		}
		entries, err := d.File.Readdir(count)
		for _, entry := range entries {
			if !IsReservedPath(entry.Name()) {
				result = append(result, entry)
			}
		}
		if err != nil {
			if err == io.EOF && len(result) > 0 {
				return result, nil
			}
			return result, err
		}
		if n <= 0 {
			break
		}
	}
	return result, nil
}

func (d *visibleDirectory) Readdirnames(n int) ([]string, error) {
	entries, err := d.Readdir(n)
	names := make([]string, len(entries))
	for i, entry := range entries {
		names[i] = entry.Name()
	}
	return names, err
}

func (d *visibleDirectory) ReadDir(n int) ([]fs.DirEntry, error) {
	entries, err := d.Readdir(n)
	result := make([]fs.DirEntry, len(entries))
	for i, entry := range entries {
		result[i] = fs.FileInfoToDirEntry(entry)
	}
	return result, err
}
