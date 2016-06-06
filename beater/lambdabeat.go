package beater

import (
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

func GetFunctionMetric(metric string, start time.Time, end time.Time, functionName string) *cloudwatch.GetMetricStatisticsOutput {
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

	logp.Info("error: %v", err)
	return resp
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
	if bt.beatConfig.Lambdabeat.Period == "" {
		bt.beatConfig.Lambdabeat.Period = "300s"
	}

	if bt.beatConfig.Lambdabeat.Metrics == nil {
		bt.beatConfig.Lambdabeat.Metrics = []string{"Invocations", "Duration"}
	}

	if bt.beatConfig.Lambdabeat.Functions == nil {
		bt.beatConfig.Lambdabeat.Functions = []string{"infra-issue-stats"}
	}

	bt.client = b.Publisher.Connect()

	var err error
	bt.period, err = time.ParseDuration(bt.beatConfig.Lambdabeat.Period)
	if err != nil {
		return err
	}

	bt.functions = bt.beatConfig.Lambdabeat.Functions
	bt.metrics = bt.beatConfig.Lambdabeat.Metrics

	// TODO: make this configurable as a backfill date
	t, _ := time.Parse(common.TsLayout, "2016-06-06T00:00:00.000Z")
	bt.lastTime = t

	logp.Info("lambdabeat", "Initializing lambdabeat")
	logp.Info("lambdabeat", "Period %v\n", bt.period)
	logp.Info("lambdabeat", "Functions %v\n", bt.functions)
	logp.Info("lambdabeat", "Metrics %v\n", bt.metrics)
	logp.Info("lambdabeat", "Time %v\n", bt.lastTime)

	return nil
}

func FetchMetric(fn string, metric string, start time.Time, end time.Time) []map[string]interface{} {
	// TODO: make a type for this
	var stats []map[string]interface{}

	data := GetFunctionMetric(metric, start, end, fn)

	for _, d := range data.Datapoints {
		//		timestr := d.Timestamp.String()
		//		logp.Info("TIME STRING: %v", timestr)
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

	return stats
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
		logp.Info("doing things")
		for _, fn := range bt.functions {
			for _, m := range bt.metrics {
				events := FetchMetric(fn, m, bt.lastTime, now)
				for _, event := range events {
					bt.client.PublishEvent(event)
					logp.Info("Event sent")
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
