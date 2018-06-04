package kafkatail

import (
	"context"
	"errors"
	"fmt"
	"github.com/Shopify/sarama"
	log "github.com/Sirupsen/logrus"
	"github.com/cenk/backoff"
	"github.com/rubyist/circuitbreaker"
	"os"
	"time"
)

type Options struct {
	Server         string `long:"server" description:"kafka server" default:"localhost"`
	Port           string `long:"port" description:"kafka port" default:"9092"`
	Topic          string `long:"topic" description:"kafka topic"`
	Partition      int32  `long:"partition" description:"partition to read from"`
	StartingOffset int64  `long:"offset" description:"offset to start from, or newest" default:"-1"`
}

// GetChans returns a list of channels but it only ever has one entry - the
// partition on which we're listening.
// TODO listen on multiple channels to multiple partitions
func GetChans(ctx context.Context, options Options) ([]chan string, error) {
	lines := make(chan string, 1)

	linesChan := make([]chan string, 1, 1)
	linesChan[0] = lines

	config := sarama.NewConfig()
	config.ClientID = makeClientId()
	config.Consumer.Return.Errors = true
	brokers := []string{options.Server + ":" + options.Port}

	sarama.Logger = log.StandardLogger()
	consumer, err := sarama.NewConsumer(brokers, config)
	if err != nil {
		return nil, err
	}

	lastSuccess := options.StartingOffset

	// TODO: Make these configurable
	expBackoff := backoff.NewExponentialBackOff()
	expBackoff.InitialInterval = 2 * time.Second
	expBackoff.MaxInterval = 30 * time.Second
	expBackoff.MaxElapsedTime = 5 * time.Minute

	breaker := circuit.NewConsecutiveBreaker(3)
	breaker.BackOff = expBackoff

	partitionConsumer, err := consumer.ConsumePartition(options.Topic, options.Partition, lastSuccess)

	if err != nil {
		log.Infof("Error starting PartitionConsumer for topic [%v] partition [%d] "+
			"with offset [%d]\n", options.Topic, options.Partition, options.StartingOffset)
		return nil, err
	}

	log.Infof("Started PartitionConsumer for topic [%v] partition [%d] "+
		"with offset [%d]\n", options.Topic, options.Partition, options.StartingOffset)

	go func() {
		defer func() {
			log.Infof("Stopping consumers for topic [%v] partition [%d]; "+
				"last successfully read offset [%d]\n", options.Topic, options.Partition, lastSuccess)

			close(lines)

			err := partitionConsumer.Close()
			if err != nil {
				log.Fatalf("Error shutting down partition consumer for topic [%v] partition [%d]\n",
					options.Topic, options.Partition)
			}

			err = consumer.Close()
			if err != nil {
				log.Fatalf("Error shutting down consumer for brokers [%v]\n", brokers)
				panic(err)
			}
		}()

		elapsedSinceBackoff := expBackoff.GetElapsedTime()
		for {
			if breaker.Ready() {
				select {
				case msg, open := <-partitionConsumer.Messages():
					if !open {
						break
					} else if msg != nil {
						breaker.Success()
						lastSuccess = msg.Offset
						lines <- string(msg.Value)
					}
				case err := <-partitionConsumer.Errors():
					breaker.Fail()
					if breaker.Tripped() {
						elapsedSinceBackoff = expBackoff.GetElapsedTime()
						log.Warnf("Recent error rate: %v\n", breaker.ErrorRate())
						log.Warnf("Backing off due to too many errors; last error was: %v\n", err)
					}
				case <-ctx.Done():
					break
				}
			} else {
				elapsedWaiting := expBackoff.GetElapsedTime() - elapsedSinceBackoff
				if elapsedWaiting > expBackoff.MaxInterval {
					// We've waited long enough, and the circuit will always stay open after this.
					log.Errorf("Giving up trying to consume topic [%v] partition [%v] after [%v]",
						options.Topic, options.Partition, elapsedWaiting.String())

					// LATER: Close & reconnect to Kafka
					panic(errors.New("kafkatail failed after: " + elapsedWaiting.String()))
				}

				time.Sleep(expBackoff.InitialInterval)
			}
		}
	}()

	return linesChan, err
}

func makeClientId() string {
	if hostName, err := os.Hostname(); err != nil {
		panic(err)
	} else {
		return fmt.Sprintf("honeykafka_%v_%d", hostName, os.Getpid())
	}
}
