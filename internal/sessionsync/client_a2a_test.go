package sessionsync

import (
	"encoding/json"
	"testing"
)

func TestInboxA2ACursorIsOptional(t *testing.T) {
	var in Inbox
	if err := json.Unmarshal([]byte(`{"a2a":[]}`), &in); err != nil || in.A2ACursor != nil {
		t.Fatalf("older server: %v %v", err, in.A2ACursor)
	}
	if err := json.Unmarshal([]byte(`{"a2a":[],"a2aCursor":42}`), &in); err != nil || in.A2ACursor == nil || *in.A2ACursor != 42 {
		t.Fatalf("cursor: %v %v", err, in.A2ACursor)
	}
}
