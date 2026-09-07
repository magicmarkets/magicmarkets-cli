package cli

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"magicmarkets-cli/internal/magicmarkets"
)

// Event IDs are comma-separated (date,tag,seq); a comma-splitting flag type
// silently mangles them into several invalid values. --event and --register
// must not split on commas.

func TestEventFlagDoesNotSplitOnCommas(t *testing.T) {
	var filter magicmarkets.OrderFilter
	cmd := &cobra.Command{Use: "test"}
	addOrderFilterFlags(cmd, &filter)

	eventID := "2026-02-23,multirunner,100364405"
	if err := cmd.Flags().Parse([]string{"--event", eventID}); err != nil {
		t.Fatalf("Parse: %v", err)
	}

	want := []string{eventID}
	if !reflect.DeepEqual(filter.EventID, want) {
		t.Errorf("EventID = %#v, want %#v", filter.EventID, want)
	}
}

func TestRegisterFlagDoesNotSplitOnCommas(t *testing.T) {
	cmd := (&App{}).newStreamCmd()

	registration := "af:2026-02-23,multirunner,100364405"
	if err := cmd.Flags().Parse([]string{"--register", registration}); err != nil {
		t.Fatalf("Parse: %v", err)
	}

	got, err := cmd.Flags().GetStringArray("register")
	if err != nil {
		t.Fatalf("GetStringArray: %v", err)
	}
	want := []string{registration}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("register = %#v, want %#v", got, want)
	}
}

// position computes one aggregate P&L and rejects a query spanning more than
// one event/sport with validation_error/non_field_errors. That's easy to hit
// by accident (position with no filter, on an account with orders on
// several events) and the bare API error doesn't say what to do about it.
func TestPositionFilterErrorAddsGuidance(t *testing.T) {
	apiErr := &magicmarkets.APIError{
		HTTPStatus: 400,
		Code:       magicmarkets.CodeValidationError,
		ValidationErrors: map[string][]string{
			"non_field_errors": {"filters match orders from multiple events"},
		},
	}

	got := positionFilterError(apiErr)
	if !errors.Is(got, apiErr) {
		t.Errorf("positionFilterError should wrap the original error")
	}
	if !strings.Contains(got.Error(), "--event") {
		t.Errorf("positionFilterError() = %q, want it to mention --event", got.Error())
	}
}

func TestPositionFilterErrorLeavesOtherErrorsAlone(t *testing.T) {
	apiErr := &magicmarkets.APIError{
		HTTPStatus: 400,
		Code:       magicmarkets.CodeValidationError,
		ValidationErrors: map[string][]string{
			"page_size": {"must be at most 1000"},
		},
	}

	got := positionFilterError(apiErr)
	if got != apiErr {
		t.Errorf("positionFilterError() = %v, want the original error unchanged", got)
	}
}
