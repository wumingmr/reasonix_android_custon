package agentpreset

import "testing"

func TestNormalizeTwoValueVocabulary(t *testing.T) {
	folds := []string{"", "standard", "balanced", "full", "normal", "light", "economy", "eco", "lite", "save", "saving", "low", "minimal", " STANDARD "}
	for _, raw := range folds {
		got, err := Normalize(raw)
		if err != nil || got != Standard {
			t.Errorf("Normalize(%q) = %q, %v; want standard, nil", raw, got, err)
		}
	}
	for _, raw := range []string{"delivery", "deliver", "quality", " DELIVERY "} {
		got, err := Normalize(raw)
		if err != nil || got != Standard {
			t.Errorf("Normalize(%q) = %q, %v; want retired value to fold to standard", raw, got, err)
		}
	}
	for _, raw := range []string{"turbo", "fuller"} {
		if _, err := Normalize(raw); err == nil {
			t.Errorf("Normalize(%q) must reject unknown values", raw)
		}
	}
}

func TestLegacyTokenModeRoundTrip(t *testing.T) {
	if got := LegacyTokenMode(Delivery); got != "full" {
		t.Fatalf("LegacyTokenMode(delivery) = %q, want full compatibility value", got)
	}
	if got := LegacyTokenMode(Standard); got != "full" {
		t.Fatalf("LegacyTokenMode(standard) = %q", got)
	}
	if got := FromLegacyTokenMode("economy"); got != Standard {
		t.Fatalf("FromLegacyTokenMode(economy) = %q, want standard (light folds)", got)
	}
	if got := FromLegacyTokenMode("delivery"); got != Standard {
		t.Fatalf("FromLegacyTokenMode(delivery) = %q, want standard", got)
	}
}
