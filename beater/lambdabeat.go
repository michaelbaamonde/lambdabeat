package beater

import (
	"errors"
	"fmt"
	"sort"
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
	beatConfig             *config.Config
	done                   chan struct{}
	period                 time.Duration
	interval               int64
	lastTime               time.Time
	client                 publisher.Client
	functions              []string
	functionConfigurations map[string]lambda.FunctionConfiguration
	region                 string
	backfill               time.Time
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

	data, err := svc.ListFunctions(&lambda.ListFunctionsInput{})

	if err != nil {
		return nil, err
	}

	var fns []string

	// Get first page of results
	for _, fn := range data.Functions {
		fnName := *fn.FunctionName
		fns = append(fns, fnName)
	}

	// If there is more than one page, keep requesting pages until we get them all
	marker := data.NextMarker
	for marker != nil {
		input := &lambda.ListFunctionsInput{
			Marker: aws.String(*marker),
		}

		data, err := svc.ListFunctions(input)

		if err != nil {
			return nil, err
		}

		for _, fn := range data.Functions {
			fnName := *fn.FunctionName
			fns = append(fns, fnName)
		}

		marker = data.NextMarker
	}

	return fns, nil
}

func (bt *Lambdabeat) FetchFunctionConfigs() error {
	svc := lambda.New(session.New(), &aws.Config{Region: aws.String(bt.region)})

	for _, fn := range bt.functions {
		params := &lambda.GetFunctionInput{
			FunctionName: aws.String(fn),
		}
		data, err := svc.GetFunction(params)

		if err != nil {
			return err
		}

		config := *data.Configuration
		bt.functionConfigurations[fn] = config
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

	if cfg.Interval%60 == 0 {
		bt.interval = cfg.Interval
	} else {
		return errors.New("Interval must be a multiple of 60.")
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

	if len(cfg.Functions) >= 1 {
		bt.functions = cfg.Functions
	} else {
		fns, err := ListAllLambdaFunctions(bt.region)
		if err != nil {
			return err
		}
		logp.Info("No functions configured, using: %v", fns)
		bt.functions = fns
	}

	bt.functionConfigurations = make(map[string]lambda.FunctionConfiguration)
	err := bt.FetchFunctionConfigs()

	if err != nil {
		return err
	}

	bt.client = b.Publisher.Connect()

	logp.Debug("lambdabeat", "Initializing lambdabeat")
	logp.Debug("lambdabeat", "Period: %s\n", bt.period)
	logp.Debug("lambdabeat", "Functions: %v\n", bt.functions)
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
			logp.Info("Running backfill from date: %v", bt.backfill)
			bt.RunBackfill()
		} else {
			logp.Info("Running periodically.")
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

type Datapoints []cloudwatch.Datapoint

func (slice Datapoints) Less(i, j int) bool {
	t1 := *slice[i].Timestamp
	t2 := *slice[j].Timestamp

	return t1.Before(t2)
}

func (slice Datapoints) Len() int {
	return len(slice)
}

func (slice Datapoints) Swap(i, j int) {
	slice[i], slice[j] = slice[j], slice[i]
}

func (bt *Lambdabeat) FetchFunctionMetric(fn string, metric string, end time.Time) (Datapoints, error) {
	var stats Datapoints

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
	}

	for _, d := range data.Datapoints {
		stats = append(stats, *d)
	}

	return stats, nil
}

func (bt *Lambdabeat) CreateEvents(fn string, end time.Time) ([]common.MapStr, error) {
	var events []common.MapStr

	fnConfig := bt.functionConfigurations[fn]

	// Fetch all metrics for the time period
	invocations, err := bt.FetchFunctionMetric(fn, "Invocations", end)
	if err != nil {
		return nil, err
	}

	errors, err := bt.FetchFunctionMetric(fn, "Errors", end)
	if err != nil {
		return nil, err
	}

	duration, err := bt.FetchFunctionMetric(fn, "Duration", end)
	if err != nil {
		return nil, err
	}

	throttles, err := bt.FetchFunctionMetric(fn, "Throttles", end)
	if err != nil {
		return nil, err
	}

	// Sort the arrays of Datapoints so that we don't mix up time periods
	// when combining into a document
	sort.Sort(invocations)
	sort.Sort(errors)
	sort.Sort(duration)
	sort.Sort(throttles)

	// Index a single document for each timestamp
	for i := 0; i < len(invocations); i++ {
		inv := invocations[i]
		err := errors[i]
		dur := duration[i]
		thr := throttles[i]

		timestr := inv.Timestamp.Format(common.TsLayout)
		t, _ := common.ParseTime(timestr)

		event := common.MapStr{
			"@timestamp":               t,
			"type":                     "metric",
			"function":                 fn,
			"description":              *fnConfig.Description,
			"last-modified":            *fnConfig.LastModified,
			"memory-size":              *fnConfig.MemorySize,
			"runtime":                  *fnConfig.Runtime,
			"code-size":                *fnConfig.CodeSize,
			"handler":                  *fnConfig.Handler,
			"timeout":                  *fnConfig.Timeout,
			"version":                  *fnConfig.Version,
			"invocations-average":      inv.Average,
			"invocations-maximum":      inv.Maximum,
			"invocations-minimum":      inv.Minimum,
			"invocations-sum":          inv.Sum,
			"invocations-sample-count": inv.SampleCount,
			"invocations-unit":         inv.Unit,
			"errors-average":           err.Average,
			"errors-maximum":           err.Maximum,
			"errors-minimum":           err.Minimum,
			"errors-sum":               err.Sum,
			"errors-sample-count":      err.SampleCount,
			"errors-unit":              err.Unit,
			"duration-average":         dur.Average,
			"duration-maximum":         dur.Maximum,
			"duration-minimum":         dur.Minimum,
			"duration-sum":             dur.Sum,
			"duration-sample-count":    dur.SampleCount,
			"duration-unit":            dur.Unit,
			"throttles-average":        thr.Average,
			"throttles-maximum":        thr.Maximum,
			"throttles-minimum":        thr.Minimum,
			"throttles-sum":            thr.Sum,
			"throttles-sample-count":   thr.SampleCount,
			"throttles-unit":           thr.Unit,
		}
		events = append(events, event)
	}

	return events, nil

}

func (bt *Lambdabeat) RunPeriodic() error {
	now := time.Now()

	for _, fn := range bt.functions {
		events, err := bt.CreateEvents(fn, now)
		if err != nil {
			logp.Err("error: %v", err)
		} else {
			for _, event := range events {
				bt.client.PublishEvent(event)
				logp.Info("Event sent")
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
			bt.lastTime = r.start
			events, err := bt.CreateEvents(fn, r.end)
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

	bt.Stop()

	return nil
}
