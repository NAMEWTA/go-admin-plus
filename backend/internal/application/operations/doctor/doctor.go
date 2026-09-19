// Package doctor runs bounded, read-only operational checks and returns a secret-free report.
package doctor

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"
	"unicode"
)

type CheckName string

const (
	CheckConfiguration   CheckName = "configuration"
	CheckSecretReference CheckName = "secret-reference"
	CheckDatabase        CheckName = "database"
	CheckSchema          CheckName = "schema"
	CheckSetup           CheckName = "setup"
	CheckFilesRoot       CheckName = "files-root"
	CheckDisk            CheckName = "disk"
	CheckWorker          CheckName = "worker"
	CheckVersion         CheckName = "version"
)

type Class string

const (
	ClassNone                 Class = "none"
	ClassInvalidConfiguration Class = "invalid-configuration"
	ClassUnavailable          Class = "unavailable"
	ClassTimeout              Class = "timeout"
	ClassIncompatible         Class = "incompatible"
	ClassNotConfigured        Class = "not-configured"
)

type Status string

const (
	StatusPassed  Status = "passed"
	StatusWarning Status = "warning"
	StatusFailed  Status = "failed"
)

type ExitClass string

const (
	ExitHealthy     ExitClass = "healthy"
	ExitDegraded    ExitClass = "degraded"
	ExitUnavailable ExitClass = "unavailable"
	ExitInvalid     ExitClass = "invalid-configuration"
)

type Config struct {
	Profile string
	Version string
	Timeout time.Duration
}

type Check struct {
	Name         CheckName
	Critical     bool
	Timeout      time.Duration
	FailureClass Class
	Run          func(context.Context) error
}

type Result struct {
	Name       CheckName `json:"name"`
	Status     Status    `json:"status"`
	Class      Class     `json:"class"`
	DurationMS int64     `json:"durationMs"`
}

type Report struct {
	Profile string    `json:"profile"`
	Version string    `json:"version"`
	Exit    ExitClass `json:"exit"`
	Checks  []Result  `json:"checks"`
}

type Runner struct {
	config Config
	checks []Check
}

func New(config Config, checks ...Check) (*Runner, error) {
	if !validProfile(config.Profile) || !validVersion(config.Version) || config.Timeout < 10*time.Millisecond || config.Timeout > time.Minute || len(checks) == 0 {
		return nil, errors.New("doctor configuration is invalid")
	}
	owned := append([]Check(nil), checks...)
	seen := make(map[CheckName]struct{}, len(owned))
	for index := range owned {
		check := &owned[index]
		if !validCheckName(check.Name) || check.Run == nil {
			return nil, errors.New("doctor check is invalid")
		}
		if _, duplicate := seen[check.Name]; duplicate {
			return nil, errors.New("doctor check is duplicated")
		}
		seen[check.Name] = struct{}{}
		if check.Timeout == 0 {
			check.Timeout = config.Timeout
		}
		if check.Timeout < 10*time.Millisecond || check.Timeout > config.Timeout {
			return nil, errors.New("doctor check timeout is invalid")
		}
		if check.FailureClass == "" {
			check.FailureClass = ClassUnavailable
		}
		if !validClass(check.FailureClass) || check.FailureClass == ClassNone || check.FailureClass == ClassTimeout {
			return nil, errors.New("doctor check failure class is invalid")
		}
	}
	return &Runner{config: config, checks: owned}, nil
}

func (runner *Runner) Run(ctx context.Context) Report {
	if ctx == nil {
		ctx = context.Background()
	}
	overall, cancel := context.WithTimeout(ctx, runner.config.Timeout)
	defer cancel()
	type indexedResult struct {
		index  int
		result Result
	}
	results := make(chan indexedResult, len(runner.checks))
	for index, check := range runner.checks {
		go func(index int, check Check) {
			results <- indexedResult{index: index, result: runCheck(overall, check)}
		}(index, check)
	}
	ordered := make([]Result, len(runner.checks))
	for range runner.checks {
		value := <-results
		ordered[value.index] = value.result
	}
	exit := ExitHealthy
	for index, result := range ordered {
		if result.Status == StatusPassed {
			continue
		}
		check := runner.checks[index]
		if !check.Critical {
			if exit == ExitHealthy {
				exit = ExitDegraded
			}
			continue
		}
		if result.Class == ClassInvalidConfiguration {
			exit = ExitInvalid
			continue
		}
		if exit != ExitInvalid {
			exit = ExitUnavailable
		}
	}
	return Report{Profile: runner.config.Profile, Version: runner.config.Version, Exit: exit, Checks: ordered}
}

func runCheck(parent context.Context, check Check) Result {
	started := time.Now()
	ctx, cancel := context.WithTimeout(parent, check.Timeout)
	defer cancel()
	completed := make(chan error, 1)
	go func() { completed <- check.Run(ctx) }()
	class := ClassNone
	status := StatusPassed
	select {
	case err := <-completed:
		if err != nil {
			class = check.FailureClass
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) && ctx.Err() != nil {
				class = ClassTimeout
			}
			status = StatusFailed
			if !check.Critical {
				status = StatusWarning
			}
		}
	case <-ctx.Done():
		class = ClassTimeout
		status = StatusFailed
		if !check.Critical {
			status = StatusWarning
		}
	}
	duration := time.Since(started).Milliseconds()
	return Result{Name: check.Name, Status: status, Class: class, DurationMS: duration}
}

func validProfile(profile string) bool {
	return profile == "server-postgres" || profile == "server-sqlite" || profile == "desktop-sqlite"
}

func validVersion(value string) bool {
	if len(value) < 1 || len(value) > 128 || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if !unicode.IsLetter(character) && !unicode.IsDigit(character) && character != '-' && character != '_' && character != '.' && character != '+' {
			return false
		}
	}
	return true
}

func validCheckName(name CheckName) bool {
	index := sort.SearchStrings(knownCheckNames, string(name))
	return index < len(knownCheckNames) && knownCheckNames[index] == string(name)
}

var knownCheckNames = []string{
	string(CheckConfiguration), string(CheckDatabase), string(CheckDisk), string(CheckFilesRoot), string(CheckSchema),
	string(CheckSecretReference), string(CheckSetup), string(CheckVersion), string(CheckWorker),
}

func validClass(class Class) bool {
	switch class {
	case ClassNone, ClassInvalidConfiguration, ClassUnavailable, ClassTimeout, ClassIncompatible, ClassNotConfigured:
		return true
	default:
		return false
	}
}
