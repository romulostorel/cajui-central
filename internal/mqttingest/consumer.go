// Package mqttingest consumes an open telemetry stream, and the device state of
// cajui-firmware receivers, without publishing application receipts.
package mqttingest

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/cajui/cajui-central/internal/devicestate"
	"github.com/cajui/cajui-central/internal/telemetry"
	mqtt "github.com/eclipse/paho.mqtt.golang"
)

const (
	Topic             = "telemetry/v1/+/+/samples"
	StateTopic        = "manage/v1/+/+/state"
	AvailabilityTopic = "manage/v1/+/+/availability"
)

// Topics Central subscribes to, all with QoS 1.
var Topics = map[string]byte{Topic: 1, StateTopic: 1, AvailabilityTopic: 1}

type Repository interface {
	InsertSample(context.Context, telemetry.Sample, time.Time) (bool, error)
	SaveDeviceState(context.Context, devicestate.State, time.Time, bool) error
	SaveAvailability(context.Context, string, string, string, time.Time, bool) error
}
type Config struct {
	URL, ClientID, Username, Password string
	TLS                               *tls.Config
}
type Consumer struct {
	config    Config
	repo      Repository
	logger    *slog.Logger
	connected atomic.Bool
}

func New(config Config, repo Repository, logger *slog.Logger) *Consumer {
	if logger == nil {
		logger = slog.Default()
	}
	return &Consumer{config: config, repo: repo, logger: logger}
}
func (c *Consumer) Connected() bool { return c.connected.Load() }

// Handle rejects retained samples: an old retained sample is not a new arrival. Device
// state and availability are retained by design and are always handled.
func (c *Consumer) Handle(ctx context.Context, topic string, payload []byte, retained bool) (bool, error) {
	if source, device, kind, ok := devicestate.ParseTopic(topic); ok {
		if kind == "availability" {
			value, err := devicestate.DecodeAvailability(payload)
			if err != nil {
				return false, telemetry.ErrInvalid
			}
			return true, c.repo.SaveAvailability(ctx, source, device, value, time.Now(), retained)
		}
		state, err := devicestate.Decode(source, device, payload)
		if err != nil {
			return false, telemetry.ErrInvalid
		}
		return true, c.repo.SaveDeviceState(ctx, state, time.Now(), retained)
	}
	if retained {
		return false, nil
	}
	s, err := telemetry.DecodeSample(payload)
	if err != nil {
		return false, err
	}
	if topic != "telemetry/v1/"+s.SourceID+"/"+s.DeviceID+"/samples" {
		return false, telemetry.ErrInvalid
	}
	return c.repo.InsertSample(ctx, s, time.Now())
}

type message struct {
	topic    string
	payload  []byte
	retained bool
	ack      func()
}

// settled reports whether a handled message may be acknowledged to the broker: stored,
// or permanently rejected. Transient storage failures stay unacknowledged so the
// persistent session redelivers them after the next reconnect.
func settled(err error) bool {
	return err == nil || errors.Is(err, telemetry.ErrInvalid) || errors.Is(err, telemetry.ErrConflict)
}

// Run reconnects until cancellation. The session is persistent: while Central is
// disconnected, the broker keeps its subscription and queues samples, including across
// broker restarts. Each message is acknowledged only after it is stored, so the broker
// never sends more than its in-flight window and nothing acknowledged is ever dropped.
func (c *Consumer) Run(ctx context.Context) {
	inbox := make(chan message, 128)
	options := mqtt.NewClientOptions().AddBroker(c.config.URL).SetClientID(c.config.ClientID).
		SetUsername(c.config.Username).SetPassword(c.config.Password).SetTLSConfig(c.config.TLS).
		SetCleanSession(false).SetAutoAckDisabled(true).SetAutoReconnect(false).SetConnectRetry(false).
		SetConnectTimeout(5 * time.Second).SetWriteTimeout(5 * time.Second).
		SetKeepAlive(30 * time.Second).SetPingTimeout(5 * time.Second)
	lost := make(chan struct{}, 1)
	options.SetConnectionLostHandler(func(_ mqtt.Client, _ error) {
		c.connected.Store(false)
		select {
		case lost <- struct{}{}:
		default:
		}
	})
	handler := func(_ mqtt.Client, m mqtt.Message) {
		_, _, _, managed := devicestate.ParseTopic(m.Topic())
		if (m.Retained() && !managed) || len(m.Payload()) > telemetry.MaxSampleBytes {
			m.Ack() // Never ingested: acknowledge so the broker discards it.
			return
		}
		item := message{m.Topic(), append([]byte(nil), m.Payload()...), m.Retained(), m.Ack}
		select {
		case inbox <- item:
		default:
			// Left unacknowledged: redelivered by the persistent session.
			c.logger.Warn("MQTT ingestion queue full; sample deferred")
		}
	}
	// Samples queued during an absence can arrive right after CONNECT, before Subscribe
	// registers its handler.
	options.SetDefaultPublishHandler(handler)
	client := mqtt.NewClient(options)
	defer func() { c.connected.Store(false); client.Disconnect(250) }()
	retry := time.Second
	for ctx.Err() == nil {
		if err := wait(ctx, client.Connect()); err != nil {
			c.logger.Warn("MQTT connection unavailable")
			if !pause(ctx, retry) {
				return
			}
			retry = min(retry*2, 30*time.Second)
			continue
		}
		subscription := client.SubscribeMultiple(Topics, handler)
		subscriptionError := wait(ctx, subscription)
		if subscriptionError == nil {
			result, ok := subscription.(*mqtt.SubscribeToken)
			for topic := range Topics {
				if !ok || result.Result()[topic] > 1 {
					subscriptionError = errors.New("subscription refused")
				}
			}
		}
		if subscriptionError != nil {
			client.Disconnect(250)
			if !pause(ctx, retry) {
				return
			}
			retry = min(retry*2, 30*time.Second)
			continue
		}
		retry = time.Second
		c.connected.Store(true)
		c.logger.Info("MQTT subscription ready")
	connected:
		for {
			select {
			case <-ctx.Done():
				return
			case <-lost:
				break connected
			case m := <-inbox:
				operation, cancel := context.WithTimeout(ctx, 5*time.Second)
				_, err := c.Handle(operation, m.topic, m.payload, m.retained)
				cancel()
				if settled(err) {
					m.ack()
				}
				switch {
				case err == nil:
				case errors.Is(err, telemetry.ErrInvalid) || errors.Is(err, telemetry.ErrConflict):
					c.logger.Warn("MQTT message rejected", "topic", m.topic)
				case ctx.Err() != nil:
					// Shutdown interrupted it; unacknowledged, it is redelivered on restart.
				default:
					c.logger.Error("MQTT sample storage failed")
				}
			}
		}
		client.Disconnect(250)
		// Packet IDs belong to the lost connection: acknowledging these on a new one could
		// acknowledge a different message. The broker redelivers them instead.
		drain(inbox)
	}
}
func drain(inbox chan message) {
	for {
		select {
		case <-inbox:
		default:
			return
		}
	}
}
func wait(ctx context.Context, t mqtt.Token) error {
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	select {
	case <-timer.C:
		return context.DeadlineExceeded
	case <-ctx.Done():
		return ctx.Err()
	case <-t.Done():
		return t.Error()
	}
}
func pause(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
