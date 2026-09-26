# Third-Party Notices

## workbuddy2api-panel-plus / workbuddy2api-panel / workbuddy2api

The model orchestration feature of this project — the virtual `auto` model with
day/night primary switching and the recursive `model_fallback` degradation
chain (see `internal/config` `AutoModel` / `ModelFallback`, and
`internal/server` `modelChain`) — as well as parts of the gateway and panel
structure, were designed with reference to the following projects:

- https://github.com/JACKY199503/workbuddy2api-panel-plus
- https://github.com/linguo2625469/workbuddy2api-panel
- https://github.com/Sliverkiss/workbuddy2api

Those projects are distributed under the MIT License, reproduced below in full
as required by its terms.

```
MIT License

Copyright (c) 2026 Sliverkiss (original project: https://github.com/Sliverkiss/workbuddy2api)
Copyright (c) 2026 linguo2625469 (this fork: https://github.com/linguo2625469/workbuddy2api-panel)
Copyright (c) 2026 contributors of this fork (added: API key distribution `wbk_` + model orchestration `auto`)

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```

## DeepSider

This project interoperates with the DeepSider web/extension API and reuses its
`sign_wasm` module (`internal/sign/sign_wasm_bg.wasm`) purely to compute the
`i-sign` header. DeepSider and its assets are the property of their respective
owners; no rights to DeepSider's services are granted by this repository.

## Other Go dependencies

Go module dependencies and their licenses are listed in `go.sum` and each
module's own repository (notably `github.com/tetratelabs/wazero` — Apache-2.0,
and `github.com/webview/webview_go` — MIT).
