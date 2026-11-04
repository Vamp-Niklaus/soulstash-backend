package email

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// EmailSender defines the interface for sending transactional emails
type EmailSender interface {
	SendEmail(ctx context.Context, recipientEmail, recipientName, subject, htmlContent string) error
}

type brevoClient struct {
	apiKey     string
	httpClient *http.Client
}

// NewBrevoClient creates a new Brevo API client
func NewBrevoClient(apiKey string) EmailSender {
	return &brevoClient{
		apiKey: apiKey,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

type emailRecipient struct {
	Email string `json:"email"`
	Name  string `json:"name"`
}

type emailSenderInfo struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

type brevoPayload struct {
	Sender      emailSenderInfo  `json:"sender"`
	To          []emailRecipient `json:"to"`
	Subject     string           `json:"subject"`
	HTMLContent string           `json:"htmlContent"`
}

func (c *brevoClient) SendEmail(ctx context.Context, recipientEmail, recipientName, subject, htmlContent string) error {
	if c.apiKey == "" {
		return fmt.Errorf("Brevo API key not configured")
	}

	name := recipientName
	if name == "" {
		name = "User"
	}

	payload := brevoPayload{
		Sender: emailSenderInfo{
			Name:  "Soulstash",
			Email: "soulstash.onrender@gmail.com",
		},
		To: []emailRecipient{
			{Email: recipientEmail, Name: name},
		},
		Subject:     subject,
		HTMLContent: htmlContent,
	}

	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.brevo.com/v3/smtp/email", bytes.NewReader(bodyBytes))
	if err != nil {
		return err
	}

	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("api-key", c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("brevo request error: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var errData map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&errData)
		return fmt.Errorf("brevo error (status %d): %v", resp.StatusCode, errData)
	}

	return nil
}
