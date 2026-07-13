package scripts

import (
	"context"
	"strconv"
	"strings"
	"time"

	domainautomation "github.com/lyming99/autoplan/backend/internal/domain/automation"
)

type ScheduleCommand struct {
	Caller    Caller
	ProjectID int64
	RequestID string
	Now       time.Time
	Context   Context
}

type ScheduleResult struct {
	ScriptID  int64
	Operation string
	Status    string
}

// RunDue evaluates persisted schedule definitions once for one project. It
// neither accepts a client cron expression nor starts a process directly.
// The local minute marker covers the interval between a Go-owned run starting
// and its later durable last_run_at archive in P005.
func (service *Service) RunDue(ctx context.Context, command ScheduleCommand) ([]ScheduleResult, error) {
	if err := service.ready(ctx); err != nil {
		return nil, err
	}
	if !validCaller(command.Caller, command.ProjectID) || !validIdentity(command.RequestID, 64) || !command.Context.Valid() {
		return nil, ErrInvalidCommand
	}
	now := command.Now
	if now.IsZero() {
		now = service.clock.Now()
	}
	now = now.In(time.Local)
	scripts, err := service.listAllScripts(ctx, command.ProjectID)
	if err != nil {
		return nil, err
	}
	result := make([]ScheduleResult, 0)
	for _, script := range scripts {
		if !due(script, now) || service.scheduledThisMinute(command.ProjectID, script.ID, now) {
			continue
		}
		identity := scheduleIdentity(command.RequestID, script.ID, now)
		run, runErr := service.RunScheduled(ctx, RunCommand{
			Caller: command.Caller, ProjectID: command.ProjectID, ScriptID: script.ID,
			RequestID: identity, IdempotencyKey: identity, Context: command.Context,
		})
		if runErr != nil {
			result = append(result, ScheduleResult{ScriptID: script.ID, Status: "rejected"})
			continue
		}
		service.markScheduled(command.ProjectID, script.ID, now)
		result = append(result, ScheduleResult{ScriptID: script.ID, Operation: run.Operation.OperationID, Status: "accepted"})
	}
	return result, nil
}

func due(script domainautomation.Script, now time.Time) bool {
	if !script.Enabled || script.TriggerMode != string(TriggerSchedule) || script.ScheduleCron == nil {
		return false
	}
	parsed, err := parseCron(*script.ScheduleCron)
	if err != nil || !parsed.due(now) {
		return false
	}
	return !sameMinute(script.LastRunAt, now)
}

func (service *Service) scheduledThisMinute(projectID, scriptID int64, now time.Time) bool {
	key := scriptKey{projectID: projectID, scriptID: scriptID}
	minute := minuteKey(now)
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.scheduled[key] == minute {
		return true
	}
	if last, found := service.last[key]; found && sameMinute(&last.ranAt, now) {
		return true
	}
	return false
}

func (service *Service) markScheduled(projectID, scriptID int64, now time.Time) {
	service.mu.Lock()
	service.scheduled[scriptKey{projectID: projectID, scriptID: scriptID}] = minuteKey(now)
	service.mu.Unlock()
}

func scheduleIdentity(requestID string, scriptID int64, now time.Time) string {
	return derivedIdentity("schedule", requestID, scriptID, minuteKey(now))
}

func minuteKey(value time.Time) string { return value.In(time.Local).Format("200601021504") }

func sameMinute(value *string, now time.Time) bool {
	if value == nil || strings.TrimSpace(*value) == "" {
		return false
	}
	parsed, err := time.Parse(time.RFC3339Nano, *value)
	if err != nil {
		return false
	}
	return minuteKey(parsed) == minuteKey(now)
}

type cronExpression struct {
	minute     map[int]struct{}
	hour       map[int]struct{}
	dayOfMonth map[int]struct{}
	month      map[int]struct{}
	dayOfWeek  map[int]struct{}
}

func parseCron(value string) (cronExpression, error) {
	fields := strings.Fields(strings.TrimSpace(value))
	if len(fields) != 5 {
		return cronExpression{}, ErrInvalidCommand
	}
	minute, err := parseCronField(fields[0], 0, 59, false)
	if err != nil {
		return cronExpression{}, err
	}
	hour, err := parseCronField(fields[1], 0, 23, false)
	if err != nil {
		return cronExpression{}, err
	}
	dayOfMonth, err := parseCronField(fields[2], 1, 31, false)
	if err != nil {
		return cronExpression{}, err
	}
	month, err := parseCronField(fields[3], 1, 12, false)
	if err != nil {
		return cronExpression{}, err
	}
	dayOfWeek, err := parseCronField(fields[4], 0, 7, true)
	if err != nil {
		return cronExpression{}, err
	}
	return cronExpression{minute: minute, hour: hour, dayOfMonth: dayOfMonth, month: month, dayOfWeek: dayOfWeek}, nil
}

func parseCronField(value string, minimum, maximum int, sunday bool) (map[int]struct{}, error) {
	result := make(map[int]struct{})
	for _, item := range strings.Split(value, ",") {
		if item == "" {
			return nil, ErrInvalidCommand
		}
		base, step := item, 1
		if before, after, found := strings.Cut(item, "/"); found {
			base = before
			parsed, err := strconv.Atoi(after)
			if err != nil || parsed <= 0 {
				return nil, ErrInvalidCommand
			}
			step = parsed
		}
		low, high, err := cronBounds(base, minimum, maximum)
		if err != nil || low > high {
			return nil, ErrInvalidCommand
		}
		for number := low; number <= high; number += step {
			normalized := number
			if sunday && normalized == 7 {
				normalized = 0
			}
			result[normalized] = struct{}{}
		}
	}
	if len(result) == 0 {
		return nil, ErrInvalidCommand
	}
	return result, nil
}

func cronBounds(value string, minimum, maximum int) (int, int, error) {
	if value == "*" {
		return minimum, maximum, nil
	}
	if first, last, found := strings.Cut(value, "-"); found {
		low, lowErr := strconv.Atoi(first)
		high, highErr := strconv.Atoi(last)
		if lowErr != nil || highErr != nil || low < minimum || high > maximum {
			return 0, 0, ErrInvalidCommand
		}
		return low, high, nil
	}
	number, err := strconv.Atoi(value)
	if err != nil || number < minimum || number > maximum {
		return 0, 0, ErrInvalidCommand
	}
	return number, number, nil
}

func (value cronExpression) due(now time.Time) bool {
	_, minute := value.minute[now.Minute()]
	_, hour := value.hour[now.Hour()]
	_, dayOfMonth := value.dayOfMonth[now.Day()]
	_, month := value.month[int(now.Month())]
	_, dayOfWeek := value.dayOfWeek[int(now.Weekday())]
	return minute && hour && dayOfMonth && month && dayOfWeek
}
