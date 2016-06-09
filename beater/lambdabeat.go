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
	"github.com/aws/aws-sdk-go/service/lambda"
)

type Lambdabeat struct {
	beatConfig *config.Config
	done       chan struct{}
	period     time.Duration
	interval   int64
	lastTime   time.Time
	client     publisher.Client
	metrics    []string
	functions  []string
	region     string
	backfill   time.Time
}

// Creates beater
func New() *Lambdabeat {
	return &Lambdabeat{
		done: make(chan struct{}),
	}
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

// List all Lambda functions

func ListAllLambdaFunctions(region string) ([]string, error) {
	svc := lambda.New(session.New(), &aws.Config{Region: aws.String(region)})

	data, err := svc.ListFunctions(nil)

	if err != nil {
		return nil, err
	}

	var fns []string

	for _, fn := range data.Functions {
		fnName := *fn.FunctionName
		fns = append(fns, fnName)
	}

	return fns, nil
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

	if cfg.Interval%60 == 0 {
		bt.interval = cfg.Interval
	} else {
		return errors.New("Interval must be a multiple of 60.")
	}

	if len(cfg.Metrics) >= 1 {
		bt.metrics = cfg.Metrics
	} else {
		bt.metrics = []string{"Invocations", "Duration", "Errors", "Throttles"}
	}

	if len(cfg.Functions) >= 1 {
		bt.functions = cfg.Functions
	} else {
		return errors.New("Must provide a list of Lambda functions.")
	}

	if cfg.BackfillDate != "" {
		t, err := time.Parse(common.TsLayout, cfg.BackfillDate)
		if err != nil {
			return err
		} else {
			bt.backfill = t
		}
	} else {
		bt.backfill = time.Time{}
		bt.lastTime = time.Now()
	}

	if cfg.Region != "" {
		bt.region = cfg.Region
	} else {
		return errors.New("Must provide an AWS region.")
	}

	bt.client = b.Publisher.Connect()

	logp.Debug("lambdabeat", "Initializing lambdabeat")
	logp.Debug("lambdabeat", "Period: %s\n", bt.period)
	logp.Debug("lambdabeat", "Functions: %v\n", bt.functions)
	logp.Debug("lambdabeat", "Metrics: %v\n", bt.metrics)
	logp.Debug("lambdabeat", "Time: %s\n", bt.lastTime)

	return nil
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
		if !bt.backfill.IsZero() {
			logp.Info("running backfill: %v", bt.backfill)
			bt.RunBackfill()
		} else {
			logp.Info("running periodic")
			bt.RunPeriodic()
		}
	}
}

func (bt *Lambdabeat) Cleanup(b *beat.Beat) error {
	return nil
}

func (bt *Lambdabeat) Stop() {
	close(bt.done)
}

/// *** Processing logic ***///

func (bt *Lambdabeat) FetchFunctionMetric(fn string, metric string, end time.Time) ([]common.MapStr, error) {
	var stats []common.MapStr

	svc := cloudwatch.New(session.New(), &aws.Config{Region: aws.String(bt.region)})

	params := &cloudwatch.GetMetricStatisticsInput{
		EndTime:    aws.Time(end),
		MetricName: aws.String(metric),
		Namespace:  aws.String("AWS/Lambda"),
		Period:     aws.Int64(bt.interval),
		StartTime:  aws.Time(bt.lastTime),
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
				Value: aws.String(fn),
			},
		},
	}

	data, err := svc.GetMetricStatistics(params)

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

func (bt *Lambdabeat) RunPeriodic() error {
	now := time.Now()

	for _, fn := range bt.functions {
		for _, m := range bt.metrics {
			events, err := bt.FetchFunctionMetric(fn, m, now)
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
	return nil
}

// Backfilling

const maxDatapoints = 1440

type dateRange struct {
	start time.Time
	end   time.Time
}

// Given a date range and an interval, chunk the range into a sequence of ranges
// such that no single range exceeds maxDatapoints.
func PartitionDateRange(start time.Time, end time.Time, interval int64) []dateRange {
	startUnix := start.Unix()
	endUnix := end.Unix()

	offset := maxDatapoints * interval

	var ranges []dateRange

	for t := startUnix; t < endUnix; t += offset {
		t1 := time.Unix(t, 0)
		t2 := time.Unix((t + offset), 0)

		r := dateRange{start: t1, end: t2}
		ranges = append(ranges, r)
	}

	return ranges

}

func (bt *Lambdabeat) RunBackfill() error {
	end := time.Now()

	ranges := PartitionDateRange(bt.backfill, end, bt.interval)

	for _, r := range ranges {
		logp.Info("start: %v, end: %v", r.start, r.end)
		for _, fn := range bt.functions {
			for _, m := range bt.metrics {
				bt.lastTime = r.start
				events, err := bt.FetchFunctionMetric(fn, m, r.end)
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
	}

	bt.Stop()

	return nil
}
