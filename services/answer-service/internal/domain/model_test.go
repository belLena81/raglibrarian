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

func TestSearchRequestNormalizeAndValidateBoundsRawInputBeforeTrimming(t *testing.T) {
	policy := testRequestPolicy()
	policy.MaximumQuestionCharacters = 8
	policy.MaximumAuthorCharacters = 6
	policy.MaximumTagCharacters = 4
	tests := []struct {
		name   string
		mutate func(*SearchRequest)
	}{
		{
			name: "question whitespace counts",
			mutate: func(request *SearchRequest) {
				request.Question = " question "
			},
		},
		{
			name: "author whitespace counts",
			mutate: func(request *SearchRequest) {
				request.Filters.Author = " author "
			},
		},
		{
			name: "tag whitespace counts",
			mutate: func(request *SearchRequest) {
				request.Filters.Tags = []string{" tag "}
			},
		},
		{
			name: "question must be valid UTF-8",
			mutate: func(request *SearchRequest) {
				request.Question = string([]byte{0xff})
			},
		},
		{
			name: "tag must be valid UTF-8",
			mutate: func(request *SearchRequest) {
				request.Filters.Tags = []string{string([]byte{0xff})}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := validRequest()
			test.mutate(&request)

			if _, err := request.NormalizeAndValidate(policy); !errorsIs(err, ErrInvalidRequest) {
				t.Fatalf("NormalizeAndValidate() error = %v, want invalid request", err)
			}
		})
	}
}

func TestSearchRequestNormalizeAndValidateReturnsNormalizedDeepCopy(t *testing.T) {
	yearFrom := int32(1990)
	yearTo := int32(2000)
	request := validRequest()
	request.Question = "  question  "
	request.Filters.Author = "  author  "
	request.Filters.Tags = []string{"  history  ", " science "}
	request.Filters.YearFrom = &yearFrom
	request.Filters.YearTo = &yearTo

	normalized, err := request.NormalizeAndValidate(testRequestPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if normalized.Question != "question" || normalized.Filters.Author != "author" ||
		len(normalized.Filters.Tags) != 2 || normalized.Filters.Tags[0] != "history" || normalized.Filters.Tags[1] != "science" {
		t.Fatalf("normalized request = %#v", normalized)
	}

	request.Filters.Tags[0] = "changed"
	yearFrom = 1980
	yearTo = 2010
	if normalized.Filters.Tags[0] != "history" || *normalized.Filters.YearFrom != 1990 || *normalized.Filters.YearTo != 2000 {
		t.Fatalf("normalized request aliases input = %#v", normalized)
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
