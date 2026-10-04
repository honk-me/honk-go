// Package honk is the official Go client for Honk (https://honk-me.app), the inbox that turns
// events from apps, scripts, cron jobs and CI into calm, grouped push notifications.
//
//	c, err := honk.FromEnv() // HONK_URL, HONK_KEY
//	if err != nil { log.Fatal(err) }
//	_, err = c.Problem(ctx, "db/backup", "Backup failed", "pg_dump exited with 1")
//
// Every Send carries an Idempotency-Key (a UUIDv7 unless you pass WithIdempotencyKey) that is
// reused on every retry, so a lost response never creates a duplicate. Only network errors,
// 429 and 5xx are retried, with exponential backoff, full jitter and Retry-After, within a
// total deadline. Errors are *Error; match them with errors.Is(err, honk.ErrQuota) and
// friends, or errors.As for the details.
//
// The ingestion key belongs on the server side only. Send from a goroutine or a queue so the
// critical path of your service never waits for a notification.
package honk
