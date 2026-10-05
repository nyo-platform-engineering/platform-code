package eventbus

import (
	"context"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

const (
	StreamName = "HAZARDS_STREAM"
	Subject    = "hazards.created.v1"
)

func EnsureStream(ctx context.Context, js jetstream.JetStream) error {
	_, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:            StreamName,
		Subjects:        []string{Subject},
		Storage:         jetstream.FileStorage,
		Retention:       jetstream.LimitsPolicy,
		MaxAge:          7 * 24 * time.Hour,
		Duplicates:      2 * time.Minute,
		Replicas:        1,
	})
	return err
}
