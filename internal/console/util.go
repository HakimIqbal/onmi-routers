package console

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

func nowStr() string {
	return time.Now().Format("2006-01-02T15:04:05.000Z07:00")
}

func jsonUnmarshal(b []byte, v any) error {
	return json.Unmarshal(b, v)
}

func toStr(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return fmt.Sprintf("%v", t)
	case bool:
		return fmt.Sprintf("%t", t)
	default:
		return fmt.Sprintf("%v", t)
	}
}

func joinComma(s []string) string {
	return strings.Join(s, ", ")
}
