package sqlite

import "testing"

func TestListConversationsPageAllAccountsCoversEveryRowOnceInRecencyOrder(t *testing.T) {
	store := openRepositoryTestStore(t)
	account := repositoryTestAccount("account-page")
	mustRepositoryWrite(t, "UpsertAccount", store.UpsertAccount(account))
	ids := []string{}
	for i := 0; i < 23; i++ {
		id := string(rune('a'+i)) + "-conv"
		ids = append(ids, id)
		mustRepositoryWrite(t, "UpsertConversation", store.UpsertConversation(Conversation{
			ConversationID: id, AccountID: account.AccountID, RemoteConversationID: "remote-" + id,
			Kind: ConversationKindDirect, Title: id, NotificationMode: NotificationModeAll,
			LastMessageAtMS: int64(100 + i%5), // many ties
			MetadataJSON:    `{}`, CreatedAtMS: repositoryTestTimeMS, UpdatedAtMS: repositoryTestTimeMS,
		}))
	}
	full, err := store.ListConversationsByRecencyAllAccounts(1000)
	if err != nil {
		t.Fatal(err)
	}
	var paged []string
	for offset := 0; ; offset += 7 {
		page, err := store.ListConversationsPageAllAccounts(7, offset)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		for _, c := range page {
			paged = append(paged, c.ConversationID)
		}
	}
	if len(paged) != len(ids) || len(full) != len(ids) {
		t.Fatalf("paged=%d full=%d want %d", len(paged), len(full), len(ids))
	}
	for i, c := range full {
		if paged[i] != c.ConversationID {
			t.Fatalf("row %d = %s, want %s", i, paged[i], c.ConversationID)
		}
	}
}
