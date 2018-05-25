package kafkatail

import (
	"context"
	"github.com/Shopify/sarama"
	"log"
)

type Options struct {
	Server    string `long:"server" description:"kafka server" default:"localhost"`
	Port      string `long:"port" description:"kafka port" default:"9092"`
	Topic     string `long:"topic" description:"kafka topic" default:"my_topic"`
	Partition int32  `long:"partition" description:"partition to read from"`
	Offset    int64  `long:"offset" description:"offset to start from" default:"sarama.OffsetNewest"`
}

// GetChans returns a list of channels but it only ever has one entry - the
// partition on which we're listening.
// TODO listen on multiple channels to multiple partitions
func GetChans(ctx context.Context, options Options) ([]chan string, chan error) {
	linesCh := make([]chan string, 1, 1)
	lines := make(chan string, 1)
	linesCh[0] = lines

	config := sarama.NewConfig()
	config.Consumer.Return.Errors = true

	brokers := []string{options.Server + ":" + options.Port}

	consumer, err := sarama.NewConsumer(brokers, config)
	if err != nil {
		panic(err)
	}

	partitionConsumer, err := consumer.ConsumePartition(options.Topic, options.Partition, options.Offset)
	if err != nil {
		panic(err)
	}

	errCh := make(chan error, 1)

	go func() {
		log.Printf("Consumer started for topic %s\n", options.Topic)

		defer func() {
			log.Printf("Terminating consumer for topic %s\n", options.Topic)
			close(lines)

			if err := partitionConsumer.Close(); err != nil {
				log.Fatalln(err)
			}

			if err := consumer.Close(); err != nil {
				log.Fatalln(err)
			}
		}()

	ConsumerLoop:
		for {
			select {
			case err := <-partitionConsumer.Errors():
				log.Printf("Error at offset %d in topic %s: %v\n",
					partitionConsumer.HighWaterMarkOffset()-1, options.Topic, err)
				errCh <- err
			case msg := <-partitionConsumer.Messages():
				lines <- string(msg.Value)
			case <-ctx.Done():
				break ConsumerLoop
			}
		}
	}()

	return linesCh, errCh
}
