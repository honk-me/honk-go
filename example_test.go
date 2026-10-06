package honk_test

import (
	"context"
	"errors"
	"log"
	"net/url"
	"time"

	honk "github.com/honk-me/honk-go"
)

func Example() {
	c, err := honk.FromEnv() // HONK_URL, HONK_KEY
	if err != nil {
		log.Fatal(err)
	}
	ctx := context.Background()
	if _, err := c.Problem(ctx, "db/backup", "Backup failed", "pg_dump exited with 1"); err != nil {
		log.Print(err)
	}
}

// A customer request: one group per request, pushed right away, linked to the admin page.
func ExampleClient_Send_customerRequest() {
	c, _ := honk.FromEnv()
	go func() { // never block the request path on a notification
		_, err := c.Send(context.Background(), honk.Message{
			Title:    "New request: online shop quote",
			Message:  "Ana Pop (Acme) asked for a quote: 40 products, delivery in November.",
			Priority: honk.PriorityHigh,
			Category: honk.CategoryCustomers,
			Channel:  "requests",
			GroupKey: "requests/4812",
			URL:      "https://shop.example.com/admin/requests/4812",
			Metadata: map[string]any{"request_id": "4812"},
		}, honk.WithIdempotencyKey("request-4812"))
		if err != nil {
			log.Printf("honk: %v", err)
		}
	}()
}

// Buttons on a customer request: reply by email or call back, straight from the notification.
func ExampleAction() {
	c, _ := honk.FromEnv()
	_, err := c.Send(context.Background(), honk.Message{
		Title:    "New request: online shop quote",
		Message:  "Emily Carter (Acme) asked for a quote: online shop, 40 products",
		Category: honk.CategoryCustomers,
		GroupKey: "requests/4812",
		Actions: []honk.Action{
			{Title: "Reply", URL: "mailto:emily@example.com?subject=" + url.PathEscape("Your quote")},
			{Title: "Call Emily", URL: "tel:+15550134"},
		},
	}, honk.WithIdempotencyKey("request-4812"))
	if err != nil {
		log.Printf("honk: %v", err)
	}
}

func ExampleError() {
	c, _ := honk.FromEnv()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := c.Long(ctx, "Payment failed", "Stripe declined order 1042", honk.WithGroupKey("payments/stripe"))
	var he *honk.Error
	switch {
	case err == nil:
	case errors.Is(err, honk.ErrValidation):
		log.Printf("fix the message: %v", err) // he.Fields lists every invalid field
	case errors.Is(err, honk.ErrQuota) && errors.As(err, &he):
		log.Printf("over quota, retry in %s", he.RetryAfter)
	case errors.As(err, &he) && he.Retryable():
		log.Printf("Honk unreachable; retry later with key %s", he.IdempotencyKey)
	default:
		log.Print(err)
	}
}
