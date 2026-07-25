package rtk

import (
	"strings"
	"testing"
)

func TestCompressLS(t *testing.T) {
	body := map[string]any{
		"messages": []any{
			map[string]any{
				"role": "tool",
				"content": "total 124\n" +
					"-rw-r--r-- 1 user user  1234 Jan 12 10:30 main.go\n" +
					"-rw-r--r-- 1 user user   512 Jan 12 10:31 util.go\n" +
					"-rw-r--r-- 1 user user  8192 Jan 12 10:32 handler.go\n" +
					"-rw-r--r-- 1 user user  4096 Jan 12 10:33 server.go\n" +
					"-rwxr-xr-x 1 user user 20480 Jan 12 09:00 binary\n" +
					"drwxr-xr-x 1 user user   128 Jan 12 09:00 node_modules\n" +
					"drwxr-xr-x 1 user user   128 Jan 12 09:00 .git\n" +
					"-rw-r--r-- 1 user user   256 Jan 12 10:34 helper_test.go\n" +
					"-rw-r--r-- 1 user user   128 Jan 12 10:35 types.go\n" +
					"drwxr-xr-x 1 user user   128 Jan 12 09:00 dist\n" +
					"drwxr-xr-x 1 user user   128 Jan 12 09:00 build\n",
			},
		},
	}
	stats := CompressMessages(body)
	if stats == nil {
		t.Fatal("expected compression stats, got nil")
	}
	if stats.BytesBefore <= stats.BytesAfter {
		t.Fatalf("expected reduction, before=%d after=%d", stats.BytesBefore, stats.BytesAfter)
	}
	out := body["messages"].([]any)[0].(map[string]any)["content"].(string)
	if !strings.Contains(out, "Summary:") {
		t.Fatalf("expected summary in compressed output, got: %s", out)
	}
	if strings.Contains(out, "node_modules") {
		t.Fatalf("noise dir node_modules should be skipped, got: %s", out)
	}
	t.Logf("RTK: %s", FormatLog(stats))
}

func TestCompressClaudeToolResult(t *testing.T) {
	huge := strings.Repeat("line of log output that repeats a lot\n", 500)
	body := map[string]any{
		"messages": []any{
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{
						"type": "tool_result",
						"content": huge,
					},
				},
			},
		},
	}
	stats := CompressMessages(body)
	if stats == nil {
		t.Fatal("expected compression stats, got nil")
	}
	if stats.BytesAfter >= stats.BytesBefore {
		t.Fatalf("expected reduction for repetitive content, before=%d after=%d", stats.BytesBefore, stats.BytesAfter)
	}
}

func TestSkipErrorTraces(t *testing.T) {
	body := map[string]any{
		"messages": []any{
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{
						"type":     "tool_result",
						"is_error": true,
						"content":  strings.Repeat("ERROR TRACE MUST BE PRESERVED\n", 200),
					},
				},
			},
		},
	}
	stats := CompressMessages(body)
	if stats != nil {
		t.Fatal("error tool_result must not be compressed")
	}
}

func TestDisabledNeverNil(t *testing.T) {
	// empty / non-tool bodies return nil gracefully
	if CompressMessages(nil) != nil {
		t.Fatal("nil body should return nil")
	}
	if CompressMessages(map[string]any{}) != nil {
		t.Fatal("empty body should return nil")
	}
}
