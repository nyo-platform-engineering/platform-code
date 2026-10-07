package controller

import (
	"context"
	"errors"
	"time"

	"aat/aggregator/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (ctrl *Controller) GetPollState(ctx context.Context, source string) (model.PollState, error) {
	state := model.PollState{Source: source}
	err := ctrl.DB.WithContext(ctx).Take(&state, "source = ?", source).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return model.PollState{}, err
	}

	state.LastSuccessCallerTime = utc(state.LastSuccessCallerTime)
	state.LastErrorCallerTime = utc(state.LastErrorCallerTime)
	return state, nil
}

func (ctrl *Controller) MarkPollSuccess(ctx context.Context, source string, callerTime time.Time) error {
	// Save the caller's start time only after the poll succeeds.
	values := map[string]any{
		"source":          source,
		"healthy":         true,
		"last_success_at": callerTime.UTC(),
		"last_error_at":   nil,
		"last_error":      nil,
	}
	updateExisting := clause.OnConflict{
		Columns: []clause.Column{{Name: "source"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"healthy", "last_success_at", "last_error_at", "last_error",
		}),
	}
	return ctrl.DB.WithContext(ctx).
		Model(&model.PollState{}).
		Clauses(updateExisting).
		Create(values).Error
}

func (ctrl *Controller) MarkPollFailure(ctx context.Context, source string, callerTime time.Time, message string) error {
	// Update health and error details while keeping the last successful caller time.
	values := map[string]any{
		"source":        source,
		"healthy":       false,
		"last_error_at": callerTime.UTC(),
		"last_error":    message,
	}
	updateExisting := clause.OnConflict{
		Columns: []clause.Column{{Name: "source"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"healthy", "last_error_at", "last_error",
		}),
	}
	return ctrl.DB.WithContext(ctx).
		Model(&model.PollState{}).
		Clauses(updateExisting).
		Create(values).Error
}

func (ctrl *Controller) ListSourceStatuses(ctx context.Context) ([]model.PollState, error) {
	// Read health and the caller time of the last successful poll.
	var savedStates []model.PollState
	err := ctrl.DB.WithContext(ctx).
		Select("source", "healthy", "last_success_at").
		Where("source IN ?", []string{"BMKG", "PVMBG"}).
		Find(&savedStates).Error
	if err != nil {
		return nil, err
	}

	bySource := make(map[string]model.PollState, len(savedStates))
	for _, state := range savedStates {
		state.LastSuccessCallerTime = utc(state.LastSuccessCallerTime)
		bySource[state.Source] = state
	}

	// Include both sources, even when neither has completed its first poll.
	statuses := make([]model.PollState, 0, 2)
	for _, source := range []string{"BMKG", "PVMBG"} {
		state, exists := bySource[source]
		if !exists {
			state = model.PollState{Source: source}
		}
		statuses = append(statuses, state)
	}
	return statuses, nil
}

func utc(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	converted := value.UTC()
	return &converted
}
