package sign

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// QSStringify reproduces the subset of the npm "qs" stringify behaviour used
// by DeepSider when it builds the canonical signing string:
//
//	arrayFormat: "indices"  -> key[0]=v0&key[1]=v1
//	allowDots:   true       -> parent.child=value
//	sort:        ascending  -> keys sorted lexicographically at every level
//
// Empty arrays and empty objects contribute nothing. Values are RFC3986
// percent encoded (encodeURIComponent with !'()* escaped as well).
func QSStringify(obj map[string]interface{}) string {
	var parts []string
	for _, key := range sortedKeys(obj) {
		encodeValue(&parts, key, obj[key])
	}
	return strings.Join(parts, "&")
}

func sortedKeys(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func encodeValue(parts *[]string, key string, value interface{}) {
	switch v := value.(type) {
	case nil:
		*parts = append(*parts, encode(key)+"=")
	case string:
		*parts = append(*parts, encode(key)+"="+encode(v))
	case bool:
		*parts = append(*parts, encode(key)+"="+strconv.FormatBool(v))
	case float64:
		*parts = append(*parts, encode(key)+"="+formatNumber(v))
	case float32:
		*parts = append(*parts, encode(key)+"="+formatNumber(float64(v)))
	case int:
		*parts = append(*parts, encode(key)+"="+strconv.Itoa(v))
	case int32:
		*parts = append(*parts, encode(key)+"="+strconv.FormatInt(int64(v), 10))
	case int64:
		*parts = append(*parts, encode(key)+"="+strconv.FormatInt(v, 10))
	case json.Number:
		*parts = append(*parts, encode(key)+"="+v.String())
	case []interface{}:
		for i, item := range v {
			encodeValue(parts, fmt.Sprintf("%s[%d]", key, i), item)
		}
	case []string:
		for i, item := range v {
			encodeValue(parts, fmt.Sprintf("%s[%d]", key, i), item)
		}
	case map[string]interface{}:
		for _, sub := range sortedKeys(v) {
			encodeValue(parts, key+"."+sub, v[sub])
		}
	default:
		*parts = append(*parts, encode(key)+"="+encode(fmt.Sprint(v)))
	}
}

func formatNumber(f float64) string {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return "0"
	}
	if f == math.Trunc(f) && math.Abs(f) < 1e21 {
		return strconv.FormatFloat(f, 'f', -1, 64)
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}

const upperhex = "0123456789ABCDEF"

func encode(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '.' || c == '_' || c == '~' {
			b.WriteByte(c)
		} else {
			b.WriteByte('%')
			b.WriteByte(upperhex[c>>4])
			b.WriteByte(upperhex[c&0x0f])
		}
	}
	return b.String()
}
