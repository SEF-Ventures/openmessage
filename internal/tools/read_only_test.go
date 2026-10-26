package tools

import (
	"testing"

	"github.com/mark3labs/mcp-go/server"
)

var writeTools = []string{
	"send_message",
	"send_to_conversation",
	"send_media_to_conversation",
	"send_group_message",
	"react_to_message",
	"import_messages",
	"set_message_transcript",
}

func TestReadOnlyOptionRegistersNoSendTools(t *testing.T) {
	a := testApp(t)
	s := server.NewMCPServer("read-only", "test")
	RegisterWithOptions(s, a, Options{Reads: a.Store, ReadOnly: true})
	assertReadOnlyTools(t, s)
}

func TestReadOnlyEnvRegistersNoSendTools(t *testing.T) {
	t.Setenv("OPENMESSAGES_MCP_READ_ONLY", "1")
	a := testApp(t)
	s := server.NewMCPServer("read-only-env", "test")
	RegisterWithOptions(s, a, Options{Reads: a.Store})
	assertReadOnlyTools(t, s)
}

func TestDefaultStillRegistersSendTools(t *testing.T) {
	a := testApp(t)
	s := server.NewMCPServer("default", "test")
	RegisterWithOptions(s, a, Options{Reads: a.Store})
	if s.GetTool("send_message") == nil {
		t.Fatal("default registration lost send_message")
	}
}

func assertReadOnlyTools(t *testing.T, s *server.MCPServer) {
	t.Helper()
	for _, name := range writeTools {
		if s.GetTool(name) != nil {
			t.Errorf("read-only registration exposes %q", name)
		}
	}
	for _, name := range []string{"get_messages", "get_conversation", "search_messages", "list_conversations", "draft_message", "get_status"} {
		if s.GetTool(name) == nil {
			t.Errorf("read-only registration is missing %q", name)
		}
	}
}
