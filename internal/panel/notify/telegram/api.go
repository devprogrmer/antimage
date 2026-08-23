package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// API is the slice of Telegram's Bot API this package uses.
//
// An interface, so the bot is testable without a network or a bot token. The
// real implementation is HTTPAPI; tests pass a fake that returns scripted
// updates and records replies.
type API interface {
	// GetUpdates long-polls. offset acknowledges everything below it, which is
	// how Telegram knows an update was processed.
	GetUpdates(ctx context.Context, offset int64, timeout time.Duration) ([]Update, error)
	// SendMessage delivers a reply.
	SendMessage(ctx context.Context, chatID int64, text string) error
}

// Update is one incoming event. Only the fields this bot acts on are decoded;
// Telegram sends many more and adds new ones regularly.
type Update struct {
	UpdateID int64    `json:"update_id"`
	Message  *Message `json:"message"`
}

type Message struct {
	MessageID int64  `json:"message_id"`
	From      *User  `json:"from"`
	Chat      *Chat  `json:"chat"`
	Text      string `json:"text"`
}

// User is the sender.
//
// ID is set by Telegram's servers, not by the client, so it cannot be forged
// inside a genuine update. The forgeable surface is the update itself, which
// is why webhook mode needs a secret token and why polling -- which pulls from
// Telegram rather than accepting pushes -- avoids the question entirely.
type User struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	IsBot    bool   `json:"is_bot"`
}

type Chat struct {
	ID int64 `json:"id"`
	// Type is "private", "group", "supergroup" or "channel". The bot serves
	// only "private": in a group, every member would inherit the linked
	// admin's entire tenant scope.
	Type string `json:"type"`
}

// HTTPAPI talks to api.telegram.org.
//
// Written by hand rather than pulling in a bot framework. The surface used
// here is two endpoints; a dependency would bring a few thousand lines, its
// own update-loop opinions, and a supply-chain surface, to save about eighty
// lines of JSON decoding.
type HTTPAPI struct {
	token  string
	client *http.Client
	base   string
}

func NewHTTPAPI(token string) *HTTPAPI {
	return &HTTPAPI{
		token: token,
		base:  "https://api.telegram.org",
		// No global timeout: GetUpdates long-polls and would be cut off
		// mid-poll. Each call bounds itself through the context instead.
		client: &http.Client{},
	}
}

// method builds an endpoint URL.
//
// The token is a credential and appears in the path, so this value must never
// be logged. Errors from this package deliberately report the METHOD name
// rather than the URL for that reason.
func (a *HTTPAPI) method(name string) string {
	return fmt.Sprintf("%s/bot%s/%s", a.base, a.token, name)
}

type apiResponse struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	Description string          `json:"description"`
	ErrorCode   int             `json:"error_code"`
}

func (a *HTTPAPI) call(ctx context.Context, name string, body any, out any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encode %s request: %w", name, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		a.method(name), bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("build %s request: %w", name, err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.client.Do(req)
	if err != nil {
		// url.Error embeds the request URL, which contains the token. Replace
		// it rather than wrapping, or the bot logs its own credential on every
		// network blip.
		var uerr *url.Error
		if errors.As(err, &uerr) {
			return fmt.Errorf("telegram %s: %w", name, uerr.Err)
		}
		return fmt.Errorf("telegram %s failed", name)
	}
	defer func() { _ = resp.Body.Close() }()

	var decoded apiResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return fmt.Errorf("decode %s response: %w", name, err)
	}
	if !decoded.OK {
		return fmt.Errorf("telegram %s: %s (code %d)",
			name, decoded.Description, decoded.ErrorCode)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(decoded.Result, out)
}

func (a *HTTPAPI) GetUpdates(
	ctx context.Context, offset int64, timeout time.Duration,
) ([]Update, error) {
	// The HTTP call must outlive the long poll, or every poll is cancelled at
	// the moment Telegram would have answered.
	ctx, cancel := context.WithTimeout(ctx, timeout+10*time.Second)
	defer cancel()

	var updates []Update
	err := a.call(ctx, "getUpdates", map[string]any{
		"offset":  offset,
		"timeout": int(timeout.Seconds()),
		// Only message updates are handled; asking for the rest wastes
		// bandwidth and invites handling something we have not thought about.
		"allowed_updates": []string{"message"},
	}, &updates)
	return updates, err
}

func (a *HTTPAPI) SendMessage(ctx context.Context, chatID int64, text string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return a.call(ctx, "sendMessage", map[string]any{
		"chat_id": chatID,
		"text":    text,
		// Plain text. Markdown would require escaping every user-supplied name
		// and a missed escape turns a customer name into broken formatting or
		// an injected link.
		"disable_web_page_preview": true,
	}, nil)
}
