package web

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"

	"github.com/maxghenis/openmessage/internal/db"
)

func getBody(t *testing.T, url string) []byte {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("%s = %d %s", url, resp.StatusCode, b)
	}
	return b
}

func TestConversationsOffsetPagesCoverEveryRowOnce(t *testing.T) {
	ts := newTestServer(t)
	const n = 23
	for i := 0; i < n; i++ {
		c := &db.Conversation{ConversationID: fmt.Sprintf("c%02d", i), Name: fmt.Sprintf("n%d", i), LastMessageTS: int64(100 + i%4)}
		if i == 0 {
			c.IsFavorite = true
		}
		if err := ts.store.UpsertConversation(c); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]int{}
	var order []string
	for offset := 0; offset < n+7; offset += 7 {
		var page []db.Conversation
		if err := json.Unmarshal(getBody(t, fmt.Sprintf("%s/api/conversations?limit=7&offset=%d", ts.server.URL, offset)), &page); err != nil {
			t.Fatal(err)
		}
		for _, c := range page {
			seen[c.ConversationID]++
			order = append(order, c.ConversationID)
		}
	}
	if len(seen) != n || len(order) != n {
		t.Fatalf("seen=%d rows=%d, want %d each", len(seen), len(order), n)
	}
	for id, count := range seen {
		if count != 1 {
			t.Fatalf("%s seen %d times", id, count)
		}
	}
	// Negative offsets clamp to 0.
	if string(getBody(t, ts.server.URL+"/api/conversations?limit=7&offset=-5")) != string(getBody(t, ts.server.URL+"/api/conversations?limit=7&offset=0")) {
		t.Fatal("negative offset must behave as 0")
	}
}

func TestConversationsDefaultResponseUnchangedWithoutOffset(t *testing.T) {
	ts := newTestServer(t)
	for i := 0; i < 6; i++ {
		c := &db.Conversation{ConversationID: fmt.Sprintf("c%d", i), Name: "n", LastMessageTS: int64(100 + i)}
		c.IsFavorite = i == 0 // oldest, outside limit=3, still appended by legacy semantics
		if err := ts.store.UpsertConversation(c); err != nil {
			t.Fatal(err)
		}
	}
	legacy, err := ts.store.ListConversations(3)
	if err != nil {
		t.Fatal(err)
	}
	var got []db.Conversation
	if err := json.Unmarshal(getBody(t, ts.server.URL+"/api/conversations?limit=3"), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != len(legacy) || len(got) != 4 {
		t.Fatalf("default path rows = %d, legacy = %d, want 4 (3 recent + favorite)", len(got), len(legacy))
	}
	for i := range got {
		if got[i].ConversationID != legacy[i].ConversationID {
			t.Fatalf("row %d = %s, want %s", i, got[i].ConversationID, legacy[i].ConversationID)
		}
	}
}
