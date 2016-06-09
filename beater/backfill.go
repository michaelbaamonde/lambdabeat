package beater

import (
	"time"

	"github.com/elastic/beats/libbeat/logp"
)

const maxDatapoints = 1440

type dateRange struct {
	start time.Time
	end   time.Time
}

func ExceedsMaxDatapointsThreshold(start time.Time, end time.Time, interval int64) bool {
	secondsBetween := int64(end.Sub(start).Seconds())
	datapoints := secondsBetween / interval

	return datapoints > maxDatapoints
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
