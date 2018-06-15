package kafkatail

import (
	"context"
	"fmt"
	"github.com/Shopify/sarama"
	log "github.com/Sirupsen/logrus"
	"os"
)

type Options struct {
	Server    string `long:"server" description:"kafka server" default:"localhost"`
	Port      string `long:"port" description:"kafka port" default:"9092"`
	Topic     string `long:"topic" description:"kafka topic" default:"my_topic"`
	Partition int32  `long:"partition" description:"partition to read from"`
	ClientLog string `long:"client_log" description:"file to redirect Kafka client logs"`
}

// GetChans returns a list of channels but it only ever has one entry - the
// partition on which we're listening.
// TODO listen on multiple channels to multiple partitions
func GetChans(ctx context.Context, options Options) ([]chan string, error) {
	partitionRecords := make(chan string, 1)

	partitions := make([]chan string, 1, 1)
	partitions[0] = partitionRecords

	if logFilePath := options.ClientLog; logFilePath != "" {
		logger := log.New()

		if f, err := os.OpenFile(logFilePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666); err != nil {
			log.Warnf("Failed to write Kafka client logs to [%v], using stderr: %v",
				logFilePath, err)
		} else {
			logger.Out = f
		}
		sarama.Logger = logger
	}

	config := sarama.NewConfig()
	config.ClientID = makeClientId(options.Topic, options.Partition)

	brokers := []string{options.Server + ":" + options.Port}
	consumer, err := sarama.NewConsumer(brokers, config)
	if err != nil {
		return nil, err
	}

	partitionConsumer, err := consumer.ConsumePartition(
		options.Topic, options.Partition, sarama.OffsetNewest)
	if err != nil {
		log.Errorf("Error starting PartitionConsumer for topic [%v] partition [%d]\n",
			options.Topic, options.Partition)
		return nil, err
	}

	go func() {
		log.Infof("Started consumer for topic [%v] partition [%d]\n",
			options.Topic, options.Partition)

		var lastSuccessOffset int64
		var recordsConsumed int64

		defer func() {
			log.Infof("Stopping consumer for topic [%v] partition [%d]; "+
				"consumed records: [%v]; last successfully read offset [%d]",
				options.Topic, options.Partition, recordsConsumed, lastSuccessOffset)

			close(partitionRecords)

			err := partitionConsumer.Close()
			if err != nil {
				log.Errorf("Error shutting down partition consumer for topic [%v] partition [%d]",
					options.Topic, options.Partition)
			}

			err = consumer.Close()
			if err != nil {
				log.Errorf("Error shutting down consumer for brokers [%v]", brokers)
			}
		}()

		for {
			select {
			case msg, ok := <-partitionConsumer.Messages():
				if msg != nil {
					partitionRecords <- string(msg.Value)
					lastSuccessOffset = max(lastSuccessOffset, msg.Offset)
					recordsConsumed++
				}
				if !ok {
					// Kafka client bailed on us; start clean up, drain messages, then bail out.
					partitionConsumer.AsyncClose()
					for range partitionConsumer.Messages() {
						partitionRecords <- string(msg.Value)
						lastSuccessOffset = max(lastSuccessOffset, msg.Offset)
						recordsConsumed++
					}
					return
				}
			case <-ctx.Done():
				// listen for the context's Done channel to clean up and exit
				return
			}
		}
	}()

	return partitions, nil
}

func max(a int64, b int64) int64 {
	if a < b {
		return b
	}
	return b
}

func makeClientId(topic string, partition int32) string {
	return fmt.Sprintf("honeykafka_%v_%v", topic, partition)
}
