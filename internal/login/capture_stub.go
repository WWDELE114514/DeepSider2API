//go:build !(windows && cgo)

package login

import (
	"context"
	"errors"

	"github.com/WWDELE114514/DeepSider2API/internal/config"
)

// capture is unavailable on builds without the Windows webview (cgo) support,
// e.g. the Linux/Docker build or a CGO_ENABLED=0 Windows build.
func capture(ctx context.Context, lc config.Login) (Result, error) {
	return Result{}, errors.New("交互登录需要带 cgo 的 Windows 构建（WebView2）")
}
