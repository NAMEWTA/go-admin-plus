package scheduler

import (
	"encoding/json"
	"strings"
	"time"
	_ "time/tzdata"

	"github.com/robfig/cron/v3"
)

// Schedule 使用标准五段 Cron；时区属于任务定义，数据库中的执行时刻始终为 UTC。
type Schedule struct {
	Cron     string `json:"cron"`
	Timezone string `json:"timezone"`
}

func normalizeSchedule(value Schedule) (Schedule, bool) {
	fields := strings.Fields(value.Cron)
	if len(fields) != 5 || len(value.Cron) > 256 {
		return Schedule{}, false
	}
	value.Cron = strings.Join(fields, " ")
	value.Timezone = strings.TrimSpace(value.Timezone)
	if value.Timezone == "" {
		value.Timezone = "Asia/Shanghai"
	}
	if len(value.Timezone) > 100 {
		return Schedule{}, false
	}
	if _, err := time.LoadLocation(value.Timezone); err != nil {
		return Schedule{}, false
	}
	if _, err := cron.ParseStandard("CRON_TZ=" + value.Timezone + " " + value.Cron); err != nil {
		return Schedule{}, false
	}
	return value, true
}

func nextOccurrence(value Schedule, after time.Time) (time.Time, bool) {
	value, ok := normalizeSchedule(value)
	if !ok || after.IsZero() || after.Location() != time.UTC {
		return time.Time{}, false
	}
	schedule, err := cron.ParseStandard("CRON_TZ=" + value.Timezone + " " + value.Cron)
	if err != nil {
		return time.Time{}, false
	}
	next := schedule.Next(after)
	if next.IsZero() {
		return time.Time{}, false
	}
	return next.UTC(), true
}
func marshalSchedule(value Schedule) ([]byte, error) { return json.Marshal(value) }
func unmarshalSchedule(value []byte) (Schedule, error) {
	var result Schedule
	if err := json.Unmarshal(value, &result); err != nil {
		return Schedule{}, err
	}
	normalized, ok := normalizeSchedule(result)
	if !ok {
		return Schedule{}, ErrInternal
	}
	return normalized, nil
}
