package fbhttp

import (
	"context"
	"io"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/dawidpiotrowski/filebrowser-ng/v2/diskcache"
	"github.com/dawidpiotrowski/filebrowser-ng/v2/files"
	"github.com/dawidpiotrowski/filebrowser-ng/v2/img"
	"github.com/spf13/afero"
)

func TestPreviewCacheDistinguishesChangesWithinOneSecond(t *testing.T) {
	f := &files.FileInfo{Path: "/photo.png", Size: 10, ModTime: time.Unix(100, 1)}
	before := previewCacheKey(f, PreviewSizeThumb)
	f.ModTime = time.Unix(100, 2)
	if previewCacheKey(f, PreviewSizeThumb) == before {
		t.Fatal("two versions of an image in the same second share a cache key")
	}
	before = previewCacheKey(f, PreviewSizeThumb)
	f.Size++
	if previewCacheKey(f, PreviewSizeThumb) == before {
		t.Fatal("size changes with preserved timestamps share a cache key")
	}
}

type cancellationPreviewService struct{ sawCancellation bool }

func (*cancellationPreviewService) FormatFromExtension(string) (img.Format, error) {
	return img.FormatPng, nil
}

func (s *cancellationPreviewService) Resize(ctx context.Context, _ io.Reader, _, _ int, _ io.Writer, _ ...img.Option) error {
	s.sawCancellation = ctx.Err() == context.Canceled
	return context.Canceled
}

func TestPreviewPropagatesRequestCancellation(t *testing.T) {
	fs := afero.NewMemMapFs()
	if err := afero.WriteFile(fs, "/photo.png", []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	svc := &cancellationPreviewService{}
	r := httptest.NewRequest("GET", "/photo.png", nil).WithContext(ctx)
	_, _ = handleImagePreview(httptest.NewRecorder(), r, svc, diskcache.NewNoOp(), &files.FileInfo{Fs: fs, Path: "/photo.png", Extension: ".png"}, PreviewSizeThumb, true, true)
	if !svc.sawCancellation {
		t.Fatal("disconnected request cannot cancel a queued thumbnail job")
	}
}
