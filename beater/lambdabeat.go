package beater

import (
	"errors"
	"fmt"
	"time"

	"github.com/elastic/beats/libbeat/beat"
	"github.com/elastic/beats/libbeat/common"
	"github.com/elastic/beats/libbeat/logp"
	"github.com/elastic/beats/libbeat/publisher"

	"github.com/michaelbaamonde/lambdabeat/config"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/cloudwatch"
)

type Lambdabeat struct {
	beatConfig *config.Config
	done       chan struct{}
	period     time.Duration
	lastTime   time.Time
	client     publisher.Client
	metrics    []string
	functions  []string
}

// Creates beater
func New() *Lambdabeat {
	return &Lambdabeat{
		done: make(chan struct{}),
	}
}

func GetFunctionMetric(metric string, start time.Time, end time.Time, functionName string) (*cloudwatch.GetMetricStatisticsOutput, error) {
	// TODO: make region configurable
	svc := cloudwatch.New(session.New(), &aws.Config{Region: aws.String("us-east-1")})

	params := &cloudwatch.GetMetricStatisticsInput{
		EndTime:    aws.Time(end),
		MetricName: aws.String(metric),
		Namespace:  aws.String("AWS/Lambda"),
		Period:     aws.Int64(60),
		StartTime:  aws.Time(start),
		Statistics: []*string{
			aws.String("Average"),
			aws.String("Maximum"),
			aws.String("Minimum"),
			aws.String("SampleCount"),
			aws.String("Sum"),
		},
		Dimensions: []*cloudwatch.Dimension{
			{
				Name:  aws.String("FunctionName"),
				Value: aws.String(functionName),
			},
		},
	}
	resp, err := svc.GetMetricStatistics(params)

	if err != nil {
		return nil, err
	}

	return resp, nil
}

/// *** Beater interface methods ***///

func (bt *Lambdabeat) Config(b *beat.Beat) error {

	// Load beater beatConfig
	err := b.RawConfig.Unpack(&bt.beatConfig)
	if err != nil {
		return fmt.Errorf("Error reading config file: %v", err)
	}

	return nil
}

func (bt *Lambdabeat) Setup(b *beat.Beat) error {

	cfg := bt.beatConfig.Lambdabeat

	if cfg.Period != "" {
		period, err := time.ParseDuration(cfg.Period)
		if err != nil {
			return err
		} else {
			bt.period = period
		}
	} else {
		period, err := time.ParseDuration("300s")
		if err != nil {
			return err
		} else {
			bt.period = period
		}
	}

	if cfg.Metrics != nil {
		bt.metrics = cfg.Metrics
	} else {
		bt.metrics = []string{"Invocations", "Duration", "Errors"}
	}

	if cfg.Functions != nil {
		bt.functions = cfg.Functions
	} else {
		return errors.New("Must provide a list of Lambda functions.")
	}

	if cfg.BackfillDate != "" {
		t, err := time.Parse(common.TsLayout, cfg.BackfillDate)
		if err != nil {
			return err
		} else {
			bt.lastTime = t
		}
	} else {
		bt.lastTime = time.Now()
	}

	bt.client = b.Publisher.Connect()

	logp.Debug("lambdabeat", "Initializing lambdabeat")
	logp.Debug("lambdabeat", "Period: %s\n", bt.period)
	logp.Debug("lambdabeat", "Functions: %v\n", bt.functions)
	logp.Debug("lambdabeat", "Metrics: %v\n", bt.metrics)
	logp.Debug("lambdabeat", "Time: %s\n", bt.lastTime)

	return nil
}

func FetchMetric(fn string, metric string, start time.Time, end time.Time) ([]common.MapStr, error) {
	var stats []common.MapStr

	data, err := GetFunctionMetric(metric, start, end, fn)

	if err != nil {
		return nil, err
	} else {
		for _, d := range data.Datapoints {
			timestr := d.Timestamp.Format(common.TsLayout)
			t, _ := common.ParseTime(timestr)

			event := common.MapStr{
				"type":         "metric",
				"function":     fn,
				"metric":       metric,
				"@timestamp":   t,
				"average":      d.Average,
				"maximum":      d.Maximum,
				"minimum":      d.Minimum,
				"sample-count": d.SampleCount,
				"sum":          d.Sum,
				"unit":         d.Unit,
			}
			stats = append(stats, event)
		}

		return stats, nil
	}
}

func (bt *Lambdabeat) Run(b *beat.Beat) error {
	logp.Info("lambdabeat is running! Hit CTRL-C to stop it.")

	ticker := time.NewTicker(bt.period)

	for {
		select {
		case <-bt.done:
			return nil
		case <-ticker.C:
		}

		now := time.Now()

		for _, fn := range bt.functions {
			for _, m := range bt.metrics {
				events, err := FetchMetric(fn, m, bt.lastTime, now)
				if err != nil {
					logp.Err("error: %v", err)
				} else {
					for _, event := range events {
						bt.client.PublishEvent(event)
						logp.Info("Event sent")
					}
				}
			}
		}

		bt.lastTime = now
	}

}

func (bt *Lambdabeat) Cleanup(b *beat.Beat) error {
	return nil
}

func (bt *Lambdabeat) Stop() {
	close(bt.done)
}
