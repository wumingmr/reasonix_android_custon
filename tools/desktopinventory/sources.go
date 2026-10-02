package main

import (
	"fmt"
	"regexp"
	"strings"
)

var artifactRe = regexp.MustCompile(`^#\s+(?:macOS|Windows|Linux)?:?\s*(Reasonix-[a-z]+-<arch>[^\s]*)\s+\((.*)\)`)

var ciJobs = map[string]struct {
	class class
	owner string
}{
	"ci.yml/desktop":                                 {classKeepBusiness, "aggregate gate, unchanged"},
	"ci.yml/desktop-frontend":                        {classKeepBusiness, "React gates unchanged"},
	"ci.yml/desktop-browser":                         {classKeepBusiness, "Playwright browser gates unchanged"},
	"ci.yml/desktop-browser-group":                   {classKeepBusiness, "Playwright browser gates split into bounded groups"},
	"ci.yml/desktop-prepare":                         {classKeepBusiness, "go run . -emit-contract drift gate; pnpm workspace root"},
	"ci.yml/desktop-go":                              {classKeepBusiness, "hostrpc + module tests; no WebKitGTK toolchain"},
	"ci.yml/desktop-go-race":                         {classKeepBusiness, "desktop module race sweep, split from desktop-go"},
	"ci.yml/desktop-macos":                           {classKeepBusiness, "Electron packaging smoke"},
	"ci.yml/desktop-windows":                         {classKeepBusiness, "Electron packaging smoke"},
	"ci.yml/desktop-windows-package":                 {classKeepBusiness, "Electron installer build, split from the test leg"},
	"ci.yml/desktop-windows-go":                      {classKeepBusiness, "fail-closed aggregate for Windows desktop Go partitions"},
	"ci.yml/desktop-windows-go-group":                {classKeepBusiness, "isolated parallel Windows desktop Go test partitions"},
	"app-memory.yml/app-memory":                      {classKeepBusiness, "browser memory screening unchanged"},
	"app-memory.yml/prepare":                         {classKeepBusiness, "browser memory screening unchanged"},
	"app-memory.yml/shard":                           {classKeepBusiness, "browser memory screening unchanged"},
	"app-memory.yml/changes":                         {classKeepBusiness, "path filter unchanged"},
	"release-desktop.yml/resolve":                    {classKeepBusiness, "version/channel resolution unchanged"},
	"release-desktop.yml/orchestration-guard":        {classKeepBusiness, "unchanged"},
	"release-desktop.yml/release-gate":               {classKeepBusiness, "unchanged"},
	"release-desktop.yml/signing-contract":           {classKeepBusiness, "payload list covers the Electron executables and native modules"},
	"release-desktop.yml/cache-guard":                {classKeepBusiness, "unchanged"},
	"release-desktop.yml/build":                      {classKeepBusiness, "desktop-build.sh packages the Electron app with the same NSIS/nfpm/signing steps"},
	"release-desktop.yml/windows-build":              {classKeepBusiness, "builds and smoke-tests unsigned x64 and ARM64 packages on native Windows runners before signing"},
	"release-desktop.yml/windows-sign":               {classKeepBusiness, "Certum signs native-tested x64 and ARM64 payloads and rebuilt installers on x64"},
	"release-desktop.yml/windows-runtime-acceptance": {classKeepBusiness, "installs and starts the exact signed x64 and ARM64 installers on native runners"},
	"release-desktop.yml/mac-universal-intel":        {classKeepBusiness, "validates the exact universal DMG from the build matrix on an Intel runner"},
	"release-desktop.yml/publish":                    {classKeepBusiness, "manifest, minisign and mirror unchanged"},
	"release-desktop.yml/attest-signing-contract":    {classKeepBusiness, "attests the extended payload list"},
	"release-desktop.yml/mirror":                     {classKeepBusiness, "unchanged"},
}

var ciJobRe = regexp.MustCompile(`(?m)^  ([a-z][a-z0-9_-]*):$`)

var ciTriggerKeys = map[string]bool{"push": true, "pull_request": true, "workflow_dispatch": true, "workflow_call": true, "schedule": true}

func scanSources(root string, inv *inventory) error {
	build, err := readFile(root, "scripts/desktop-build.sh")
	if err != nil {
		return err
	}
	for i, line := range strings.Split(build, "\n") {
		m := artifactRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		inv.add(entry{
			Kind:     kindArtifact,
			Name:     m[1],
			Detail:   m[2],
			Location: fmt.Sprintf("scripts/desktop-build.sh:%d", i+1),
			Class:    classKeepBusiness,
			Owner:    "same file name and installer identity; Electron payload inside",
		})
	}
	for _, workflow := range []string{"ci.yml", "app-memory.yml", "release-desktop.yml"} {
		text, err := readFile(root, ".github/workflows/"+workflow)
		if err != nil {
			return err
		}
		for _, m := range ciJobRe.FindAllStringSubmatch(text, -1) {
			job := m[1]
			if ciTriggerKeys[job] || (workflow == "ci.yml" && !strings.HasPrefix(job, "desktop")) {
				continue
			}
			key := workflow + "/" + job
			e := entry{Kind: kindCIJob, Name: key, Location: ".github/workflows/" + workflow}
			if rule, ok := ciJobs[key]; ok {
				e.Class, e.Owner = rule.class, rule.owner
			}
			inv.add(e)
		}
	}
	return nil
}
