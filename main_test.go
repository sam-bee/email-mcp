package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMailTools(t *testing.T) {
	var jmap *httptest.Server
	jmap = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			http.Error(w, "missing test authorization", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/session" {
			json.NewEncoder(w).Encode(map[string]any{
				"apiUrl":          jmap.URL + "/jmap",
				"primaryAccounts": map[string]string{mailCapability: "mailbox"},
				"capabilities":    map[string]any{coreCapability: map[string]int{"maxObjectsInGet": 1}},
			})
			return
		}
		var req struct{ MethodCalls [][]json.RawMessage }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if len(req.MethodCalls) == 1 {
			w.Write([]byte(`{"methodResponses":[["Email/get",{"list":[{"id":"new","textBody":[{"partId":"1","type":"text/plain"}],"htmlBody":[{"partId":"2","type":"text/html"}],"bodyValues":{"1":{"value":"Code 123456"},"2":{"value":"<b>123456</b>"}}}]},"body"]]}`))
			return
		}
		var query struct {
			Anchor       string
			AnchorOffset int
			Filter       struct{ After, Before time.Time }
		}
		if err := json.Unmarshal(req.MethodCalls[0][1], &query); err != nil {
			t.Error(err)
		}
		if query.Filter.Before.Sub(query.Filter.After) != time.Hour {
			t.Errorf("expected a one-hour query window, got %+v", query.Filter)
		}
		if query.Anchor != "" && query.AnchorOffset != 1 {
			t.Errorf("expected pagination to continue after the anchor, got %+v", query)
		}
		switch query.Anchor {
		case "":
			w.Write([]byte(`{"methodResponses":[["Email/query",{"ids":["new"]},"query"],["Email/get",{"list":[{"id":"new","receivedAt":"2026-11-18T12:00:00Z","subject":"New"}]},"summaries"]]}`))
		case "new":
			w.Write([]byte(`{"methodResponses":[["Email/query",{"ids":["old"]},"query"],["Email/get",{"list":[{"id":"old","receivedAt":"2026-11-18T11:59:00Z","subject":"Old"}]},"summaries"]]}`))
		case "old":
			w.Write([]byte(`{"methodResponses":[["Email/query",{"ids":[]},"query"],["Email/get",{"list":[]},"summaries"]]}`))
		default:
			t.Errorf("unexpected pagination anchor %q", query.Anchor)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer jmap.Close()
	client, err := newJMAPClient(jmap.URL+"/session", "test-key")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(newMCPHandler(client))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil)
	session, err := mcpClient.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: server.URL}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "list_recent_messages"})
	var recent recentMessages
	decodeToolResult(t, result, err, &recent)
	if len(recent.Messages) != 2 || recent.Messages[0].ID != "new" || recent.Messages[1].ID != "old" {
		t.Fatalf("unexpected recent messages %+v", recent.Messages)
	}
	result, err = session.CallTool(ctx, &mcp.CallToolParams{
		Name: "get_message_body", Arguments: map[string]any{"id": "new"},
	})
	var body messageBody
	decodeToolResult(t, result, err, &body)
	if len(body.Parts) != 2 || body.Parts[0].Value != "Code 123456" || body.Parts[1].Value != "<b>123456</b>" {
		t.Fatalf("unexpected body %+v", body)
	}
}

func decodeToolResult(t *testing.T, result *mcp.CallToolResult, err error, target any) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("tool failed with result %+v", result)
	}
	data, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, target); err != nil {
		t.Fatal(err)
	}
}

func TestTailnetHostAndOrigin(t *testing.T) {
	handler := newMCPHandler(nil)
	for _, tc := range []struct {
		host   string
		origin string
		status int
	}{
		{"email-mcp.example.ts.net", "https://email-mcp.example.ts.net", http.StatusMethodNotAllowed},
		{"email-mcp.example.ts.net", "https://elsewhere.example.net", http.StatusForbidden},
		{"elsewhere.example.net", "", http.StatusMisdirectedRequest},
	} {
		req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
		req.Host = tc.host
		if tc.origin != "" {
			req.Header.Set("Origin", tc.origin)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		if response.Code != tc.status {
			t.Errorf("host %q origin %q got HTTP %d, want %d", tc.host, tc.origin, response.Code, tc.status)
		}
	}
}
