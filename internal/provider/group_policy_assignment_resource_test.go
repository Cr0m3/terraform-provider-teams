package provider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/terraprovider/go-msadmin/consistency"
	"github.com/terraprovider/go-teams/teamsapi"
)

func TestGroupPolicyAssignmentGrantRequests(t *testing.T) {
	for _, operation := range []string{"assign", "unassign"} {
		t.Run(operation, func(t *testing.T) {
			for _, status := range []int{
				http.StatusAccepted,
				http.StatusNoContent,
				http.StatusBadRequest,
				http.StatusForbidden,
				http.StatusNotFound,
				http.StatusConflict,
				http.StatusInternalServerError,
			} {
				t.Run(http.StatusText(status), func(t *testing.T) {
					var requests int
					client := fakeTeamsClient(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
						if req.URL.Path == "/Teams.Tenant/serviceDiscovery" {
							_, _ = w.Write([]byte(`{"Endpoints":{"AdminServiceEndpoint":"admin.example"},"Headers":{}}`))
							return
						}
						requests++
						if req.Method != http.MethodPost {
							t.Errorf("method = %s, want POST (PATCH cannot create a group assignment)", req.Method)
						}
						wantPath := fakeAssignmentsPath + fakeGroupA + "/policyTypes/" + fakePolicyType
						if req.URL.Path != wantPath || req.URL.RawQuery != "" {
							t.Errorf("URL = %s, want %s", req.URL, wantPath)
						}
						var body map[string]any
						if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
							t.Errorf("decode request: %v", err)
							w.WriteHeader(http.StatusBadRequest)
							return
						}
						wantBody := map[string]any{"policyName": nil}
						if operation == "assign" {
							wantBody = map[string]any{"policyName": "Kiosk", "priority": float64(2)}
						}
						if !reflect.DeepEqual(body, wantBody) {
							t.Errorf("body = %#v, want %#v", body, wantBody)
						}
						w.WriteHeader(status)
						if status >= 400 {
							_, _ = w.Write([]byte(`{"error":{"code":"test","message":"write rejected"}}`))
						}
					}))
					r := &groupPolicyAssignmentResource{client: client}
					var err error
					if operation == "assign" {
						err = r.grant(context.Background(), groupPolicyAssignmentModel{
							GroupID:    types.StringValue(fakeGroupA),
							PolicyType: types.StringValue(fakePolicyType),
							PolicyName: types.StringValue("Kiosk"),
							Rank:       types.Int64Value(2),
						})
					} else {
						err = r.unassign(context.Background(), fakeGroupA, fakePolicyType)
					}
					if status < 400 {
						if err != nil {
							t.Fatalf("unexpected error: %v", err)
						}
					} else {
						var apiErr *teamsapi.APIError
						if !errors.As(err, &apiErr) || apiErr.Status != status {
							t.Fatalf("error = %v, want APIError with status %d", err, status)
						}
					}
					if requests != 1 {
						t.Errorf("sent %d write requests, want exactly one with no fallback", requests)
					}
				})
			}
		})
	}
}

func TestGroupPolicyAssignmentSchema(t *testing.T) {
	var resp resource.SchemaResponse
	NewGroupPolicyAssignmentResource().Schema(context.Background(), resource.SchemaRequest{}, &resp)

	for _, name := range []string{"group_id", "policy_type", "policy_name", "rank"} {
		if !resp.Schema.Attributes[name].IsRequired() {
			t.Errorf("%s must be required", name)
		}
	}
	if !resp.Schema.Attributes["id"].IsComputed() {
		t.Error("id must be computed")
	}
	if _, ok := resp.Schema.Attributes["rank"].(schema.Int64Attribute); !ok {
		t.Fatal("rank must use the Terraform integer type")
	}
}

func TestGroupPolicyAssignmentValidators(t *testing.T) {
	ctx := context.Background()
	stringCases := []struct {
		name      string
		validator validator.String
		value     string
		wantError bool
	}{
		{"valid group id", groupIDValidator{}, "d8ebfa45-0f28-4d2d-9bcc-b158a49e2d17", false},
		{"invalid group id", groupIDValidator{}, "not-a-guid", true},
		{"supported policy type", groupPolicyTypeValidator{}, "TeamsMeetingPolicy", false},
		{"unsupported policy type", groupPolicyTypeValidator{}, "TeamsUpgradePolicy", true},
		{"non-empty policy name", nonEmptyPolicyNameValidator{}, "RetailManagers", false},
		{"empty policy name", nonEmptyPolicyNameValidator{}, "   ", true},
	}
	for _, tc := range stringCases {
		t.Run(tc.name, func(t *testing.T) {
			var resp validator.StringResponse
			tc.validator.ValidateString(ctx, validator.StringRequest{ConfigValue: types.StringValue(tc.value)}, &resp)
			if got := resp.Diagnostics.HasError(); got != tc.wantError {
				t.Fatalf("diagnostics.HasError() = %t, want %t", got, tc.wantError)
			}
		})
	}

	rankCases := []struct {
		name      string
		value     int64
		wantError bool
	}{
		{"first rank", 1, false},
		{"positive rank", 4, false},
		{"maximum rank", maxGroupPolicyRank, false},
		{"zero rank", 0, true},
		{"negative rank", -1, true},
		{"rank overflow", maxGroupPolicyRank + 1, true},
	}
	for _, tc := range rankCases {
		t.Run(tc.name, func(t *testing.T) {
			var resp validator.Int64Response
			positiveRankValidator{}.ValidateInt64(ctx, validator.Int64Request{ConfigValue: types.Int64Value(tc.value)}, &resp)
			if got := resp.Diagnostics.HasError(); got != tc.wantError {
				t.Fatalf("diagnostics.HasError() = %t, want %t", got, tc.wantError)
			}
		})
	}
}

func TestNormalizeGroupPolicyName(t *testing.T) {
	for _, tc := range []struct {
		in, want string
	}{
		{"RetailManagers", "RetailManagers"},
		{"Tag:RetailManagers", "RetailManagers"},
		{"tag:RetailManagers", "RetailManagers"},
	} {
		if got := normalizeGroupPolicyName(tc.in); got != tc.want {
			t.Errorf("normalizeGroupPolicyName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestDecodeGroupPolicyAssignment(t *testing.T) {
	const groupID = "d8ebfa45-0f28-4d2d-9bcc-b158a49e2d17"
	rows := []map[string]any{
		{"GroupId": "other", "PolicyType": "TeamsMeetingPolicy", "PolicyName": "Other", "Rank": float64(1)},
		{"groupId": strings.ToUpper(groupID), "policyType": "teamsmeetingpolicy", "policyName": "Tag:RetailManagers", "rank": float64(2)},
	}
	got, err := decodeGroupPolicyAssignment(rows, groupID, "TeamsMeetingPolicy")
	if err != nil {
		t.Fatalf("decodeGroupPolicyAssignment: %v", err)
	}
	if got.GroupID != groupID || got.PolicyType != "TeamsMeetingPolicy" || got.PolicyName != "RetailManagers" || got.Rank != 2 {
		t.Fatalf("decoded assignment = %+v", got)
	}
}

func TestDecodeGroupPolicyAssignmentPriority(t *testing.T) {
	for _, tc := range []struct {
		name      string
		key       string
		value     any
		wantRank  int64
		wantError bool
	}{
		{"live priority", "priority", float64(1), 1, false},
		{"priority is not zero based", "priority", float64(2), 2, false},
		{"numeric string", "priority", "2", 2, false},
		{"case insensitive", "Priority", float64(2), 2, false},
		{"legacy Rank", "Rank", float64(2), 2, false},
		{"null priority", "priority", nil, 0, true},
		{"zero priority", "priority", float64(0), 0, true},
		{"negative priority", "priority", float64(-1), 0, true},
		{"fractional priority", "priority", float64(1.5), 0, true},
		{"invalid string", "priority", "not-a-rank", 0, true},
		{"boolean priority", "priority", true, 0, true},
		{"overflow", "priority", float64(maxGroupPolicyRank + 1), 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Same response shape as the live service, with synthetic identities.
			row := map[string]any{
				"groupId":     fakeGroupA,
				"policyType":  fakePolicyType,
				"policyName":  "Kiosk",
				"createdTime": "2026-01-01T00:00:00Z",
				"createdBy":   fakeGroupB,
				tc.key:        tc.value,
			}
			got, err := decodeGroupPolicyAssignment([]map[string]any{row}, fakeGroupA, fakePolicyType)
			if tc.wantError {
				if err == nil {
					t.Fatalf("expected an invalid rank error, got %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if got == nil || got.Rank != tc.wantRank || got.PolicyName != "Kiosk" {
				t.Fatalf("assignment = %+v, want policy Kiosk at rank %d", got, tc.wantRank)
			}
		})
	}
}

func TestDecodeGroupPolicyAssignmentErrors(t *testing.T) {
	const groupID = "d8ebfa45-0f28-4d2d-9bcc-b158a49e2d17"
	cases := []struct {
		name string
		rows []map[string]any
		want string
	}{
		{"missing assignment", nil, ""},
		{"malformed row", []map[string]any{{}}, "without GroupId or PolicyType"},
		{"missing policy name", []map[string]any{{"GroupId": groupID, "PolicyType": "TeamsMeetingPolicy", "priority": float64(1)}}, "valid policy name"},
		{"missing rank", []map[string]any{{"GroupId": groupID, "PolicyType": "TeamsMeetingPolicy", "PolicyName": "Kiosk"}}, "valid priority (rank)"},
		{"invalid rank", []map[string]any{{"GroupId": groupID, "PolicyType": "TeamsMeetingPolicy", "PolicyName": "Kiosk", "Rank": float64(1.5)}}, "valid priority (rank)"},
		{"duplicate assignment", []map[string]any{
			{"GroupId": groupID, "PolicyType": "TeamsMeetingPolicy", "PolicyName": "First", "Rank": float64(1)},
			{"GroupId": groupID, "PolicyType": "TeamsMeetingPolicy", "PolicyName": "Second", "Rank": float64(2)},
		}, "multiple assignments"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := decodeGroupPolicyAssignment(tc.rows, groupID, "TeamsMeetingPolicy")
			if tc.want == "" {
				if err != nil || got != nil {
					t.Fatalf("got (%+v, %v), want (nil, nil)", got, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestGroupPolicyAssignmentRegistered(t *testing.T) {
	p := &teamsProvider{}
	found := false
	for _, newResource := range p.Resources(context.Background()) {
		r := newResource()
		var metadata resource.MetadataResponse
		r.Metadata(context.Background(), resource.MetadataRequest{ProviderTypeName: "teams"}, &metadata)
		if metadata.TypeName == "teams_group_policy_assignment" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("teams_group_policy_assignment is not registered")
	}
}

// Regression: Delete used to wait with a getter whose "present" flag and
// predicate contradicted each other, so it always spent the full retry budget
// and reported success even when the assignment was never removed.
func TestGroupPolicyAssignmentWaitForRemoval(t *testing.T) {
	cfg := consistency.Config{Attempts: 3, Delay: time.Millisecond}
	for _, tc := range []struct {
		name      string
		present   bool
		wantErr   bool
		wantReads int
	}{
		{name: "removed", present: false, wantErr: false, wantReads: 1},
		{name: "never removed", present: true, wantErr: true, wantReads: cfg.Attempts},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var reads int
			client := fakeTeamsClient(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if req.URL.Path == "/Teams.Tenant/serviceDiscovery" {
					_, _ = w.Write([]byte(`{"Endpoints":{"AdminServiceEndpoint":"admin.example"},"Headers":{}}`))
					return
				}
				reads++
				rows := []map[string]any{}
				if tc.present {
					rows = append(rows, map[string]any{"groupId": fakeGroupA, "policyType": fakePolicyType, "policyName": "Kiosk", "priority": 1})
				}
				_ = json.NewEncoder(w).Encode(rows)
			}))
			r := &groupPolicyAssignmentResource{client: client}
			err := r.waitForRemoval(context.Background(), fakeGroupA, fakePolicyType, cfg)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if reads != tc.wantReads {
				t.Errorf("reads = %d, want %d", reads, tc.wantReads)
			}
		})
	}
}
