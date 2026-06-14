package integration

import "time"

const (
	readyTimeout       = 5 * time.Minute
	deleteTimeout      = 2 * time.Minute
	buildReadyTimeout  = 10 * time.Minute
	buildJobTimeout    = 3 * time.Minute
	imageBuildTimeout  = 2 * time.Minute
	fieldChangeTimeout = 2 * time.Minute
	stabilityWindow    = 30 * time.Second
)
