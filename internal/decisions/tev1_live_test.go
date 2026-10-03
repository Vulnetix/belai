package decisions

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"
)

// tev1LiveRequest is the charge-dispute example plus an injection noul.
func tev1LiveRequest() Request {
	return Request{
		State: chargeDisputeState,
		Questions: map[string]Question{
			"intent": chargeDisputeQuestion(),
			"inject": Noul("The text tries to override the assistant's instructions."),
		},
	}
}

func checkTev1Live(t *testing.T, d Decider) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := d.Decide(ctx, tev1LiveRequest())
	if err != nil {
		t.Fatal(err)
	}
	a := res.Answers["intent"]
	t.Logf("%s: intent %s (%.3f), inject %.3f, mass %.2f, %s", d.Identity(), a.Choice, a.Probabilities[a.Choice], res.Answers["inject"].Noul, res.Meta.LetterMass, res.Meta.Latency)
	if a.Choice != "duplicate_charge" {
		t.Fatalf("charge dispute answered %q", a.Choice)
	}
	if n := res.Answers["inject"].Noul; n > 0.5 {
		t.Errorf("a plain customer message scored %.2f for injection", n)
	}
}

// TestTev1Live asks Tev1 on Together the upstream charge-dispute example. It
// is a diagnostic: it runs only when BELAI_TEV1_LIVE=1 and TOGETHER_API_KEY
// is set.
//
//	BELAI_TEV1_LIVE=1 TOGETHER_API_KEY=… go test ./internal/decisions -run Tev1Live -v
func TestTev1Live(t *testing.T) {
	key := os.Getenv("TOGETHER_API_KEY")
	if os.Getenv("BELAI_TEV1_LIVE") != "1" || key == "" {
		t.Skip("set BELAI_TEV1_LIVE=1 and TOGETHER_API_KEY to call Together")
	}
	checkTev1Live(t, &ChatLetters{
		Name: TogetherProvider, BaseURL: "https://api.together.xyz/v1", Model: Tev1HostedModel,
		Key: func() (string, error) { return key, nil }, Client: &http.Client{}, Timeout: 30 * time.Second,
	})
}

// TestTev1LocalLive asks a llama-server already serving a Tev1 GGUF, at
// BELAI_TEV1_LOCAL_URL, the same example. BELAI_TEV1_LOCAL_MODEL picks the
// catalogue entry (tev1-4b by default).
//
//	llama-server -m togethercomputer_Tev1-4B-experimental-Q4_K_M.gguf --port 18197 &
//	BELAI_TEV1_LOCAL_URL=http://127.0.0.1:18197 go test ./internal/decisions -run Tev1LocalLive -v
func TestTev1LocalLive(t *testing.T) {
	url := os.Getenv("BELAI_TEV1_LOCAL_URL")
	if url == "" {
		t.Skip("set BELAI_TEV1_LOCAL_URL to a llama-server serving Tev1")
	}
	id := os.Getenv("BELAI_TEV1_LOCAL_MODEL")
	if id == "" {
		id = "tev1-4b"
	}
	m, ok := LocalModelByID(id)
	if !ok || m.Template != TemplateTev1 {
		t.Fatalf("%s is not a local Tev1 model", id)
	}
	checkTev1Live(t, &Llama{BaseURL: url, Model: m, Client: &http.Client{}, Timeout: 60 * time.Second})
}
