package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/mail"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const (
	coreCapability = "urn:ietf:params:jmap:core"
	mailCapability = "urn:ietf:params:jmap:mail"
	maxJMAPReply   = 8 << 20
	pageSize       = 100
	maxBodyBytes   = 256 << 10
)

var jmapID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,255}$`)

type jmapClient struct {
	discovery *url.URL
	token     string
	http      *http.Client
}

type mailSession struct {
	APIURL          string            `json:"apiUrl"`
	PrimaryAccounts map[string]string `json:"primaryAccounts"`
	Capabilities    struct {
		Core struct {
			MaxObjectsInGet int `json:"maxObjectsInGet"`
		} `json:"urn:ietf:params:jmap:core"`
	} `json:"capabilities"`
}

type emailAddress struct {
	Name  string `json:"name,omitempty"`
	Email string `json:"email"`
}

type messageSummary struct {
	ID         string         `json:"id"`
	ReceivedAt time.Time      `json:"receivedAt"`
	From       []emailAddress `json:"from,omitempty"`
	To         []emailAddress `json:"to,omitempty"`
	Cc         []emailAddress `json:"cc,omitempty"`
	Subject    string         `json:"subject"`
}

type recentMessages struct {
	Since    time.Time        `json:"since"`
	Messages []messageSummary `json:"messages"`
}

type addressedMessages struct {
	Address  string           `json:"address"`
	Messages []messageSummary `json:"messages"`
}

type bodyPart struct {
	ContentType       string `json:"contentType"`
	Value             string `json:"value"`
	IsTruncated       bool   `json:"isTruncated,omitempty"`
	IsEncodingProblem bool   `json:"isEncodingProblem,omitempty"`
}

type messageBody struct {
	ID    string     `json:"id"`
	Parts []bodyPart `json:"parts"`
}

type jmapCall [3]any

func newJMAPClient(rawURL, token string) (*jmapClient, error) {
	discovery, err := url.Parse(rawURL)
	if err != nil || discovery.Hostname() == "" || discovery.User != nil ||
		(discovery.Scheme != "http" && discovery.Scheme != "https") {
		return nil, errors.New("JMAP_SESSION_URL must be an absolute HTTP or HTTPS URL")
	}
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("JMAP_TOKEN is required")
	}
	client := &jmapClient{discovery: discovery, token: token}
	client.http = &http.Client{
		Timeout: 8 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 4 || !client.sameOrigin(req.URL) {
				return errors.New("unexpected JMAP redirect")
			}
			// Only the discovery GET may redirect.
			if via[0].Method != http.MethodGet {
				return errors.New("unexpected JMAP API redirect")
			}
			return nil
		},
	}
	return client, nil
}

func (c *jmapClient) sameOrigin(u *url.URL) bool {
	return u.Scheme == c.discovery.Scheme && strings.EqualFold(u.Host, c.discovery.Host) && u.User == nil
}

func (c *jmapClient) request(ctx context.Context, method, endpoint string, body []byte, result any) error {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return errors.New("invalid JMAP request URL")
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("JMAP request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("JMAP returned HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxJMAPReply+1))
	if err != nil {
		return fmt.Errorf("read JMAP response: %w", err)
	}
	if len(data) > maxJMAPReply {
		return errors.New("JMAP response exceeded 8 MiB")
	}
	if err := json.Unmarshal(data, result); err != nil {
		return fmt.Errorf("decode JMAP response: %w", err)
	}
	return nil
}

func (c *jmapClient) session(ctx context.Context) (mailSession, error) {
	var s mailSession
	if err := c.request(ctx, http.MethodGet, c.discovery.String(), nil, &s); err != nil {
		return s, err
	}
	api, err := url.Parse(s.APIURL)
	if err != nil || !c.sameOrigin(api) {
		return s, errors.New("JMAP advertised an API URL outside the configured origin")
	}
	if s.PrimaryAccounts[mailCapability] == "" {
		return s, errors.New("JMAP key has no primary mail account")
	}
	return s, nil
}

func (c *jmapClient) call(ctx context.Context, endpoint string, calls ...jmapCall) ([]json.RawMessage, error) {
	body, err := json.Marshal(struct {
		Using       []string   `json:"using"`
		MethodCalls []jmapCall `json:"methodCalls"`
	}{[]string{coreCapability, mailCapability}, calls})
	if err != nil {
		return nil, err
	}
	var response struct {
		MethodResponses [][]json.RawMessage `json:"methodResponses"`
	}
	if err := c.request(ctx, http.MethodPost, endpoint, body, &response); err != nil {
		return nil, err
	}
	if len(response.MethodResponses) != len(calls) {
		return nil, errors.New("JMAP returned an unexpected number of method responses")
	}
	results := make([]json.RawMessage, len(calls))
	for i, triple := range response.MethodResponses {
		var name, id string
		if len(triple) != 3 || json.Unmarshal(triple[0], &name) != nil ||
			json.Unmarshal(triple[2], &id) != nil || id != calls[i][2] {
			return nil, errors.New("JMAP returned an invalid method response")
		}
		if name == "error" {
			var failure struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal(triple[1], &failure); err != nil || failure.Type == "" {
				return nil, fmt.Errorf("JMAP %s failed", calls[i][0])
			}
			return nil, fmt.Errorf("JMAP %s failed: %s", calls[i][0], failure.Type)
		}
		if name != calls[i][0] {
			return nil, errors.New("JMAP returned an unexpected method response")
		}
		results[i] = triple[1]
	}
	return results, nil
}

func (c *jmapClient) recent(ctx context.Context, now time.Time) (recentMessages, error) {
	// JMAP timestamps have second precision and the upper bound is exclusive.
	until := now.UTC().Truncate(time.Second).Add(time.Second)
	since := until.Add(-time.Hour)
	messages, err := c.listMessages(ctx, map[string]any{"after": since, "before": until}, nil, 0)
	return recentMessages{Since: since, Messages: messages}, err
}

func (c *jmapClient) toAddress(ctx context.Context, address string, all bool) (addressedMessages, error) {
	recipient, err := mail.ParseAddress(strings.TrimSpace(address))
	if err != nil {
		return addressedMessages{}, errors.New("provide one valid recipient email address")
	}
	limit := 5
	if all {
		limit = 0
	}
	// Compare complete addresses in the parsed To and Cc headers.
	matches := func(message messageSummary) bool {
		for _, recipients := range [][]emailAddress{message.To, message.Cc} {
			for _, entry := range recipients {
				if strings.EqualFold(entry.Email, recipient.Address) {
					return true
				}
			}
		}
		return false
	}
	messages, err := c.listMessages(ctx, nil, matches, limit)
	return addressedMessages{Address: recipient.Address, Messages: messages}, err
}

func (c *jmapClient) listMessages(ctx context.Context, filter map[string]any, matches func(messageSummary) bool, maxResults int) ([]messageSummary, error) {
	result := []messageSummary{}
	session, err := c.session(ctx)
	if err != nil {
		return result, err
	}
	limit := pageSize
	if n := session.Capabilities.Core.MaxObjectsInGet; n > 0 && n < limit {
		limit = n
	}
	account := session.PrimaryAccounts[mailCapability]
	query := map[string]any{
		"accountId": account,
		"sort":      []map[string]any{{"property": "receivedAt", "isAscending": false}},
		"limit":     limit,
	}
	if filter != nil {
		query["filter"] = filter
	}
	get := map[string]any{
		"accountId":  account,
		"properties": []string{"id", "receivedAt", "from", "to", "cc", "subject"},
		"#ids":       map[string]string{"resultOf": "query", "name": "Email/query", "path": "/ids"},
	}
	var anchor string
	for {
		values, err := c.call(ctx, session.APIURL,
			jmapCall{"Email/query", query, "query"},
			jmapCall{"Email/get", get, "summaries"},
		)
		if err != nil {
			return result, err
		}
		var found struct {
			IDs []string `json:"ids"`
		}
		var messages struct {
			List []messageSummary `json:"list"`
		}
		if err := json.Unmarshal(values[0], &found); err != nil {
			return result, fmt.Errorf("decode JMAP message IDs: %w", err)
		}
		if err := json.Unmarshal(values[1], &messages); err != nil {
			return result, fmt.Errorf("decode JMAP message summaries: %w", err)
		}
		byID := make(map[string]messageSummary, len(messages.List))
		for _, message := range messages.List {
			byID[message.ID] = message
		}
		for _, id := range found.IDs {
			if message, ok := byID[id]; ok && (matches == nil || matches(message)) {
				result = append(result, message)
				if maxResults > 0 && len(result) == maxResults {
					return result, nil
				}
			}
		}
		if len(found.IDs) == 0 {
			return result, nil
		}
		next := found.IDs[len(found.IDs)-1]
		if next == anchor {
			return result, errors.New("JMAP pagination did not advance")
		}
		anchor = next
		query["anchor"] = anchor
		query["anchorOffset"] = 1
	}
}

type jmapPart struct {
	PartID string `json:"partId"`
	Type   string `json:"type"`
}

type jmapBodyValue struct {
	Value             string `json:"value"`
	IsTruncated       bool   `json:"isTruncated"`
	IsEncodingProblem bool   `json:"isEncodingProblem"`
}

func (c *jmapClient) body(ctx context.Context, id string) (messageBody, error) {
	result := messageBody{ID: id, Parts: []bodyPart{}}
	if !jmapID.MatchString(id) {
		return result, errors.New("provide a valid message ID from a message listing tool")
	}
	session, err := c.session(ctx)
	if err != nil {
		return result, err
	}
	values, err := c.call(ctx, session.APIURL, jmapCall{"Email/get", map[string]any{
		"accountId":           session.PrimaryAccounts[mailCapability],
		"ids":                 []string{id},
		"properties":          []string{"id", "textBody", "htmlBody", "bodyValues"},
		"bodyProperties":      []string{"partId", "type"},
		"fetchTextBodyValues": true,
		"fetchHTMLBodyValues": true,
		"maxBodyValueBytes":   maxBodyBytes,
	}, "body"})
	if err != nil {
		return result, err
	}
	var response struct {
		List []struct {
			ID         string                   `json:"id"`
			TextBody   []jmapPart               `json:"textBody"`
			HTMLBody   []jmapPart               `json:"htmlBody"`
			BodyValues map[string]jmapBodyValue `json:"bodyValues"`
		} `json:"list"`
	}
	if err := json.Unmarshal(values[0], &response); err != nil {
		return result, fmt.Errorf("decode JMAP message body: %w", err)
	}
	if len(response.List) == 0 {
		return result, errors.New("message was not found in the agent mailbox")
	}
	message := response.List[0]
	if message.ID != id {
		return result, errors.New("JMAP returned a different message")
	}
	for _, part := range append(message.TextBody, message.HTMLBody...) {
		if value, ok := message.BodyValues[part.PartID]; ok {
			result.Parts = append(result.Parts, bodyPart{
				ContentType:       part.Type,
				Value:             value.Value,
				IsTruncated:       value.IsTruncated,
				IsEncodingProblem: value.IsEncodingProblem,
			})
			delete(message.BodyValues, part.PartID)
		}
	}
	return result, nil
}
