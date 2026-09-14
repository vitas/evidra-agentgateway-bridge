package observation

import (
	"strings"
	"testing"
)

// A real operation id from a recorder directory, so the validity rule is tested against the
// shape the product actually issues rather than one invented for the test.
const realID = "EV-01M2FAS00J36FTV4EG5EJC1D7S"

func TestValidOperationID(t *testing.T) {
	valid := []string{
		realID,
		"EV-01M2EB52K0000000000000000A",
	}
	for _, id := range valid {
		if !ValidOperationID(id) {
			t.Errorf("ValidOperationID(%q) = false, want true", id)
		}
	}

	invalid := map[string]string{
		"empty":                "",
		"whitespace":           "   ",
		"no prefix":            "01M2FAS00J36FTV4EG5EJC1D7S",
		"wrong prefix":         "XX-01M2FAS00J36FTV4EG5EJC1D7S",
		"too short":            "EV-01M2FAS0",
		"too long":             "EV-01M2FAS00J36FTV4EG5EJC1D7SXX",
		"lowercase":            "ev-01m2fas00j36ftv4eg5ejc1d7s",
		"excluded Crockford I": "EV-01M2FAS00J36FTV4EG5EJC1D7I",
		"excluded Crockford L": "EV-01M2FAS00J36FTV4EG5EJC1D7L",
		"excluded Crockford O": "EV-01M2FAS00J36FTV4EG5EJC1D7O",
		"excluded Crockford U": "EV-01M2FAS00J36FTV4EG5EJC1D7U",
		"embedded space":       "EV-01M2FAS00J36FTV4EG5EJC1D7 S",
		"baggage not parsed":   "evidra.operation.id=" + realID,
		"trailing comma":       realID + ",",
	}
	for name, id := range invalid {
		if ValidOperationID(id) {
			t.Errorf("%s: ValidOperationID(%q) = true, want false", name, id)
		}
	}
}

// TestClassifyOperation covers the three states and, more importantly, refuses to invent a
// fourth. Nothing here may complete an id from another field, pick a winner between two
// conflicting values, or repair a malformed one.
func TestClassifyOperation(t *testing.T) {
	cases := []struct {
		name          string
		found         []string
		want          Correlation
		wantID        string
		wantDetailHas string
	}{
		{"nothing found", nil, Unattributed, "", "no explicit operation id"},
		{"only empty strings", []string{"", "  "}, Unattributed, "", "no explicit operation id"},
		{"one valid id", []string{realID}, Correlated, realID, ""},
		{"same id on both signals", []string{realID, realID}, Correlated, realID, ""},
		{"one signal silent", []string{"", realID}, Correlated, realID, ""},
		{
			name:          "two different valid ids",
			found:         []string{realID, "EV-01M2EB52K0000000000000000A"},
			want:          Ambiguous,
			wantID:        "",
			wantDetailHas: "conflicting operation ids",
		},
		{
			name:          "malformed id",
			found:         []string{"not-an-operation-id"},
			want:          Ambiguous,
			wantID:        "",
			wantDetailHas: "not well formed",
		},
		{
			// A whole baggage header, unparsed. Accepting it would mean the projection
			// contract lives in two places and they can disagree.
			name:          "unparsed baggage member",
			found:         []string{"evidra.operation.id=" + realID},
			want:          Ambiguous,
			wantID:        "",
			wantDetailHas: "not well formed",
		},
		{
			name:          "valid id beside a malformed one",
			found:         []string{realID, "garbage"},
			want:          Ambiguous,
			wantID:        "",
			wantDetailHas: "conflicting",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, id, detail := ClassifyOperation(tc.found...)
			if got != tc.want {
				t.Errorf("correlation = %s, want %s (detail %q)", got, tc.want, detail)
			}
			if id != tc.wantID {
				t.Errorf("operation id = %q, want %q", id, tc.wantID)
			}
			if tc.want != Correlated && id != "" {
				t.Errorf("a %s classification must not carry an operation id, got %q", got, id)
			}
			if tc.wantDetailHas != "" && !strings.Contains(detail, tc.wantDetailHas) {
				t.Errorf("detail %q does not explain itself (want it to mention %q)", detail, tc.wantDetailHas)
			}
		})
	}
}

func TestClassifyOperationTrimsButDoesNotRepair(t *testing.T) {
	got, id, _ := ClassifyOperation("  " + realID + "  ")
	if got != Correlated || id != realID {
		t.Errorf("surrounding whitespace should be trimmed, got %s/%q", got, id)
	}
	// Interior damage is not repaired: that would be guessing at an id.
	if got, _, _ := ClassifyOperation(realID[:10] + " " + realID[10:]); got != Ambiguous {
		t.Errorf("an id with an interior space classified as %s, want ambiguous", got)
	}
}

func TestJoinKeyRequiresBothHalves(t *testing.T) {
	if k := JoinKey("abc", "def"); k == "" {
		t.Error("a complete pair produced no join key")
	}
	// Joining on trace id alone would be a heuristic: one trace normally carries several tool
	// calls, so an operation id would land on whichever execution merged first.
	for _, tc := range [][2]string{{"", "def"}, {"abc", ""}, {"", ""}, {"  ", "def"}} {
		if k := JoinKey(tc[0], tc[1]); k != "" {
			t.Errorf("JoinKey(%q, %q) = %q, want empty", tc[0], tc[1], k)
		}
	}
}
