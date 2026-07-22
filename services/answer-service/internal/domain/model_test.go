package domain

import (
	"strings"
	"testing"
)

func TestSearchRequestValidateAuthorizesOnlyActiveProductRoles(t *testing.T) {
	for _, role := range []string{"reader", "librarian", "admin"} {
		request := validRequest()
		request.Actor.Role = role
		if err := request.Validate(); err != nil {
			t.Fatalf("role %q: %v", role, err)
		}
	}
	request := validRequest()
	request.Actor.Status = "pending"
	if err := request.Validate(); !errorsIs(err, ErrForbidden) {
		t.Fatalf("error = %v, want forbidden", err)
	}
}

func TestSearchRequestValidateBoundsPublicInput(t *testing.T) {
	tests := []func(*SearchRequest){
		func(r *SearchRequest) { r.Question = strings.Repeat("a", MaximumQuestionCharacters+1) },
		func(r *SearchRequest) { r.Limit = MaximumResultLimit + 1 },
		func(r *SearchRequest) { r.CorrelationID = "not-a-request-id" },
		func(r *SearchRequest) { r.Filters.Tags = []string{""} },
		func(r *SearchRequest) { r.Filters.Author = string([]byte{0xff}) },
		func(r *SearchRequest) {
			year := int32(10000)
			r.Filters.YearTo = &year
		},
	}
	for index, mutate := range tests {
		request := validRequest()
		mutate(&request)
		if err := request.Validate(); !errorsIs(err, ErrInvalidRequest) {
			t.Fatalf("case %d error = %v", index, err)
		}
	}
}

func validRequest() SearchRequest {
	return SearchRequest{Question: "question", Limit: 5, Actor: Actor{UserID: "user-1", Role: "reader", Status: "active"}, CorrelationID: strings.Repeat("a", 32)}
}

func errorsIs(got, want error) bool { return got == want }
