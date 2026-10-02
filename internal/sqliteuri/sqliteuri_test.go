package sqliteuri

import (
	"net/url"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiskUsesAbsoluteEncodedPathAndSeparateQuery(t *testing.T) {
	query := url.Values{"mode": {"ro"}, "_pragma": {"busy_timeout(5000)", "foreign_keys(1)"}}
	dsn, err := Disk(filepath.Join("relative dir", "会话%23#.sqlite"), query)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	wantSuffix := filepath.ToSlash(filepath.Join("relative dir", "会话%23#.sqlite"))
	if !strings.HasSuffix(u.Path, wantSuffix) || !strings.HasPrefix(u.Path, "/") {
		t.Fatalf("decoded path = %q, want absolute suffix %q", u.Path, wantSuffix)
	}
	if u.Query().Get("mode") != "ro" || len(u.Query()["_pragma"]) != 2 {
		t.Fatalf("query = %#v", u.Query())
	}
	if strings.Contains(strings.SplitN(dsn, "?", 2)[0], "#") || !strings.Contains(dsn, "%25") || !strings.Contains(dsn, "%23") {
		t.Fatalf("path is not URI encoded: %s", dsn)
	}
}

func TestDiskRejectsBlankPath(t *testing.T) {
	if _, err := Disk(" \t", nil); err == nil {
		t.Fatal("Disk accepted a blank path")
	}
}

func TestDiskWindowsDriveAndUNCForms(t *testing.T) {
	tests := []struct {
		name string
		path string
		want string
	}{
		{name: "forward drive", path: `C:/Users/Test User/会话%23#.sqlite`, want: `file:///C:/Users/Test%20User/%E4%BC%9A%E8%AF%9D%2523%23.sqlite`},
		{name: "backslash drive", path: `C:\Users\Test User\data.sqlite`, want: `file:///C:/Users/Test%20User/data.sqlite`},
		{name: "UNC", path: `\\server\share\Test User\data.sqlite`, want: `file:////server/share/Test%20User/data.sqlite`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := disk(tt.path, nil, "windows"); got != tt.want {
				t.Fatalf("disk() = %q, want %q", got, tt.want)
			}
		})
	}
}
