package localeenv

import (
	"reflect"
	"testing"
)

func TestDefaultPreservesExplicitLocale(t *testing.T) {
	for _, key := range []string{"LANG", "LC_ALL", "LC_CTYPE"} {
		for _, value := range []string{"C", "POSIX", "zh_CN.GB18030", "en_US.UTF-8"} {
			env := []string{"PATH=/bin", key + "=" + value}
			if got := withDefault(env, "C.UTF-8"); !reflect.DeepEqual(got, env) {
				t.Fatalf("changed explicit locale: %v", got)
			}
		}
	}
}

func TestDefaultOnlyFillsMissingLocale(t *testing.T) {
	env := []string{"PATH=/bin", "LANG=", "LC_ALL=", "LC_COLLATE=C"}
	want := []string{"PATH=/bin", "LC_ALL=", "LC_COLLATE=C", "LANG=C.UTF-8"}
	if got := withDefault(env, "C.UTF-8"); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
	if env[1] != "LANG=" {
		t.Fatal("mutated source environment")
	}
	if got := withDefault(env, ""); !reflect.DeepEqual(got, env) {
		t.Fatal("invented unavailable locale")
	}
}

func TestSelectInstalledUTF8(t *testing.T) {
	for input, want := range map[string]string{
		"C\nPOSIX\nen_US.UTF-8\n": "en_US.UTF-8",
		"en_US.utf8\nC.utf8\n":    "C.utf8",
		"C\nPOSIX\n":              "",
		"C\nzh_CN.UTF-8\n":        "zh_CN.UTF-8",
	} {
		if got := selectUTF8(input); got != want {
			t.Fatalf("got %q want %q", got, want)
		}
	}
}
