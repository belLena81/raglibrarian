package application

import "testing"

func TestContentSelectionProfileDigestIncludesPolicyBounds(t *testing.T) {
	profile := validSelectionProfile(ContentSelectionEnforcement)
	baseline := profile.Digest()

	changedRanges := profile
	changedRanges.MaximumRanges--
	if changedRanges.Digest() == baseline {
		t.Fatal("maximum range policy did not change selection profile digest")
	}
	changedRatio := profile
	changedRatio.MaximumExcludedRatio = 0.2
	if changedRatio.Digest() == baseline {
		t.Fatal("maximum exclusion ratio did not change selection profile digest")
	}
}
