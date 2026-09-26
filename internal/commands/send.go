// Package commands sends management channel commands to cajui-firmware receivers
// (cajui-firmware docs/management-v1.md) and reports their answers.
package commands

import (
	"context"
	"errors"
	"time"

	"github.com/cajui/cajui-central/internal/devicestate"
)

var (
	// ErrUnavailable means the broker connection that carries commands is down.
	ErrUnavailable = errors.New("broker connection unavailable")
)

type Repository interface {
	DeviceState(ctx context.Context, source, device string) (devicestate.State, error)
	InsertCommand(ctx context.Context, source, device string, c devicestate.Command, at time.Time) error
	DeleteCommand(ctx context.Context, id string) error
	Command(ctx context.Context, id string) (devicestate.CommandRecord, error)
}

// Publisher is the broker port; the MQTT consumer implements it.
type Publisher interface {
	PublishCommand(ctx context.Context, source, device string, c devicestate.Command) error
}

// Send validates a command against the device's advertised capabilities, records it and
// publishes it. The record exists before publishing so an immediate answer finds it.
func Send(ctx context.Context, repo Repository, publisher Publisher, source, device, kind, node string, now time.Time) (devicestate.CommandRecord, error) {
	if publisher == nil {
		return devicestate.CommandRecord{}, ErrUnavailable
	}
	state, err := repo.DeviceState(ctx, source, device)
	if err != nil {
		return devicestate.CommandRecord{}, err
	}
	command, err := devicestate.NewCommand(kind, node, state)
	if err != nil {
		return devicestate.CommandRecord{}, err
	}
	if err = repo.InsertCommand(ctx, source, device, command, now); err != nil {
		return devicestate.CommandRecord{}, err
	}
	// Only a command the client never accepted is certainly not sent. After a timeout the
	// client may still deliver it (QoS 1 retries on reconnection), so its record stays and
	// the answer, or the timeout below, settles it.
	if err = publisher.PublishCommand(ctx, source, device, command); errors.Is(err, ErrUnavailable) {
		if deleteErr := repo.DeleteCommand(ctx, command.ID); deleteErr != nil {
			return devicestate.CommandRecord{}, errors.Join(err, deleteErr)
		}
		return devicestate.CommandRecord{}, err
	}
	return repo.Command(ctx, command.ID)
}

// Status returns a command's latest answer; one still unanswered after the timeout is
// reported as not delivered.
func Status(ctx context.Context, repo Repository, id string, now time.Time) (devicestate.CommandRecord, error) {
	record, err := repo.Command(ctx, id)
	if err != nil {
		return record, err
	}
	// A pending accept gets its final answer within the pairing window unless the receiver
	// restarts first; then no answer ever comes.
	if record.Status == "sent" && now.Sub(record.SentAt) >= devicestate.CommandTimeout ||
		record.Status == "pending" && now.Sub(record.UpdatedAt) >= devicestate.PendingTimeout {
		record.Status = "undelivered"
	}
	return record, nil
}
