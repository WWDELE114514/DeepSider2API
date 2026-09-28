// Package sign reuses DeepSider's sign_wasm (wasm-bindgen) to produce the
// i-sign header required by protected endpoints such as
// /api/v2/chat/conversation.
//
// The wasm module is driven through a minimal hand written wasm-bindgen glue
// implemented with wazero. Only three host imports are actually exercised by
// get_sign: Date construction, Date.getTime and object drop. The remaining
// imports are provided as inert stubs so the module can be instantiated.
package sign

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

//go:embed sign_wasm_bg.wasm
var wasmFS embed.FS

const (
	importModule = "wbg"

	fnGetSign       = "get_sign"
	fnStackPointer  = "__wbindgen_add_to_stack_pointer"
	fnMalloc        = "__wbindgen_export_1"
	fnFree          = "__wbindgen_export_3"
	fnWbindgenThrow = "__wbindgen_export_0"
)

type jsDate struct{ ms int64 }

type jsGlobal struct{}

// heap mirrors the index based object table used by wasm-bindgen's JS glue.
// Slots 0..127 are reserved, 128..131 hold the primitive sentinels and the
// rest form a free list.
type heap struct {
	slots []interface{}
	free  int
}

const (
	heapSentinelBase = 128
	heapPrimUndef    = 128
	heapPrimNull     = 129
	heapPrimTrue     = 130
	heapPrimFalse    = 131
	heapFirstFree    = 132
)

func newHeap() *heap {
	h := &heap{slots: make([]interface{}, heapFirstFree)}
	h.slots[heapPrimUndef] = nil
	h.slots[heapPrimNull] = nil
	h.slots[heapPrimTrue] = true
	h.slots[heapPrimFalse] = false
	h.free = heapFirstFree
	return h
}

func (h *heap) add(obj interface{}) uint32 {
	if h.free == len(h.slots) {
		h.slots = append(h.slots, h.free+1)
	}
	idx := h.free
	h.free = h.slots[idx].(int)
	h.slots[idx] = obj
	return uint32(idx)
}

func (h *heap) get(idx uint32) interface{} {
	if int(idx) >= len(h.slots) {
		return nil
	}
	return h.slots[idx]
}

func (h *heap) drop(idx uint32) {
	if int(idx) < heapFirstFree || int(idx) >= len(h.slots) {
		return
	}
	h.slots[idx] = h.free
	h.free = int(idx)
}

func (h *heap) isObject(idx uint32) uint32 {
	_, ok := h.get(idx).(map[string]interface{})
	if ok {
		return 1
	}
	if _, ok := h.get(idx).(jsDate); ok {
		return 1
	}
	if _, ok := h.get(idx).(jsGlobal); ok {
		return 1
	}
	return 0
}

// Signer owns a compiled instance of the signing wasm module.
type Signer struct {
	runtime wazero.Runtime
	module  api.Module
	heap    *heap
	mu      sync.Mutex
}

// New compiles and instantiates the embedded signing module.
func New(ctx context.Context) (*Signer, error) {
	runtime := wazero.NewRuntime(ctx)
	s := &Signer{runtime: runtime, heap: newHeap()}

	if err := s.registerHost(ctx); err != nil {
		_ = runtime.Close(ctx)
		return nil, err
	}

	wasmBytes, err := wasmFS.ReadFile("sign_wasm_bg.wasm")
	if err != nil {
		_ = runtime.Close(ctx)
		return nil, fmt.Errorf("read embedded wasm: %w", err)
	}

	compiled, err := runtime.CompileModule(ctx, wasmBytes)
	if err != nil {
		_ = runtime.Close(ctx)
		return nil, fmt.Errorf("compile wasm: %w", err)
	}

	mod, err := runtime.InstantiateModule(ctx, compiled, wazero.NewModuleConfig().WithName("deepsider_sign"))
	if err != nil {
		_ = runtime.Close(ctx)
		return nil, fmt.Errorf("instantiate wasm: %w", err)
	}

	s.module = mod
	return s, nil
}

// Close releases the underlying wasm runtime.
func (s *Signer) Close(ctx context.Context) error {
	if s.runtime == nil {
		return nil
	}
	return s.runtime.Close(ctx)
}

type hostFn func(m api.Module, stack []uint64)

func (s *Signer) registerHost(ctx context.Context) error {
	b := s.runtime.NewHostModuleBuilder(importModule)

	i32 := []api.ValueType{api.ValueTypeI32}
	i32x2 := []api.ValueType{api.ValueTypeI32, api.ValueTypeI32}
	i32x3 := []api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32}

	reg := func(name string, params, results []api.ValueType, fn hostFn) {
		b.NewFunctionBuilder().
			WithGoModuleFunction(api.GoModuleFunc(func(_ context.Context, m api.Module, stack []uint64) {
				fn(m, stack)
			}), params, results).
			Export(name)
	}

	// Real semantics required by get_sign.
	reg("__wbg_new0_f788a2397c7ca929", nil, i32, func(m api.Module, stack []uint64) {
		stack[0] = uint64(s.heap.add(jsDate{ms: time.Now().UnixMilli()}))
	})
	reg("__wbg_getTime_46267b1c24877e30", i32, []api.ValueType{api.ValueTypeF64}, func(m api.Module, stack []uint64) {
		d, _ := s.heap.get(uint32(stack[0])).(jsDate)
		stack[0] = math.Float64bits(float64(d.ms))
	})
	reg("__wbindgen_object_drop_ref", i32, nil, func(m api.Module, stack []uint64) {
		s.heap.drop(uint32(stack[0]))
	})

	// Inert but well-formed stubs. get_sign never reaches these, they exist so
	// the module can instantiate and so unexpected calls degrade safely.
	reg("__wbindgen_is_object", i32, i32, func(m api.Module, stack []uint64) {
		stack[0] = uint64(s.heap.isObject(uint32(stack[0])))
	})
	reg("__wbindgen_is_function", i32, i32, func(m api.Module, stack []uint64) {
		stack[0] = 0
	})
	reg("__wbindgen_is_null", i32, i32, func(m api.Module, stack []uint64) {
		if uint32(stack[0]) == heapPrimNull {
			stack[0] = 1
		} else {
			stack[0] = 0
		}
	})
	reg("__wbindgen_is_undefined", i32, i32, func(m api.Module, stack []uint64) {
		if uint32(stack[0]) == heapPrimUndef {
			stack[0] = 1
		} else {
			stack[0] = 0
		}
	})
	reg("__wbindgen_string_new", i32x2, i32, func(m api.Module, stack []uint64) {
		ptr, length := uint32(stack[0]), uint32(stack[1])
		str, _ := readString(m, ptr, length)
		stack[0] = uint64(s.heap.add(str))
	})
	reg("__wbindgen_object_clone_ref", i32, i32, func(m api.Module, stack []uint64) {
		stack[0] = uint64(s.heap.add(s.heap.get(uint32(stack[0]))))
	})
	reg("__wbg_static_accessor_GLOBAL_88a902d13a557d07", nil, i32, func(m api.Module, stack []uint64) {
		stack[0] = uint64(s.heap.add(jsGlobal{}))
	})
	reg("__wbg_static_accessor_GLOBAL_THIS_56578be7e9f832b0", nil, i32, func(m api.Module, stack []uint64) {
		stack[0] = uint64(s.heap.add(jsGlobal{}))
	})
	reg("__wbg_static_accessor_SELF_37c5d418e4bf5819", nil, i32, func(m api.Module, stack []uint64) {
		stack[0] = uint64(s.heap.add(jsGlobal{}))
	})
	reg("__wbg_static_accessor_WINDOW_5de37043a91a9c40", nil, i32, func(m api.Module, stack []uint64) {
		stack[0] = uint64(s.heap.add(jsGlobal{}))
	})
	reg("__wbg_has_a5ea9117f258a0ec", i32x2, i32, func(m api.Module, stack []uint64) {
		stack[0] = 0
	})
	reg("__wbg_get_67b2ba62fc30de12", i32x2, i32, func(m api.Module, stack []uint64) {
		stack[0] = uint64(s.heap.add(nil))
	})
	reg("__wbg_call_672a4d21634d4a24", i32x2, i32, func(m api.Module, stack []uint64) {
		stack[0] = uint64(s.heap.add(nil))
	})
	reg("__wbg_call_7cccdd69e0791ae2", i32x3, i32, func(m api.Module, stack []uint64) {
		stack[0] = uint64(s.heap.add(nil))
	})
	reg("__wbg_newnoargs_105ed471475aaf50", i32x2, i32, func(m api.Module, stack []uint64) {
		stack[0] = uint64(s.heap.add(nil))
	})
	reg("__wbindgen_throw", i32x2, nil, func(m api.Module, stack []uint64) {
		ptr, length := uint32(stack[0]), uint32(stack[1])
		str, _ := readString(m, ptr, length)
		panic("sign wasm threw: " + str)
	})

	_, err := b.Instantiate(ctx)
	return err
}

func readString(m api.Module, ptr, length uint32) (string, bool) {
	buf, ok := m.Memory().Read(ptr, length)
	if !ok {
		return "", false
	}
	return string(buf), true
}

// Sign runs get_sign(flag=true, data) and returns the raw 32 hex digest.
func (s *Signer) Sign(data string) (out string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	defer func() {
		if r := recover(); r != nil {
			out = ""
			err = fmt.Errorf("sign panic: %v", r)
		}
	}()

	ctx := context.Background()
	mod := s.module
	mem := mod.Memory()

	res, err := mod.ExportedFunction(fnStackPointer).Call(ctx, api.EncodeI32(-16))
	if err != nil {
		return "", fmt.Errorf("stack pointer: %w", err)
	}
	retptr := uint32(res[0])

	payload := []byte(data)
	res, err = mod.ExportedFunction(fnMalloc).Call(ctx, api.EncodeI32(int32(len(payload))), api.EncodeI32(1))
	if err != nil {
		return "", fmt.Errorf("malloc: %w", err)
	}
	ptr := uint32(res[0])
	if len(payload) > 0 && !mem.Write(ptr, payload) {
		return "", fmt.Errorf("memory write out of range")
	}

	if _, err = mod.ExportedFunction(fnGetSign).Call(ctx, uint64(retptr), api.EncodeI32(1), uint64(ptr), api.EncodeI32(int32(len(payload)))); err != nil {
		return "", fmt.Errorf("get_sign: %w", err)
	}

	raw, ok := mem.Read(retptr, 8)
	if !ok {
		return "", fmt.Errorf("read retptr out of range")
	}
	rptr := binary.LittleEndian.Uint32(raw[0:4])
	rlen := binary.LittleEndian.Uint32(raw[4:8])

	if rlen > 0 {
		buf, ok := mem.Read(rptr, rlen)
		if !ok {
			return "", fmt.Errorf("read result out of range")
		}
		out = fixDigest(string(buf))
	}

	_, _ = mod.ExportedFunction(fnFree).Call(ctx, uint64(rptr), uint64(rlen), api.EncodeI32(1))
	_, _ = mod.ExportedFunction(fnStackPointer).Call(ctx, api.EncodeI32(16))

	return out, nil
}

// fixDigest matches the DeepSider JS glue byte for byte. The wasm returns the
// 32 char hex digest with the two nibbles at offsets 15 and 16 transposed
// relative to a plain read of the output buffer; the server verifies the
// transposed form, so we normalise to it here.
func fixDigest(s string) string {
	if len(s) != 32 {
		return s
	}
	b := []byte(s)
	b[15], b[16] = b[16], b[15]
	return string(b)
}

type iSignPayload struct {
	Nonce     string `json:"nonce"`
	Timestamp int64  `json:"timestamp"`
	Sign      string `json:"sign"`
}

// Generate builds the base64 encoded i-sign header for the given absolute URL
// plus extra parameters (typically the JSON request body).
func (s *Signer) Generate(rawURL string, extra map[string]interface{}) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("parse url: %w", err)
	}

	obj := map[string]interface{}{}
	for key, values := range u.Query() {
		if len(values) > 0 {
			// Object.fromEntries keeps the last value for duplicated keys.
			obj[key] = values[len(values)-1]
		}
	}
	for key, value := range extra {
		obj[key] = value
	}

	ts := time.Now().UnixMilli()
	nonce := randomString(16)
	obj["timestamp"] = strconv.FormatInt(ts, 10)
	obj["nonce"] = nonce

	canonical := originOf(u) + u.Path + "?" + QSStringify(obj)

	digest, err := s.Sign(canonical)
	if err != nil {
		return "", err
	}
	if digest == "" {
		return "", fmt.Errorf("sign returned empty digest")
	}

	blob, err := json.Marshal(iSignPayload{Nonce: nonce, Timestamp: ts, Sign: digest})
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(blob), nil
}

// Canonical exposes the exact string that would be signed, useful for tests
// and debugging.
func Canonical(rawURL string, extra map[string]interface{}) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	obj := map[string]interface{}{}
	for key, values := range u.Query() {
		if len(values) > 0 {
			obj[key] = values[len(values)-1]
		}
	}
	for key, value := range extra {
		obj[key] = value
	}
	return originOf(u) + u.Path + "?" + QSStringify(obj), nil
}

func originOf(u *url.URL) string {
	return u.Scheme + "://" + u.Host
}

func randomString(n int) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		for i := range buf {
			buf[i] = byte(time.Now().UnixNano() >> (i % 8))
		}
	}
	for i := range buf {
		buf[i] = alphabet[int(buf[i])%len(alphabet)]
	}
	return string(buf)
}
