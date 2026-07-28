package domain

import (
	"strings"
	"testing"
)

func TestSearchRequestValidateAuthorizesOnlyActiveProductRoles(t *testing.T) {
	for _, role := range []string{"reader", "librarian", "admin"} {
		request := validRequest()
		request.Actor.Role = role
		if err := request.Validate(testRequestPolicy()); err != nil {
			t.Fatalf("role %q: %v", role, err)
		}
	}
	request := validRequest()
	request.Actor.Status = "pending"
	if err := request.Validate(testRequestPolicy()); !errorsIs(err, ErrForbidden) {
		t.Fatalf("error = %v, want forbidden", err)
	}
}

func TestSearchRequestValidateBoundsPublicInput(t *testing.T) {
	tests := []func(*SearchRequest){
		func(r *SearchRequest) {
			r.Question = strings.Repeat("a", testRequestPolicy().MaximumQuestionCharacters+1)
		},
		func(r *SearchRequest) { r.Limit = testRequestPolicy().MaximumResultLimit + 1 },
		func(r *SearchRequest) { r.CorrelationID = "not-a-request-id" },
		func(r *SearchRequest) { r.Filters.Tags = []string{""} },
		func(r *SearchRequest) { r.Filters.Author = string([]byte{0xff}) },
		func(r *SearchRequest) { r.MinimumEvidenceScore = -0.1 },
		func(r *SearchRequest) {
			year := int32(10000)
			r.Filters.YearTo = &year
		},
	}
	for index, mutate := range tests {
		request := validRequest()
		mutate(&request)
		if err := request.Validate(testRequestPolicy()); !errorsIs(err, ErrInvalidRequest) {
			t.Fatalf("case %d error = %v", index, err)
		}
	}
}

func testRequestPolicy() RequestPolicy {
	return RequestPolicy{
		MaximumQuestionCharacters: 2000,
		MaximumFilterTags:         20,
		MaximumTagCharacters:      64,
		MaximumAuthorCharacters:   256,
		MaximumResultLimit:        20,
	}
}

func validRequest() SearchRequest {
	return SearchRequest{Question: "question", Limit: 5, Actor: Actor{UserID: "user-1", Role: "reader", Status: "active"}, CorrelationID: strings.Repeat("a", 32)}
}

func errorsIs(got, want error) bool { return got == want }
