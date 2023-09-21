// Copyright 2020 The NATS Authors
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cli

import (
	"bytes"
	"context"
	"fmt"
	"github.com/nats-io/nats.go/jetstream"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/choria-io/fisk"
	"github.com/dustin/go-humanize"
	"github.com/gosuri/uiprogress"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/bench"
)

type benchCmd struct {
	subject              string
	numPubs              int
	numSubs              int
	numMsg               int
	msgSizeString        string
	msgSize              int
	csvFile              string
	noProgress           bool
	request              bool
	reply                bool
	syncPub              bool
	pubBatch             int
	jsTimeout            time.Duration
	js                   bool
	storage              string
	streamName           string
	streamMaxBytesString string
	streamMaxBytes       int64
	pull                 bool
	push                 bool
	consumerBatch        int
	replicas             int
	purge                bool
	subSleep             time.Duration
	pubSleep             time.Duration
	consumerName         string
	kv                   bool
	bucketName           string
	history              uint8
	fetchTimeout         bool
	multiSubject         bool
	multiSubjectMax      int
	deDuplication        bool
	deDuplicationWindow  time.Duration
	retries              int
	retriesUsed          bool
	newJSAPI             bool
	ack                  bool
}

const (
	DefaultDurableConsumerName string = "natscli-bench"
	DefaultStreamName          string = "benchstream"
	DefaultBucketName          string = "benchbucket"
)

func configureBenchCommand(app commandHost) {
	c := &benchCmd{}

	benchHelp := `
Core NATS publish and subscribe:

  nats bench benchsubject --pub 1 --sub 10

Request reply with queue group:

  nats bench benchsubject --sub 1 --reply

  nats bench benchsubject --pub 10 --request

JetStream publish:

  nats bench benchsubject --js --purge --pub 1

JetStream ordered ephemeral consumers:

  nats bench benchsubject --js --sub 10

JetStream durable pull and push consumers:

  nats bench benchsubject --js --sub 5 --pull

  nats bench benchsubject --js --sub 5 --push

JetStream KV put and get:

  nats bench benchsubject --kv --pub 1

  nats bench benchsubject --kv --sub 10

Remember to use --no-progress to measure performance more accurately
`
	bench := app.Command("bench", "Benchmark utility").Action(c.bench)
	if !opts.NoCheats {
		bench.CheatFile(fs, "bench", "cheats/bench.md")
	}
	bench.HelpLong(benchHelp)
	bench.Arg("subject", "Subject to use for the benchmark").Required().StringVar(&c.subject)
	bench.Flag("pub", "Number of concurrent publishers").Default("0").IntVar(&c.numPubs)
	bench.Flag("sub", "Number of concurrent subscribers").Default("0").IntVar(&c.numSubs)
	bench.Flag("js", "Use JetStream").UnNegatableBoolVar(&c.js)
	bench.Flag("request", "Request-Reply mode: publishers send requests waits for a reply").UnNegatableBoolVar(&c.request)
	bench.Flag("reply", "Request-Reply mode: subscribers send replies").UnNegatableBoolVar(&c.reply)
	bench.Flag("kv", "KV mode, subscribers get from the bucket and publishers put in the bucket").UnNegatableBoolVar(&c.kv)
	bench.Flag("msgs", "Number of messages to publish").Default("100000").IntVar(&c.numMsg)
	bench.Flag("size", "Size of the test messages").Default("128").StringVar(&c.msgSizeString)
	bench.Flag("no-progress", "Disable progress bar while publishing").UnNegatableBoolVar(&c.noProgress)
	bench.Flag("csv", "Save benchmark data to CSV file").StringVar(&c.csvFile)
	bench.Flag("purge", "Purge the stream before running").UnNegatableBoolVar(&c.purge)
	bench.Flag("storage", "JetStream storage (memory/file) for the \"benchstream\" stream").Default("file").EnumVar(&c.storage, "memory", "file")
	bench.Flag("replicas", "Number of stream replicas for the \"benchstream\" stream").Default("1").IntVar(&c.replicas)
	bench.Flag("maxbytes", "The maximum size of the stream or KV bucket in bytes").Default("1GB").StringVar(&c.streamMaxBytesString)
	bench.Flag("stream", "When set to something else than \"benchstream\": use (and do not attempt to define) the specified stream when creating durable subscribers. Otherwise define and use the \"benchstream\" stream").Default(DefaultStreamName).StringVar(&c.streamName)
	bench.Flag("bucket", "When set to something else than \"benchbucket\": use (and do not attempt to define) the specified bucket when in KV mode. Otherwise define and use the \"benchbucket\" bucket").Default(DefaultBucketName).StringVar(&c.bucketName)
	bench.Flag("consumer", "Specify the durable consumer name to use").Default(DefaultDurableConsumerName).StringVar(&c.consumerName)
	bench.Flag("jstimeout", "Timeout for JS operations").Default("30s").DurationVar(&c.jsTimeout)
	bench.Flag("syncpub", "Synchronously publish to the stream").UnNegatableBoolVar(&c.syncPub)
	bench.Flag("pubbatch", "Sets the batch size for JS asynchronous publishing").Default("100").IntVar(&c.pubBatch)
	bench.Flag("newjsapi", "Uses the new JetStream API, unlike with the legacy API there is no concept of push or pull consumers but the arguments are re-used to inform the choice of the subscribers consuming either by registering a callback (--push mode) or by fetching batches of messages (--pull mode) from the (shared durable) consumer (setting neither means each subscriber uses it own ephemeral consumer)").Default("true").BoolVar(&c.newJSAPI)
	bench.Flag("pull", "All the subscribers fetch and consume from a shared durable consumer in batches rather than each subscriber consuming from its own ephemeral consumer").UnNegatableBoolVar(&c.pull)
	bench.Flag("push", "All the subscribers register a callback and consume on a shared durable consumer rather than each subscriber consuming from its own ephemeral consumer").UnNegatableBoolVar(&c.push)
	bench.Flag("consumerbatch", "Sets the batch size for the JS durable pull consumer, or the max ack pending value for the JS durable push consumer").Default("100").IntVar(&c.consumerBatch)
	bench.Flag("pullbatch", "Sets the batch fetch size for the consumer (or the max ack pending value for the legacy JS durable push consumer)").Hidden().Default("100").IntVar(&c.consumerBatch)
	bench.Flag("subsleep", "Sleep for the specified interval before sending the subscriber acknowledgement back in --js mode, or sending the reply back in --reply mode,  or doing the next get in --kv mode").Default("0s").DurationVar(&c.subSleep)
	bench.Flag("pubsleep", "Sleep for the specified interval after publishing each message").Default("0s").DurationVar(&c.pubSleep)
	bench.Flag("history", "History depth for the bucket in KV mode").Default("1").Uint8Var(&c.history)
	bench.Flag("multisubject", "Multi-subject mode, each message is published on a subject that includes the publisher's message sequence number as a token").UnNegatableBoolVar(&c.multiSubject)
	bench.Flag("multisubjectmax", "The maximum number of subjects to use in multi-subject mode (0 means no max)").Default("100000").IntVar(&c.multiSubjectMax)
	bench.Flag("retries", "The maximum number of retries in JS operations").Default("3").IntVar(&c.retries)
	bench.Flag("dedup", "Sets a message id in the header to use JS Publish de-duplication").Default("false").UnNegatableBoolVar(&c.deDuplication)
	bench.Flag("dedupwindow", "Sets the duration of the stream's deduplication functionality").Default("2m").DurationVar(&c.deDuplicationWindow)
	bench.Flag("ack", "Uses explicit message acknowledgement").Default("true").BoolVar(&c.ack)
}

func init() {
	registerCommand("bench", 2, configureBenchCommand)
}

func (c *benchCmd) bench(_ *fisk.ParseContext) error {
	// first check the sanity of the arguments
	if c.numMsg <= 0 {
		return fmt.Errorf("number of messages should be greater than 0")
	}

	msgSize, err := parseStringAsBytes(c.msgSizeString)

	if err != nil || msgSize <= 0 {
		log.Fatal("Can not parse or invalid the value specified for the message size: %s", c.msgSizeString)
	}

	c.msgSize = int(msgSize)

	if c.js && c.numSubs > 0 && c.pull {
		log.Print("JetStream durable pull (fetch) consumer mode")
	}

	if c.js && c.numSubs > 0 && c.push {
		if c.pull {
			log.Fatal("The durable consumer must be either pull or push, it can not be both")
		}
		log.Print("JetStream durable push (callback) consumer mode")
	}

	if c.js && c.numSubs > 0 && !c.pull && !c.push {
		if c.newJSAPI {
			log.Print("Simplified JetStream API ephemeral ordered unacked callback mode")
		} else {
			log.Print("Pre-simplification JetStream ephemeral ordered unacked push mode")
		}
	}

	if c.numPubs == 0 && c.numSubs == 0 {
		log.Fatal("You must have at least one publisher or at least one subscriber... try adding --pub 1 and/or --sub 1 to the arguments")
	}

	if (c.request || c.reply) && c.js {
		log.Fatal("Request-reply mode is not applicable to JetStream benchmarking")
	} else if !c.js && !c.kv {
		if c.request || c.reply {
			log.Print("Benchmark in request-reply mode")
			if c.reply {
				// reply mode is open-ended for the number of messages, so don't show the progress bar
				c.noProgress = true
			}
		}

		if c.request && c.reply {
			log.Fatal("Request-reply mode error: can not be both a requester and a replier at the same time, please use at least two instances of nats bench to benchmark request/reply")
		}

		if c.reply && c.numPubs > 0 && c.numSubs > 0 {
			log.Fatal("Request-reply mode error: can not have a publisher while in --reply mode")
		}
	} else if c.kv {
		if c.js {
			log.Fatal("Can not operate in both --js and --kv mode at the same time")
		}

		if c.newJSAPI {
			log.Print("KV mode (new simplified JS API), using the subject name as the KV bucket name. Publishers do puts, subscribers do gets")
		} else {
			log.Print("KV mode, (pre-simplification JS API) using the subject name as the KV bucket name. Publishers do puts, subscribers do gets")
		}

	}

	if c.js || c.kv {
		size, err := parseStringAsBytes(c.streamMaxBytesString)

		if err != nil || size <= 0 {
			log.Fatalf("Can not parse or invalid the value specified for the max stream/bucket size: %s", c.streamMaxBytesString)
		}

		c.streamMaxBytes = size
	}

	// Create the banner which includes the appropriate argument names and values for the type of benchmark being run
	var benchType string

	type nvp struct {
		name  string
		value string
	}
	var argnvps []nvp

	if c.kv {
		benchType = "KV"
		argnvps = append(argnvps, nvp{"bucket", c.bucketName})
	} else {
		if c.request || c.reply {
			benchType = "request-reply"
		} else if c.js {
			if c.newJSAPI {
				benchType = "Jetstream (New simplified API)"
			} else {
				benchType = "JetStream (Pre-simplification API)"
			}
		} else {
			benchType = "Core NATS pub/sub"
		}

		argnvps = append(argnvps, nvp{c.subject, getSubscribeSubject(c)})
	}
	if !c.kv {
		argnvps = append(argnvps, nvp{"multisubject", f(c.multiSubject)})
		argnvps = append(argnvps, nvp{"multisubjectmax", f(c.multiSubjectMax)})
	}
	if c.request {
		argnvps = append(argnvps, nvp{"request", f(c.request)})
	}
	if c.reply {
		argnvps = append(argnvps, nvp{"reply", f(c.reply)})
	}
	if c.js {
		argnvps = append(argnvps, nvp{"js", f(c.js)})
		if c.newJSAPI {
			argnvps = append(argnvps, nvp{"JS API", "new"})
		} else {
			argnvps = append(argnvps, nvp{"JS API", "legacy"})
		}
	}
	argnvps = append(argnvps, nvp{"msgs", f(c.numMsg)})
	argnvps = append(argnvps, nvp{"msgsize", humanize.IBytes(uint64(c.msgSize))})
	argnvps = append(argnvps, nvp{"pubs", f(c.numPubs)})
	argnvps = append(argnvps, nvp{"subs", f(c.numSubs)})
	if c.js {
		argnvps = append(argnvps, nvp{"stream", c.streamName})
		argnvps = append(argnvps, nvp{"maxbytes", humanize.IBytes(uint64(c.streamMaxBytes))})
	}
	if c.js || c.kv {
		if c.streamName == DefaultStreamName || c.kv {
			argnvps = append(argnvps, nvp{"storage", c.storage})
		}
	}
	if c.js {
		argnvps = append(argnvps, nvp{"syncpub", f(c.syncPub)})
		argnvps = append(argnvps, nvp{"pubbatch", f(c.pubBatch)})
		argnvps = append(argnvps, nvp{"jstimeout", f(c.jsTimeout)})
		argnvps = append(argnvps, nvp{"pull", f(c.pull)})
		argnvps = append(argnvps, nvp{"consumerbatch", f(c.consumerBatch)})
		argnvps = append(argnvps, nvp{"push", f(c.push)})
		argnvps = append(argnvps, nvp{"consumername", c.consumerName})
	}
	if c.js || c.kv {
		argnvps = append(argnvps, nvp{"replicas", f(c.replicas)})
		if c.js {
			argnvps = append(argnvps, nvp{"purge", f(c.purge)})
		}
	}
	argnvps = append(argnvps, nvp{"pubsleep", f(c.pubSleep)})
	argnvps = append(argnvps, nvp{"subsleep", f(c.subSleep)})
	if c.js {
		argnvps = append(argnvps, nvp{"deduplication", f(c.deDuplication)})
		argnvps = append(argnvps, nvp{"dedup-window", f(c.deDuplicationWindow)})
	}

	banner := fmt.Sprintf("Starting %s benchmark [", benchType)

	var joinBuffer []string

	for _, v := range argnvps {
		joinBuffer = append(joinBuffer, v.name+"="+v.value)
	}
	banner += strings.Join(joinBuffer, ", ") + "]"

	log.Println(banner)

	bm := bench.NewBenchmark("NATS", c.numSubs, c.numPubs)

	benchId := strconv.FormatInt(time.Now().UnixMilli(), 16)

	startwg := &sync.WaitGroup{}
	donewg := &sync.WaitGroup{}

	var js nats.JetStreamContext

	storageType := func() nats.StorageType {
		switch c.storage {
		case "file":
			return nats.FileStorage
		case "memory":
			return nats.MemoryStorage
		default:
			{
				log.Printf("Unknown storage type %s, using memory", c.storage)
				return nats.MemoryStorage
			}
		}
	}()

	if c.js || c.kv {
		// create the stream for the benchmark (and purge it)
		nc, err := nats.Connect(opts.Config.ServerURL(), natsOpts()...)
		if err != nil {
			log.Fatalf("NATS connection failed: %v", err)
		}

		js, err = nc.JetStream(append(jsOpts(), nats.MaxWait(c.jsTimeout))...)
		if err != nil {
			log.Fatalf("Couldn't get the JetStream context: %v", err)
		}

		ctx, cancel := context.WithTimeout(context.Background(), c.jsTimeout)
		defer cancel()

		var js2 jetstream.JetStream

		switch {
		case opts.JsDomain != "":
			js2, err = jetstream.NewWithDomain(nc, opts.JsDomain)
		case opts.JsApiPrefix != "":
			js2, err = jetstream.NewWithAPIPrefix(nc, opts.JsApiPrefix)
		default:
			js2, err = jetstream.New(nc)
		}
		if err != nil {
			log.Fatalf("Couldn't get the new API JetStream instance: %v", err)
		}

		if c.kv {

			// There is no way to purge all the keys in a KV bucket in a single operation so deleting the bucket instead
			if c.purge {
				err = js2.DeleteStream(ctx, "KV_"+c.bucketName)
				// err = js.DeleteKeyValue(c.subject)
				if err != nil {
					log.Fatalf("Error trying to purge the bucket: %v", err)
				}
			}

			if c.bucketName == DefaultBucketName {
				// create bucket
				_, err := js.CreateKeyValue(&nats.KeyValueConfig{Bucket: c.bucketName, History: c.history, Storage: storageType, Description: "nats bench bucket", Replicas: c.replicas, MaxBytes: c.streamMaxBytes})
				if err != nil {
					log.Fatalf("Couldn't create the KV bucket: %v", err)
				}
			}
		} else if c.js {
			var s jetstream.Stream
			if c.streamName == DefaultStreamName {
				// create the stream with our attributes, will create it if it doesn't exist or make sure the existing one has the same attributes
				s, err = js2.CreateStream(ctx, jetstream.StreamConfig{Name: c.streamName, Subjects: []string{getSubscribeSubject(c)}, Retention: jetstream.LimitsPolicy, Discard: jetstream.DiscardNew, Storage: jetstream.StorageType(storageType), Replicas: c.replicas, MaxBytes: c.streamMaxBytes, Duplicates: c.deDuplicationWindow})
				if err != nil {
					log.Fatalf("%v. If you want to delete and re-define the stream use `nats stream delete %s`.", err, c.streamName)
				}
			} else if (c.pull || c.push) && c.numSubs > 0 {
				s, err = js2.Stream(ctx, c.streamName)
				if err != nil {
					log.Fatalf("Stream %s does not exist: %v", c.streamName, err)
				}
				log.Printf("Using stream: %s", c.streamName)
			}

			if c.purge {
				log.Printf("Purging the stream")
				err = s.Purge(ctx)
				if err != nil {
					log.Fatalf("Error purging stream %s: %v", c.streamName, err)
				}
			}

			if c.numSubs > 0 {
				// create the simplified API consumer
				if c.newJSAPI {
					if c.pull || c.push {
						ack := jetstream.AckNonePolicy
						if c.ack {
							ack = jetstream.AckExplicitPolicy
						}

						_, err := js2.CreateOrUpdateConsumer(ctx, c.streamName, jetstream.ConsumerConfig{
							Durable:       c.consumerName,
							DeliverPolicy: jetstream.DeliverAllPolicy,
							AckPolicy:     ack,
							ReplayPolicy:  jetstream.ReplayInstantPolicy,
							MaxAckPending: c.consumerBatch * c.numSubs,
						})
						if err != nil {
							log.Fatalf("Error creating consumer %s: %v", c.consumerName, err)
						}
						defer func() {
							err := js2.DeleteConsumer(ctx, c.streamName, c.consumerName)
							if err != nil {
								log.Fatalf("Error deleting the consumer on stream %s: %v", c.streamName, err)
							}
							log.Printf("Deleted durable consumer: %s\n", c.consumerName)
						}()
					}
				} else if c.pull || c.push {
					ack := nats.AckNonePolicy
					if c.ack {
						ack = nats.AckExplicitPolicy
					}
					if c.pull && c.consumerName == DefaultDurableConsumerName {
						_, err = js.AddConsumer(c.streamName, &nats.ConsumerConfig{
							Durable:       c.consumerName,
							DeliverPolicy: nats.DeliverAllPolicy,
							AckPolicy:     ack,
							ReplayPolicy:  nats.ReplayInstantPolicy,
							MaxAckPending: func(a int) int {
								if a >= 10000 {
									return a
								} else {
									return 10000
								}
							}(c.numSubs * c.consumerBatch),
						})
						if err != nil {
							log.Fatalf("Error creating the pull consumer: %v", err)
						}
						defer func() {
							err := js.DeleteConsumer(c.streamName, c.consumerName)
							if err != nil {
								log.Printf("Error deleting the pull consumer on stream %s: %v", c.streamName, err)
							}
							log.Printf("Deleted durable consumer: %s\n", c.consumerName)
						}()
						log.Printf("Defined durable pull consumer: %s\n", c.consumerName)
					} else if c.push && c.consumerName == DefaultDurableConsumerName {
						ack := nats.AckNonePolicy
						if c.ack {
							ack = nats.AckExplicitPolicy
						}
						maxAckPending := 0
						if c.ack {
							maxAckPending = c.consumerBatch * c.numSubs
						}
						_, err = js.AddConsumer(c.streamName, &nats.ConsumerConfig{
							Durable:        c.consumerName,
							DeliverSubject: c.consumerName + "-DELIVERY",
							DeliverGroup:   c.consumerName + "-GROUP",
							DeliverPolicy:  nats.DeliverAllPolicy,
							AckPolicy:      ack,
							ReplayPolicy:   nats.ReplayInstantPolicy,
							MaxAckPending:  maxAckPending,
						})
						if err != nil {
							log.Fatal("Error creating the durable push consumer: ", err)
						}

						defer func() {
							err := js.DeleteConsumer(c.streamName, c.consumerName)
							if err != nil {
								log.Fatalf("Error deleting the durable push consumer on stream %s: %v", c.streamName, err)
							}
							log.Printf("Deleted durable consumer: %s\n", c.consumerName)
						}()
					}
					if c.ack {
						log.Printf("Defined durable explicitly acked push consumer: %s\n", c.consumerName)
					} else {
						log.Printf("Defined durable unacked push consumer: %s\n", c.consumerName)
					}
				}
			}
		}
	}

	var offset = func(putter int, counts []int) int {
		var position = 0

		for i := 0; i < putter; i++ {
			position = position + counts[i]
		}
		return position
	}

	subCounts := bench.MsgsPerClient(c.numMsg, c.numSubs)

	for i := 0; i < c.numSubs; i++ {
		nc, err := nats.Connect(opts.Config.ServerURL(), natsOpts()...)
		if err != nil {
			return fmt.Errorf("nats connection %d failed: %s", i, err)
		}
		defer nc.Close()

		startwg.Add(1)
		donewg.Add(1)

		numMsg := func() int {
			if c.pull || c.reply || c.push || c.kv {
				return subCounts[i]
			} else {
				return c.numMsg
			}
		}()

		go c.runSubscriber(bm, nc, startwg, donewg, numMsg, offset(i, subCounts))
	}
	startwg.Wait()

	pubCounts := bench.MsgsPerClient(c.numMsg, c.numPubs)
	trigger := make(chan struct{})
	for i := 0; i < c.numPubs; i++ {
		nc, err := nats.Connect(opts.Config.ServerURL(), natsOpts()...)
		if err != nil {
			return fmt.Errorf("nats connection %d failed: %s", i, err)
		}
		defer nc.Close()

		startwg.Add(1)
		donewg.Add(1)

		go c.runPublisher(bm, nc, startwg, donewg, trigger, pubCounts[i], offset(i, pubCounts), benchId, strconv.Itoa(i))
	}

	if !c.noProgress {
		uiprogress.Start()
	}

	startwg.Wait()
	close(trigger)
	donewg.Wait()

	bm.Close()

	if !c.noProgress {
		uiprogress.Stop()
	}

	if c.fetchTimeout {
		log.Print("WARNING: at least one of the pull consumer Fetch operation timed out. These results are not optimal!")
	}

	if c.retriesUsed {
		log.Print("WARNING: at least one of the JS publish operations had to be retried. These results are not optimal!")
	}

	fmt.Println()
	fmt.Println(bm.Report())

	if c.csvFile != "" {
		csv := bm.CSV()
		err := os.WriteFile(c.csvFile, []byte(csv), 0644)
		if err != nil {
			log.Printf("error writing file %s: %v", c.csvFile, err)
		}
		fmt.Printf("Saved metric data in csv file %s\n", c.csvFile)
	}

	return nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func getSubscribeSubject(c *benchCmd) string {
	if c.multiSubject {
		return c.subject + ".*"
	} else {
		return c.subject
	}
}

func getPublishSubject(c *benchCmd, number int) string {
	if c.multiSubject {
		if c.multiSubjectMax == 0 {
			return c.subject + "." + strconv.Itoa(number)
		} else {
			return c.subject + "." + strconv.Itoa(number%c.multiSubjectMax)
		}
	} else {
		return c.subject
	}
}

func coreNATSPublisher(c benchCmd, nc *nats.Conn, progress *uiprogress.Bar, msg []byte, numMsg int, offset int) {

	var m *nats.Msg
	var err error

	errBytes := []byte("error")
	minusByte := byte('-')

	state := "Publishing"

	if progress != nil {
		progress.PrependFunc(func(b *uiprogress.Bar) string {
			return state
		})
	}

	for i := 0; i < numMsg; i++ {
		if progress != nil {
			progress.Incr()
		}

		if !c.request {
			err = nc.Publish(getPublishSubject(&c, i+offset), msg)
			if err != nil {
				log.Fatalf("Publish error: %v", err)
			}
		} else {
			m, err = nc.Request(getPublishSubject(&c, i+offset), msg, time.Second)
			if err != nil {
				log.Fatalf("Request error %v", err)
			}

			if len(m.Data) == 0 || m.Data[0] == minusByte || bytes.Contains(m.Data, errBytes) {
				log.Fatalf("Publish Request did not receive a positive ACK: %q", m.Data)
			}
		}
		time.Sleep(c.pubSleep)
	}
	state = "Finished  "
}

func jsPublisher(c *benchCmd, nc *nats.Conn, progress *uiprogress.Bar, msg []byte, numMsg int, idPrefix string, pubNumber string, offset int) {
	js, err := nc.JetStream(jsOpts()...)
	if err != nil {
		log.Fatalf("Couldn't get the JetStream context: %v", err)
	}

	var state string

	if progress != nil {
		progress.PrependFunc(func(b *uiprogress.Bar) string {
			return state
		})
	}

	if !c.syncPub {
		for i := 0; i < numMsg; {
			state = "Publishing"
			futures := make([]nats.PubAckFuture, min(c.pubBatch, numMsg-i))
			for j := 0; j < c.pubBatch && (i+j) < numMsg; j++ {
				if c.deDuplication {
					header := nats.Header{}
					header.Set(nats.MsgIdHdr, idPrefix+"-"+pubNumber+"-"+strconv.Itoa(i+j+offset))
					message := nats.Msg{Data: msg, Header: header, Subject: getPublishSubject(c, i+j+offset)}
					futures[j], err = js.PublishMsgAsync(&message)
				} else {
					futures[j], err = js.PublishAsync(getPublishSubject(c, i+j+offset), msg)
				}
				if err != nil {
					log.Fatalf("PubAsync error: %v", err)
				}
				if progress != nil {
					progress.Incr()
				}
				time.Sleep(c.pubSleep)
			}

			state = "AckWait   "

			select {
			case <-js.PublishAsyncComplete():
				state = "ProcessAck"
				for future := range futures {
					select {
					case <-futures[future].Ok():
						i++
					case err := <-futures[future].Err():
						if err.Error() == "nats: maximum bytes exceeded" {
							log.Fatalf("Stream maximum bytes exceeded, can not publish any more messages")
						}
						log.Printf("PubAsyncFuture for message %v in batch not OK: %v (retrying)", future, err)
						c.retriesUsed = true
					}
				}
			case <-time.After(c.jsTimeout):
				c.retriesUsed = true
				log.Printf("JS PubAsync ack timeout (pending=%d)", js.PublishAsyncPending())
				js, err = nc.JetStream(jsOpts()...)
				if err != nil {
					log.Fatalf("Couldn't get the JetStream context: %v", err)
				}
			}
		}
		state = "Finished  "
	} else {
		state = "Publishing"
		for i := 0; i < numMsg; i++ {
			if progress != nil {
				progress.Incr()
			}
			if c.deDuplication {
				header := nats.Header{}
				header.Set(nats.MsgIdHdr, idPrefix+"-"+pubNumber+"-"+strconv.Itoa(i+offset))
				message := nats.Msg{Data: msg, Header: header, Subject: getPublishSubject(c, i+offset)}
				_, err = js.PublishMsg(&message)
			} else {
				_, err = js.Publish(getPublishSubject(c, i+offset), msg)
			}
			if err != nil {
				if err.Error() == "nats: maximum bytes exceeded" {
					log.Fatalf("Stream maximum bytes exceeded, can not publish any more messages")
				}
				log.Printf("Publish error: %v (retrying)", err)
				c.retriesUsed = true
				i--
			}
			time.Sleep(c.pubSleep)
		}
	}
}

func kvPutter(c benchCmd, nc *nats.Conn, progress *uiprogress.Bar, msg []byte, numMsg int, offset int) {
	js, err := nc.JetStream(jsOpts()...)
	if err != nil {
		log.Fatalf("Couldn't get the JetStream context: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), c.jsTimeout)
	defer cancel()

	var js2 jetstream.JetStream

	switch {
	case opts.JsDomain != "":
		js2, err = jetstream.NewWithDomain(nc, opts.JsDomain)
	case opts.JsApiPrefix != "":
		js2, err = jetstream.NewWithAPIPrefix(nc, opts.JsApiPrefix)
	default:
		js2, err = jetstream.New(nc)
	}
	if err != nil {
		log.Fatalf("Couldn't get the new API JetStream instance: %v", err)
	}

	if c.newJSAPI {
		kvBucket, err := js2.KeyValue(ctx, c.bucketName)
		if err != nil {
			log.Fatalf("Couldn't find kv bucket %s: %v", c.bucketName, err)
		}
		var state string = "Putting   "

		if progress != nil {
			progress.PrependFunc(func(b *uiprogress.Bar) string {
				return state
			})
		}

		for i := 0; i < numMsg; i++ {
			if progress != nil {
				progress.Incr()
			}

			_, err = kvBucket.Put(ctx, fmt.Sprintf("%d", offset+i), msg)
			if err != nil {
				log.Fatalf("Put: %s", err)
			}

			time.Sleep(c.pubSleep)
		}
	} else {
		kvBucket, err := js.KeyValue(c.bucketName)
		if err != nil {
			log.Fatalf("Couldn't find kv bucket %s: %v", c.bucketName, err)
		}

		var state string = "Putting   "

		if progress != nil {
			progress.PrependFunc(func(b *uiprogress.Bar) string {
				return state
			})
		}

		for i := 0; i < numMsg; i++ {
			if progress != nil {
				progress.Incr()
			}

			_, err = kvBucket.Put(fmt.Sprintf("%d", offset+i), msg)
			if err != nil {
				log.Fatalf("Put: %s", err)
			}

			time.Sleep(c.pubSleep)
		}
	}

}

func (c *benchCmd) runPublisher(bm *bench.Benchmark, nc *nats.Conn, startwg *sync.WaitGroup, donewg *sync.WaitGroup, trigger chan struct{}, numMsg int, offset int, idPrefix string, pubNumber string) {
	startwg.Done()

	var progress *uiprogress.Bar

	if c.kv {
		log.Printf("Starting KV putter, putting %s messages", f(numMsg))
	} else {
		log.Printf("Starting publisher, publishing %s messages", f(numMsg))
	}

	if !c.noProgress {
		progress = uiprogress.AddBar(numMsg).AppendCompleted().PrependElapsed()
		progress.Width = progressWidth()
	}

	var msg []byte
	if c.msgSize > 0 {
		msg = make([]byte, c.msgSize)
	}

	<-trigger

	// introduces some jitter between the publishers if pubSleep is set and more than one publisher
	if c.pubSleep != 0 && pubNumber != "0" {
		n := rand.Intn(int(c.pubSleep))
		time.Sleep(time.Duration(n))
	}

	start := time.Now()

	if !c.js && !c.kv {
		coreNATSPublisher(*c, nc, progress, msg, numMsg, offset)
	} else if c.kv {
		kvPutter(*c, nc, progress, msg, numMsg, offset)
	} else if c.js {
		jsPublisher(c, nc, progress, msg, numMsg, idPrefix, pubNumber, offset)
	}

	err := nc.Flush()
	if err != nil {
		log.Fatalf("Could not flush the connection: %v", err)
	}

	bm.AddPubSample(bench.NewSample(numMsg, c.msgSize, start, time.Now(), nc))

	donewg.Done()
}

func (c *benchCmd) runSubscriber(bm *bench.Benchmark, nc *nats.Conn, startwg *sync.WaitGroup, donewg *sync.WaitGroup, numMsg int, offset int) {
	received := 0

	ch := make(chan time.Time, 2)

	var progress *uiprogress.Bar

	if !c.reply {
		if c.kv {
			log.Printf("Starting KV getter, trying to get %s messages", f(numMsg))
		} else {
			log.Printf("Starting subscriber, expecting %s messages", f(numMsg))
		}
	} else {
		log.Print("Starting replier, hit control-c to stop")
		c.noProgress = true
	}

	if !c.noProgress {
		progress = uiprogress.AddBar(numMsg).AppendCompleted().PrependElapsed()
		progress.Width = progressWidth()
	}

	state := "Setup     "

	if progress != nil {
		progress.PrependFunc(func(b *uiprogress.Bar) string {
			return state
		})
	}

	// Message handler
	mh := func(msg *nats.Msg) {
		received++
		if c.reply || (c.js && (c.pull || c.push)) {
			time.Sleep(c.subSleep)
			if c.ack {
				err := msg.Ack()
				if err != nil {
					log.Fatalf("Error acknowledging the  message: %v", err)
				}
			}
		}

		if !c.js && received == 1 {
			ch <- time.Now()
		}
		if received >= numMsg && !c.reply {
			ch <- time.Now()
		}
		if progress != nil {
			progress.Incr()
		}
	}

	// New API message handler
	mh2 := func(msg jetstream.Msg) {
		received++
		if c.push || c.pull {
			time.Sleep(c.subSleep)
			if c.ack {
				err := msg.Ack()
				if err != nil {
					log.Fatalf("Error acknowledging the message: %v", err)
				}
			}
		}

		if !c.js && received == 1 {
			ch <- time.Now()
		}
		if received >= numMsg && !c.reply {
			ch <- time.Now()
		}
		if progress != nil {
			progress.Incr()
		}
	}

	var sub *nats.Subscription
	var consumer jetstream.Consumer

	var err error

	if !c.kv {
		// create the subscriber
		if c.js {
			var js nats.JetStreamContext

			js, err = nc.JetStream(jsOpts()...)
			if err != nil {
				log.Fatalf("Couldn't get the JetStream context: %v", err)
			}

			ctx, cancel := context.WithTimeout(context.Background(), c.jsTimeout)
			defer cancel()

			var js2 jetstream.JetStream

			switch {
			case opts.JsDomain != "":
				js2, err = jetstream.NewWithDomain(nc, opts.JsDomain)
			case opts.JsApiPrefix != "":
				js2, err = jetstream.NewWithAPIPrefix(nc, opts.JsApiPrefix)
			default:
				js2, err = jetstream.New(nc)
			}
			if err != nil {
				log.Fatalf("Couldn't get the new API JetStream instance: %v", err)
			}

			// start the timer now rather than when the first message is received in JS mode

			startTime := time.Now()
			ch <- startTime
			if progress != nil {
				progress.TimeStarted = startTime
			}
			if c.newJSAPI {
				// new API
				state = "Consuming"
				s, err := js2.Stream(ctx, c.streamName)
				if err != nil {
					log.Fatalf("Error getting stream %s: %v", c.streamName, err)
				}
				if c.push || c.pull {
					consumer, err = s.Consumer(ctx, c.consumerName)
					if err != nil {
						log.Fatalf("Error getting consumer %s: %v", c.consumerName, err)
					}
				} else {
					consumer, err = s.OrderedConsumer(ctx, jetstream.OrderedConsumerConfig{})
					if err != nil {
						log.Fatalf("Error creating the ephemeral ordered consumer: %v", err)
					}
				}
				if c.push {
					cc, err := consumer.Consume(mh2, jetstream.PullMaxMessages(c.consumerBatch), jetstream.StopAfter(numMsg))
					if err != nil {
						return
					}
					defer cc.Stop()
				} else if !c.pull {
					// ordered
					cc, err := consumer.Consume(mh2, jetstream.PullMaxMessages(c.consumerBatch))
					if err != nil {
						return
					}
					defer cc.Stop()
				}
			} else if c.pull {
				sub, err = js.PullSubscribe(getSubscribeSubject(c), c.consumerName, nats.BindStream(c.streamName))
				if err != nil {
					log.Fatalf("Error PullSubscribe: %v", err)
				}
				defer sub.Drain()
			} else if c.push {
				state = "Receiving "
				sub, err = js.QueueSubscribe(getSubscribeSubject(c), c.consumerName+"-GROUP", mh, nats.Bind(c.streamName, c.consumerName), nats.ManualAck())
				if err != nil {
					log.Fatalf("Error push durable Subscribe: %v", err)
				}
				_ = sub.AutoUnsubscribe(numMsg)

			} else {
				state = "Consuming "
				// ordered push consumer
				sub, err = js.Subscribe(getSubscribeSubject(c), mh, nats.OrderedConsumer())
				if err != nil {
					log.Fatalf("Push consumer Subscribe error: %v", err)
				}
			}
		} else {
			state = "Receiving "
			if !c.reply {
				sub, err = nc.Subscribe(getSubscribeSubject(c), mh)
				if err != nil {
					log.Fatalf("Subscribe error: %v", err)
				}
			} else {
				sub, err = nc.QueueSubscribe(getSubscribeSubject(c), "bench-reply", mh)
				if err != nil {
					log.Fatalf("QueueSubscribe error: %v", err)
				}
			}
		}

		if !c.newJSAPI {
			err = sub.SetPendingLimits(-1, -1)
			if err != nil {
				log.Fatalf("Error setting pending limits on the subscriber: %v", err)
			}
		}
	}

	err = nc.Flush()
	if err != nil {
		log.Fatalf("Error flushing: %v", err)
	}

	startwg.Done()

	if c.kv {
		if c.newJSAPI {
			ctx, cancel := context.WithTimeout(context.Background(), c.jsTimeout)
			defer cancel()

			var js2 jetstream.JetStream

			switch {
			case opts.JsDomain != "":
				js2, err = jetstream.NewWithDomain(nc, opts.JsDomain)
			case opts.JsApiPrefix != "":
				js2, err = jetstream.NewWithAPIPrefix(nc, opts.JsApiPrefix)
			default:
				js2, err = jetstream.New(nc)
			}
			if err != nil {
				log.Fatalf("Couldn't get the new API JetStream instance: %v", err)
			}

			kvBucket, err := js2.KeyValue(ctx, c.bucketName)
			if err != nil {
				log.Fatalf("Couldn't find kv bucket %s: %v", c.bucketName, err)
			}

			// start the timer now rather than when the first message is received in JS mode
			startTime := time.Now()
			ch <- startTime

			if progress != nil {
				progress.TimeStarted = startTime
			}

			state = "Getting   "

			for i := 0; i < numMsg; i++ {
				entry, err := kvBucket.Get(ctx, fmt.Sprintf("%d", offset+i))

				if err != nil {
					log.Fatalf("Error getting key %d: %v", offset+i, err)
				}

				if entry.Value() == nil {
					log.Printf("Warning: got no value for key %d", offset+i)
				}

				if progress != nil {
					progress.Incr()
				}

				time.Sleep(c.subSleep)
			}

			ch <- time.Now()
		} else {
			var js nats.JetStreamContext

			js, err = nc.JetStream(jsOpts()...)
			if err != nil {
				log.Fatalf("Couldn't get the JetStream context: %v", err)
			}

			kvBucket, err := js.KeyValue(c.bucketName)
			if err != nil {
				log.Fatalf("Couldn't find kv bucket %s: %v", c.bucketName, err)
			}

			// start the timer now rather than when the first message is received in JS mode
			startTime := time.Now()
			ch <- startTime

			if progress != nil {
				progress.TimeStarted = startTime
			}

			state = "Getting   "

			for i := 0; i < numMsg; i++ {
				entry, err := kvBucket.Get(fmt.Sprintf("%d", offset+i))
				if err != nil {
					log.Fatalf("Error getting key %d: %v", offset+i, err)
				}

				if entry.Value() == nil {
					log.Printf("Warning: got no value for key %d", offset+i)
				}

				if progress != nil {
					progress.Incr()
				}

				time.Sleep(c.subSleep)
			}

			ch <- time.Now()
		}
	} else if c.js && c.pull {
		for i := 0; i < numMsg; {
			batchSize := func() int {
				if c.consumerBatch <= (numMsg - i) {
					return c.consumerBatch
				} else {
					return numMsg - i
				}
			}()

			if progress != nil {
				if c.pull {
					state = "Pulling   "
				} else if c.newJSAPI {
					state = "Consuming "
				}
			}

			if c.newJSAPI {
				if c.pull {
					msgs, err := consumer.Fetch(batchSize)
					if err == nil && msgs.Error() == nil {
						if progress != nil {
							state = "Fetching  "
						}

						for msg := range msgs.Messages() {
							mh2(msg)
							i++
						}
					} else {
						if c.noProgress {
							if err == nats.ErrTimeout {
								log.Print("Fetch  timeout!")
							} else {
								log.Printf("New consumer Fetch error: %v", err)
							}
						}
						c.fetchTimeout = true
					}
				}
			} else if c.pull {
				msgs, err := sub.Fetch(batchSize, nats.MaxWait(c.jsTimeout))
				if err == nil {
					if progress != nil {
						state = "Handling  "
					}

					for _, msg := range msgs {
						mh(msg)
						i++
					}
				} else {
					if c.noProgress {
						if err == nats.ErrTimeout {
							log.Print("Fetch timeout!")
						} else {
							log.Printf("Pull consumer Fetch error: %v", err)
						}
					}
					c.fetchTimeout = true
				}
			}
		}
	}

	start := <-ch
	end := <-ch

	state = "Finished  "

	bm.AddSubSample(bench.NewSample(numMsg, c.msgSize, start, end, nc))

	donewg.Done()
}
