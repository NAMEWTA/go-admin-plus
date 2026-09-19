package outbox

import "testing"

func TestUniqueJSONMembersRejectsDuplicatesAtEveryDepth(t *testing.T) {
	t.Parallel()
	for _, payload := range [][]byte{
		[]byte(`{"revision":1,"revision":2}`),
		[]byte(`{"outer":{"value":1,"value":2}}`),
		[]byte(`{"items":[{"value":1,"value":2}]}`),
		[]byte(`{"value":1,"\u0076alue":2}`),
	} {
		if uniqueJSONMembers(payload) {
			t.Fatalf("duplicate JSON members accepted: %s", payload)
		}
	}
	if !uniqueJSONMembers([]byte(`{"outer":{"value":1},"items":[{"value":2}]}`)) {
		t.Fatal("unique JSON members were rejected")
	}
}
