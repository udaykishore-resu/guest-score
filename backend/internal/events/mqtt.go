package events

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// AckTopic is where the bureau publishes the outcome of every event.
const AckTopic = "guestscore/_bureau/acks"

// Ingest subscribes to property event topics and files what arrives.
type Ingest struct {
	client mqtt.Client
	store  Store
	dedupe Deduper
	log    *slog.Logger
	topic  string

	received, accepted, rejected, duplicates, failed atomic.Uint64
}

// Options configures the ingest.
type Options struct {
	URL      string
	ClientID string
	Topic    string
	Username string
	Password string
	Store    Store
	Deduper  Deduper
	Log      *slog.Logger
}

// New connects to the broker and subscribes.
func New(opts Options) (*Ingest, error) {
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	if opts.Topic == "" {
		opts.Topic = "guestscore/+/events"
	}
	if opts.ClientID == "" {
		opts.ClientID = "guest-score-ingest"
	}
	if opts.Store == nil {
		return nil, fmt.Errorf("events: a store is required")
	}
	if opts.Deduper == nil {
		opts.Deduper = NewMemoryDeduper()
	}

	in := &Ingest{store: opts.Store, dedupe: opts.Deduper, log: opts.Log, topic: opts.Topic}

	co := mqtt.NewClientOptions().
		AddBroker(opts.URL).
		SetClientID(opts.ClientID).
		SetUsername(opts.Username).
		SetPassword(opts.Password).
		// A clean session would discard the broker-side queue of a QoS 1
		// subscription across a reconnect, which is exactly the queue that
		// makes MQTT worth using here: an incident filed while the bureau was
		// restarting must still arrive.
		SetCleanSession(false).
		SetAutoReconnect(true).
		SetConnectRetry(true).
		SetConnectRetryInterval(5 * time.Second).
		SetMaxReconnectInterval(2 * time.Minute).
		SetKeepAlive(30 * time.Second).
		SetConnectTimeout(10 * time.Second).
		// Ordered delivery is the default and is kept: two events for the same
		// stay must be applied in the order the property sent them.
		SetOrderMatters(true)

	co.OnConnect = func(c mqtt.Client) {
		// Subscribing in OnConnect rather than once after connecting is what
		// makes the subscription survive a reconnect. Doing it once is the
		// classic paho bug: the client comes back and silently receives nothing.
		if tok := c.Subscribe(in.topic, 1, in.handle); tok.Wait() && tok.Error() != nil {
			in.log.Error("failed to subscribe", "topic", in.topic, "err", tok.Error())
			return
		}
		in.log.Info("subscribed to property events", "topic", in.topic)
	}
	co.OnConnectionLost = func(_ mqtt.Client, err error) {
		in.log.Warn("mqtt connection lost, reconnecting", "err", err)
	}

	client := mqtt.NewClient(co)
	tok := client.Connect()
	if !tok.WaitTimeout(15 * time.Second) {
		return nil, fmt.Errorf("mqtt %s: connection timed out", opts.URL)
	}
	if err := tok.Error(); err != nil {
		return nil, fmt.Errorf("mqtt %s: %w", opts.URL, err)
	}
	in.client = client
	return in, nil
}

// handle processes one message.
//
// paho acks a QoS 1 message when this returns, so the distinction between a
// permanent rejection and a transient failure is load-bearing: returning
// normally from a transient failure would discard the message. A panic here
// would take the client's routing goroutine down with it, hence the recover.
func (in *Ingest) handle(c mqtt.Client, msg mqtt.Message) {
	in.received.Add(1)

	defer func() {
		if r := recover(); r != nil {
			in.failed.Add(1)
			in.log.Error("panic while handling event", "topic", msg.Topic(), "panic", r)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ack, err := Apply(ctx, in.store, in.dedupe, msg.Payload())
	switch {
	case err == nil:
		if ack.Duplicate {
			in.duplicates.Add(1)
		} else {
			in.accepted.Add(1)
		}
		in.log.Info("event applied",
			"event_id", ack.EventID, "guest_id", ack.GuestID,
			"review_id", ack.ReviewID, "duplicate", ack.Duplicate, "topic", msg.Topic())

	case IsReject(err):
		in.rejected.Add(1)
		// Log at warn, not error: a malformed payload is the publisher's bug,
		// and it is going to happen. Publishing the reason back is what lets
		// them fix it without reading the bureau's logs.
		in.log.Warn("event rejected", "event_id", ack.EventID, "reason", ack.Reason, "topic", msg.Topic())

	default:
		in.failed.Add(1)
		in.log.Error("event could not be applied; it will be redelivered",
			"event_id", ack.EventID, "err", err, "topic", msg.Topic())
		// Deliberately do not publish an ack. The publisher's client keeps the
		// message in flight and retries, which is the behaviour a transient
		// failure needs.
		return
	}

	in.publishAck(c, ack)
}

func (in *Ingest) publishAck(c mqtt.Client, ack Ack) {
	b, err := json.Marshal(ack)
	if err != nil {
		in.log.Error("could not encode ack", "event_id", ack.EventID, "err", err)
		return
	}
	// QoS 1, not retained: an ack is about one message, and retaining it would
	// hand the next subscriber a stale outcome as if it were current.
	c.Publish(AckTopic, 1, false, b)
}

// Stats reports ingest counters for /api/health.
func (in *Ingest) Stats() map[string]uint64 {
	return map[string]uint64{
		"received":   in.received.Load(),
		"accepted":   in.accepted.Load(),
		"duplicates": in.duplicates.Load(),
		"rejected":   in.rejected.Load(),
		"failed":     in.failed.Load(),
	}
}

// Connected reports broker connectivity.
func (in *Ingest) Connected() bool {
	return in.client != nil && in.client.IsConnected()
}

// Close unsubscribes and disconnects, giving in-flight handlers a moment to
// finish so a shutdown does not turn accepted work into a redelivery.
func (in *Ingest) Close() error {
	if in.client == nil {
		return nil
	}
	if tok := in.client.Unsubscribe(in.topic); tok.WaitTimeout(2*time.Second) && tok.Error() != nil {
		in.log.Warn("failed to unsubscribe", "err", tok.Error())
	}
	in.client.Disconnect(2000)
	return nil
}

// Publish sends one event, for the property simulator and for tests.
func Publish(url, clientID, propertyID string, e Event, username, password string) error {
	co := mqtt.NewClientOptions().
		AddBroker(url).
		SetClientID(clientID).
		SetUsername(username).
		SetPassword(password).
		SetConnectTimeout(10*time.Second).
		// The last will marks the property offline if this client vanishes
		// without disconnecting cleanly. Retained, so a subscriber that
		// connects later still learns the property is unreachable — silence
		// and health are otherwise indistinguishable.
		SetWill("guestscore/"+propertyID+"/status", `{"status":"offline"}`, 1, true)

	c := mqtt.NewClient(co)
	tok := c.Connect()
	if !tok.WaitTimeout(15*time.Second) || tok.Error() != nil {
		return fmt.Errorf("connecting to %s: %w", url, tok.Error())
	}
	defer c.Disconnect(500)

	if t := c.Publish("guestscore/"+propertyID+"/status", 1, true, `{"status":"online"}`); t.WaitTimeout(5*time.Second) && t.Error() != nil {
		return t.Error()
	}

	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	t := c.Publish("guestscore/"+propertyID+"/events", 1, false, b)
	if !t.WaitTimeout(10*time.Second) || t.Error() != nil {
		return fmt.Errorf("publishing event: %w", t.Error())
	}
	return nil
}
