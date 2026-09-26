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

// hookScriptWebview patches fetch/XHR, recursively looks for a token in login
// responses, and additionally polls IndexedDB/localStorage (localforage) every
// two seconds. Anything found is forwarded to Go through ds2apiCapture.
const hookScriptWebview = `(function(){
  if (window.__DS2API_HOOKED__) return; window.__DS2API_HOOKED__ = true;

  function norm(s){ return String(s == null ? '' : s).replace(/\s/g, ''); }
  function log(m){ try { window.ds2apiLog(String(m)); } catch(e){} }

  function findToken(o, depth){
    if (!o || depth > 6) return null;
    if (typeof o === 'string') {
      var t = o.trim();
      if (t.charAt(0) === '{' || t.charAt(0) === '[') { try { return findToken(JSON.parse(t), depth+1); } catch(e){ return null; } }
      return null;
    }
    if (typeof o !== 'object') return null;
    var tok = o.token || o.accessToken || o.access_token;
    if (tok && typeof tok === 'string' && tok.length > 20) {
      return { token: norm(tok), refreshToken: norm(o.refreshToken || o.refresh_token || ''), email: o.email || o.mail || '' };
    }
    for (var k in o) { try { var r = findToken(o[k], depth+1); if (r) return r; } catch(e){} }
    return null;
  }

  function report(r){
    if (!r || !r.token) return;
    window.__DS2API_TOKEN__ = r;
    try { window.ds2apiCapture(JSON.stringify(r)); } catch(e){}
  }
  function inspect(obj){ try { var r = findToken(obj, 0); if (r) report(r); } catch(e){} }

  function isLoginUrl(u){ if(!u) return false; u = String(u); return u.indexOf('/user/') >= 0 || u.indexOf('login') >= 0; }

  var of = window.fetch;
  if (of) {
    window.fetch = function(input, init){
      var url = (typeof input === 'string') ? input : (input && input.url);
      return of.apply(this, arguments).then(function(res){
        try {
          if (isLoginUrl(url || (res && res.url))) {
            log('fetch ' + res.status + ' ' + (url || res.url));
            res.clone().json().then(inspect).catch(function(){});
          }
        } catch (e) {}
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
        try {
          var u = self.__ds2api_url || self.responseURL;
          if (isLoginUrl(u)) { log('xhr ' + self.status + ' ' + u); inspect(JSON.parse(self.responseText)); }
        } catch (e) {}
      });
    } catch (e) {}
    return os.apply(this, arguments);
  };

  var db = null;
  function openDB(){
    try {
      var req = indexedDB.open('localforage');
      req.onsuccess = function(e){ db = e.target.result; };
      req.onerror = function(){};
    } catch(e){}
  }
  openDB();

  function scanStorage(){
    try {
      for (var i=0;i<localStorage.length;i++){
        var v = localStorage.getItem(localStorage.key(i));
        if (v && v.indexOf('token') >= 0) { try { inspect(JSON.parse(v)); } catch(e){} }
      }
    } catch(e){}
    try {
      if (!db) { openDB(); return; }
      var names = db.objectStoreNames;
      for (var n=0;n<names.length;n++){
        (function(name){
          try {
            var store = db.transaction(name, 'readonly').objectStore(name);
            var all = store.getAll();
            all.onsuccess = function(){ var arr = all.result || []; for (var j=0;j<arr.length;j++){ inspect(arr[j]); } };
          } catch(e){}
        })(names[n]);
      }
    } catch(e){}
  }

  setInterval(function(){
    try { if (window.__DS2API_TOKEN__ && window.__DS2API_TOKEN__.token) { report(window.__DS2API_TOKEN__); return; } } catch(e){}
    scanStorage();
  }, 2000);

  log('hook installed');
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
					go w.Terminate()
				default:
				}
			}
			return "ok"
		})
		_ = w.Bind("ds2apiLog", func(msg string) string {
			log.Printf("[login][page] %s", msg)
			return ""
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
