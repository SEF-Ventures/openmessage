package tools

import (
	"testing"

	"github.com/mark3labs/mcp-go/server"
)

var mcpWriteTools = []string{
	"send_message", "send_to_conversation", "send_media_to_conversation",
	"send_group_message", "react_to_message", "set_message_transcript",
	"draft_message", "download_media", "import_messages",
}

func registeredToolNames(t *testing.T) map[string]bool {
	t.Helper()
	a := testApp(t)
	s := server.NewMCPServer("readonly-test", "0.1.0")
	Register(s, a)
	names := map[string]bool{}
	for name := range s.ListTools() {
		names[name] = true
	}
	return names
}

func TestMCPReadOnlyOmitsWriteTools(t *testing.T) {
	t.Setenv(EnvMCPReadOnly, "")
	normal := registeredToolNames(t)
	for _, name := range mcpWriteTools {
		if !normal[name] {
			t.Fatalf("default mode must list %s", name)
		}
	}

	t.Setenv(EnvMCPReadOnly, "1")
	ro := registeredToolNames(t)
	for _, name := range mcpWriteTools {
		if ro[name] {
			t.Fatalf("read-only mode must not list %s", name)
		}
	}
	for _, name := range []string{"get_messages", "search_messages", "list_conversations", "get_status"} {
		if !ro[name] {
			t.Fatalf("read-only mode must still list %s", name)
		}
	}
	if len(ro) != len(normal)-len(mcpWriteTools) {
		t.Fatalf("read-only tools = %d, want %d", len(ro), len(normal)-len(mcpWriteTools))
	}
}
