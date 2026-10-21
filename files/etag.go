package files

import (
	"fmt"
	"os"
	"strings"
)

func FileETag(info os.FileInfo) string {
	return fmt.Sprintf(`"%x-%x-%s"`, info.ModTime().UnixNano(), info.Size(), etagIdentity(info))
}

func (i *FileInfo) ETag() string {
	if i.identity == nil {
		return ""
	}
	return FileETag(i.identity)
}

func MatchETag(value string, info os.FileInfo) bool {
	if value == "" {
		return true
	}
	if info == nil {
		return false
	}
	etag := FileETag(info)
	for _, candidate := range strings.Split(value, ",") {
		if strings.TrimSpace(candidate) == "*" || strings.TrimSpace(candidate) == etag {
			return true
		}
	}
	return false
}
