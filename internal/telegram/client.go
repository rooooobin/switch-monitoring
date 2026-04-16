package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client wraps the Telegram Bot API.
type Client struct {
	token  string
	proxy  string
	client *http.Client
}

// NewClient creates a new Telegram client.
func NewClient(token, proxy string) (*Client, error) {
	c := &http.Client{Timeout: 35 * time.Second} // slightly higher than the 30s long-poll timeout
	if proxy != "" {
		proxyStr := strings.ReplaceAll(proxy, "localhost", "127.0.0.1")
		proxyURL, err := url.Parse(proxyStr)
		if err != nil {
			return nil, fmt.Errorf("invalid telegram proxy URL: %w", err)
		}
		c.Transport = &http.Transport{
			Proxy: http.ProxyURL(proxyURL),
		}
	}
	return &Client{
		token:  token,
		proxy:  proxy,
		client: c,
	}, nil
}

// SendMessage sends a text message to a specific chat ID.
func (c *Client) SendMessage(ctx context.Context, chatID, text string) error {
	return c.sendMessage(ctx, chatID, text, "")
}

// SendMessageHTML sends a message using Telegram HTML parse mode (<b>, <pre>, etc.).
func (c *Client) SendMessageHTML(ctx context.Context, chatID, html string) error {
	return c.sendMessage(ctx, chatID, html, "HTML")
}

func (c *Client) sendMessage(ctx context.Context, chatID, text, parseMode string) error {
	urlStr := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", c.token)

	payload := map[string]interface{}{
		"chat_id": chatID,
		"text":    text,
	}
	if parseMode != "" {
		payload["parse_mode"] = parseMode
	}

	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", urlStr, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("telegram API returned status: %s, body: %s", resp.Status, string(bodyBytes))
	}
	return nil
}

// Update represents a Telegram update.
type Update struct {
	UpdateID int      `json:"update_id"`
	Message  *Message `json:"message"`
}

// Message represents a Telegram message.
type Message struct {
	MessageID int    `json:"message_id"`
	Text      string `json:"text"`
	Chat      Chat   `json:"chat"`
}

// Chat represents a Telegram chat.
type Chat struct {
	ID int64 `json:"id"`
}

type getUpdatesResponse struct {
	Ok     bool     `json:"ok"`
	Result []Update `json:"result"`
}

// GetUpdates fetches updates using long-polling.
func (c *Client) GetUpdates(ctx context.Context, offset int) ([]Update, error) {
	urlStr := fmt.Sprintf("https://api.telegram.org/bot%s/getUpdates?offset=%d&timeout=30", c.token, offset)

	req, err := http.NewRequestWithContext(ctx, "GET", urlStr, nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("telegram getUpdates API returned status: %s", resp.Status)
	}

	var parsed getUpdatesResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, err
	}

	if !parsed.Ok {
		return nil, fmt.Errorf("telegram getUpdates returned ok=false")
	}

	return parsed.Result, nil
}
