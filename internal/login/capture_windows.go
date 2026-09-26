//go:build windows && cgo

package login

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"runtime"
	"strings"
	"sync"

	webview "github.com/webview/webview_go"

	"github.com/WWDELE114514/DeepSider2API/internal/config"
)

// captureMutex serialises logins because WEBVIEW2_USER_DATA_FOLDER is a
// process-wide environment variable.
var captureMutex sync.Mutex

// hookScriptWebview patches fetch/XHR and forwards the first captured token
// pair to the Go side through the bound ds2apiCapture function.
const hookScriptWebview = `(function(){
  if (window.__DS2API_HOOKED__) return; window.__DS2API_HOOKED__ = true;
  function norm(s){ return String(s == null ? '' : s).replace(/\s/g, ''); }
  function report(d){
    try {
      if (!d) return;
      var token = d.token || d.accessToken || d.access_token;
      if (!token) return;
      window.ds2apiCapture(JSON.stringify({
        token: norm(token),
        refreshToken: norm(d.refreshToken || d.refresh_token || ''),
        email: d.email || ''
      }));
    } catch (e) {}
  }
  function pick(obj){ try { report(obj && obj.data ? obj.data : obj); } catch (e) {} }
  function isTarget(u){
    if (!u) return false; u = String(u);
    return u.indexOf('/user/login') >= 0
        || u.indexOf('/user/google-onetap-login') >= 0
        || u.indexOf('/user/google-login') >= 0
        || u.indexOf('/user/refreshtoken') >= 0;
  }
  var of = window.fetch;
  if (of) {
    window.fetch = function(input, init){
      var url = (typeof input === 'string') ? input : (input && input.url);
      return of.apply(this, arguments).then(function(res){
        try { if (isTarget(url || (res && res.url))) { res.clone().json().then(pick).catch(function(){}); } } catch (e) {}
        return res;
      });
    };
  }
  var oo = XMLHttpRequest.prototype.open;
  XMLHttpRequest.prototype.open = function(m, u){ try { this.__ds2api_url = u; } catch (e) {} return oo.apply(this, arguments); };
  var os = XMLHttpRequest.prototype.send;
  XMLHttpRequest.prototype.send = function(){
    var self = this;
    try {
      this.addEventListener('load', function(){
        try { if (isTarget(self.__ds2api_url || self.responseURL)) { pick(JSON.parse(self.responseText)); } } catch (e) {}
      });
    } catch (e) {}
    return os.apply(this, arguments);
  };
})();`

func capture(ctx context.Context, lc config.Login) (Result, error) {
	captureMutex.Lock()
	defer captureMutex.Unlock()

	profileDir, err := os.MkdirTemp("", "ds2api-webview2-")
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(profileDir)

	// A private WebView2 profile keeps the user's Edge data untouched.
	prev, hadPrev := os.LookupEnv("WEBVIEW2_USER_DATA_FOLDER")
	_ = os.Setenv("WEBVIEW2_USER_DATA_FOLDER", profileDir)
	defer func() {
		if hadPrev {
			_ = os.Setenv("WEBVIEW2_USER_DATA_FOLDER", prev)
		} else {
			_ = os.Unsetenv("WEBVIEW2_USER_DATA_FOLDER")
		}
	}()

	resultCh := make(chan Result, 1)
	done := make(chan struct{})

	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		defer close(done)

		w := webview.New(false)
		if w == nil {
			return
		}
		defer w.Destroy()

		w.SetTitle("DeepSider 登录")
		w.SetSize(1024, 780, webview.HintNone)

		_ = w.Bind("ds2apiCapture", func(payload string) string {
			var r Result
			if json.Unmarshal([]byte(payload), &r) == nil && strings.TrimSpace(r.Token) != "" {
				r.Token = strings.TrimSpace(r.Token)
				r.RefreshToken = strings.TrimSpace(r.RefreshToken)
				select {
				case resultCh <- r:
				default:
				}
				go w.Terminate()
			}
			return "ok"
		})

		w.Init(hookScriptWebview)
		w.Navigate(lc.Page)
		w.Run()
	}()

	log.Printf("[login] 已打开登录窗口: %s", lc.Page)

	select {
	case r := <-resultCh:
		return r, nil
	case <-ctx.Done():
		return Result{}, errors.New("登录超时或已取消")
	case <-done:
		return Result{}, fmt.Errorf("登录窗口已关闭")
	}
}
