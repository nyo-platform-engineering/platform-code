package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"aat/internal/eventbus"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

func consumerTestServer(t *testing.T) (string, func()) {
	t.Helper()
	binary := os.Getenv("AAT_TEST_NATS_SERVER")
	if binary == "" {
		var err error
		binary, err = exec.LookPath("nats-server")
		if err != nil {
			t.Skip("set AAT_TEST_NATS_SERVER or install nats-server for broker integration tests")
		}
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "server.log")
	log, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = log.Close() })
	url := fmt.Sprintf("nats://127.0.0.1:%d", port)
	var cmd *exec.Cmd
	stop := func() {
		if cmd != nil {
			_ = cmd.Process.Signal(os.Interrupt)
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				_ = cmd.Process.Kill()
				<-done
			}
			cmd = nil
		}
	}
	t.Cleanup(stop)
	start := func() {
		cmd = exec.Command(binary, "-js", "-sd", filepath.Join(dir, "data"), "-a", "127.0.0.1", "-p", fmt.Sprint(port))
		cmd.Stdout, cmd.Stderr = log, log
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if nc, err := nats.Connect(url, nats.NoReconnect(), nats.Timeout(100*time.Millisecond)); err == nil {
				nc.Close()
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		content, _ := os.ReadFile(logPath)
		t.Fatalf("NATS did not start: %s", content)
	}
	start()
	return url, func() { stop(); start() }
}

func consumerTestConnect(t *testing.T, url string) (*nats.Conn, jetstream.JetStream) {
	t.Helper()
	nc, err := nats.Connect(url, nats.NoReconnect(), nats.Timeout(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatal(err)
	}
	return nc, js
}

func TestConsumerCrashRecovery(t *testing.T) {
	for _, stage := range []string{"before-effect", "after-effect", "after-ack"} {
		t.Run(stage, func(t *testing.T) {
			url, restart := consumerTestServer(t)
			nc, js := consumerTestConnect(t, url)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			if err := eventbus.EnsureStream(ctx, js); err != nil {
				t.Fatal(err)
			}
			consumer, err := js.CreateOrUpdateConsumer(ctx, eventbus.StreamName, jetstream.ConsumerConfig{
				Durable: "crash-test", FilterSubject: eventbus.Subject, AckPolicy: jetstream.AckExplicitPolicy,
				AckWait: 100 * time.Millisecond, MaxAckPending: 1,
			})
			if err != nil {
				t.Fatal(err)
			}
			effects, err := js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
				Bucket: "CRASH_EFFECTS", Storage: jetstream.FileStorage, Replicas: 1,
			})
			if err != nil {
				t.Fatal(err)
			}
			hazard := Hazard{ID: "haz-one", Source: "PVMBG", Severity: "SIAGA"}
			publish := func(id string) {
				t.Helper()
				payload, _ := json.Marshal(hazard)
				_, err := js.PublishMsg(ctx, &nats.Msg{Subject: eventbus.Subject, Data: payload,
					Header: nats.Header{"Nats-Msg-Id": []string{id}}})
				if err != nil {
					t.Fatal(err)
				}
			}
			publish("warning-1")
			child := exec.Command(os.Args[0], "-test.run=^TestConsumerCrashHelper$")
			child.Env = append(os.Environ(), "AAT_CONSUMER_CRASH_URL="+url, "AAT_CONSUMER_CRASH_STAGE="+stage)
			output, err := child.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 23 {
				t.Fatalf("crash helper: %v\n%s", err, output)
			}
			if stage == "after-effect" {
				nc.Close()
				restart()
				_, js = consumerTestConnect(t, url)
				consumer, err = js.Consumer(ctx, eventbus.StreamName, "crash-test")
				if err != nil {
					t.Fatal(err)
				}
				effects, err = js.KeyValue(ctx, "CRASH_EFFECTS")
				if err != nil {
					t.Fatal(err)
				}
			}
			status, err := effects.Status(ctx)
			if err != nil {
				t.Fatal(err)
			}
			want := uint64(1)
			if stage == "before-effect" {
				want = 0
			}
			if status.Values() != want || status.TTL() != 0 {
				t.Fatalf("after crash: records=%d want=%d ttl=%v", status.Values(), want, status.TTL())
			}
			var projections atomic.Int32
			workerCtx, stop := context.WithCancel(ctx)
			done := make(chan struct{})
			go func() {
				defer close(done)
				consume(workerCtx, "crash-test", consumer, effects, func(Hazard) string {
					projections.Add(1)
					return "field_alert"
				})
			}()
			t.Cleanup(func() { stop(); <-done })
			waitAck := func(sequence uint64) {
				t.Helper()
				deadline := time.Now().Add(5 * time.Second)
				for time.Now().Before(deadline) {
					info, err := consumer.Info(ctx)
					if err == nil && info.AckFloor.Stream >= sequence && info.NumAckPending == 0 {
						return
					}
					time.Sleep(20 * time.Millisecond)
				}
				t.Fatalf("delivery %d was not acknowledged after recovery", sequence)
			}
			waitAck(1)
			hazard.Severity = "AWAS"
			publish("warning-2")
			waitAck(2)
			status, err = effects.Status(ctx)
			if err != nil || status.Values() != 2 {
				t.Fatalf("lost or repeated effects: status=%v error=%v", status, err)
			}
			entry, err := effects.Get(ctx, deliveryKey("warning-1", hazard))
			if err != nil {
				t.Fatal(err)
			}
			var effect Effect
			if err := json.Unmarshal(entry.Value(), &effect); err != nil || effect.Hazard.Severity != "SIAGA" || effect.Kind != "field_alert" {
				t.Fatalf("original effect changed: %+v error=%v", effect, err)
			}
			wantProjections := int32(1)
			if stage == "before-effect" {
				wantProjections++
			}
			if projections.Load() != wantProjections {
				t.Fatalf("reprojected committed effect: calls=%d want=%d", projections.Load(), wantProjections)
			}
		})
	}
}

func TestConsumerCrashHelper(t *testing.T) {
	url := os.Getenv("AAT_CONSUMER_CRASH_URL")
	if url == "" {
		t.Skip("subprocess helper for TestConsumerCrashRecovery")
	}
	_, js := consumerTestConnect(t, url)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	consumer, err := js.Consumer(ctx, eventbus.StreamName, "crash-test")
	if err != nil {
		t.Fatal(err)
	}
	batch, err := consumer.Fetch(1, jetstream.FetchMaxWait(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	for msg := range batch.Messages() {
		stage := os.Getenv("AAT_CONSUMER_CRASH_STAGE")
		if stage == "before-effect" {
			os.Exit(23)
		}
		var hazard Hazard
		if err := json.Unmarshal(msg.Data(), &hazard); err != nil {
			t.Fatal(err)
		}
		effects, err := js.KeyValue(ctx, "CRASH_EFFECTS")
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := recordEffect(ctx, effects, deliveryKey(msg.Headers().Get("Nats-Msg-Id"), hazard), hazard, func(Hazard) string { return "field_alert" }); err != nil {
			t.Fatal(err)
		}
		if stage == "after-ack" {
			if err := msg.DoubleAck(ctx); err != nil {
				t.Fatal(err)
			}
		}
		os.Exit(23)
	}
	t.Fatalf("no message received: %v", batch.Error())
}
