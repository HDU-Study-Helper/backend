package main

import (
	"fmt"
	"time"
)

type collectionOptions struct {
	timeout  time.Duration
	interval time.Duration
}

func parseCollectionOptions(getenv func(string) string) (collectionOptions, error) {
	options := collectionOptions{timeout: 30 * time.Minute, interval: 3 * time.Second}
	for _, setting := range []struct {
		name string
		dest *time.Duration
		min  time.Duration
		max  time.Duration
	}{
		{"ACADEMIC_COLLECTION_TIMEOUT", &options.timeout, 1, 4 * time.Hour},
		{"ACADEMIC_REQUEST_INTERVAL", &options.interval, 500 * time.Millisecond, 30 * time.Second},
	} {
		value := getenv(setting.name)
		if value == "" {
			continue
		}
		duration, err := time.ParseDuration(value)
		if err != nil || duration < setting.min || duration > setting.max {
			return collectionOptions{}, fmt.Errorf("%s must be a duration between %s and %s", setting.name, setting.min, setting.max)
		}
		*setting.dest = duration
	}
	return options, nil
}
