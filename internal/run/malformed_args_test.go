package run

import "testing"

// A malformed tool call used to fail the whole response, ending a long turn
// over one bad call. It now reaches the agent with its raw text, which the
// agent salvages or answers with a "malformed arguments" tool result.
func TestParseOpenAIChatKeepsMalformedToolCalls(t *testing.T) {
	body := []byte(`{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","content":"",
	  "tool_calls":[
	    {"id":"a","type":"function","function":{"name":"update_plan","arguments":"{\"plan\": [{\"step\": \"x\", \"status\": \"pending\"}: 1]}"}},
	    {"id":"b","type":"function","function":{"name":"Read","arguments":"{\"file_path\":\"a.txt\"}"}}
	  ]}}]}`)
	got, err := parseOpenAIChat(body, 200, nil)
	if err != nil {
		t.Fatalf("a malformed call failed the response: %v", err)
	}
	if len(got.ToolCalls) != 2 {
		t.Fatalf("calls = %d, want both", len(got.ToolCalls))
	}
	bad := got.ToolCalls[0]
	if bad.Name != "update_plan" || bad.Args != nil || bad.RawArgs == "" {
		t.Fatalf("malformed call = %+v, want its raw arguments carried", bad)
	}
	if got.ToolCalls[1].Args["file_path"] != "a.txt" {
		t.Fatalf("the well-formed call was not parsed: %+v", got.ToolCalls[1])
	}
}
