package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type CredentialDiagnosticOptions struct {
	Probe  bool `json:"probe"`
	Repair bool `json:"repair"`
	DryRun bool `json:"dryRun"`
}

type CredentialDiagnosticCheck struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	Path    string `json:"path,omitempty"`
	Message string `json:"message,omitempty"`
}

type CredentialDiagnosticReport struct {
	Home                string                      `json:"home"`
	CredentialPath      string                      `json:"credentialPath"`
	Checks              []CredentialDiagnosticCheck `json:"checks"`
	PendingTransactions int                         `json:"pendingTransactions"`
	Actions             []string                    `json:"actions"`
}

func (r *CredentialDiagnosticReport) add(id, status, path, message string) {
	r.Checks = append(r.Checks, CredentialDiagnosticCheck{ID: id, Status: status, Path: path, Message: message})
}

func pendingModelCredentialTransactionCount() (int, error) {
	dir := modelCredentialTransactionDir()
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) || dir == "" {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	count := 0
	for _, entry := range entries {
		if !entry.IsDir() && filepath.Ext(entry.Name()) == ".json" {
			count++
		}
	}
	return count, nil
}

// DiagnoseCredentials provides the shared Desktop/CLI credential report.
func DiagnoseCredentials(opts CredentialDiagnosticOptions) (CredentialDiagnosticReport, error) {
	home, path := ReasonixHomeDir(), UserCredentialsPath()
	report := CredentialDiagnosticReport{Home: home, CredentialPath: path, Checks: []CredentialDiagnosticCheck{}, Actions: []string{}}
	if strings.TrimSpace(home) == "" || strings.TrimSpace(path) == "" {
		report.add("location", "failed", path, "Reasonix home could not be resolved")
		return report, nil
	}
	homeAbs, homeErr := filepath.Abs(home)
	pathAbs, pathErr := filepath.Abs(path)
	if homeErr != nil || pathErr != nil || !pathWithinRoot(homeAbs, pathAbs) {
		report.add("location", "failed", path, "credential path is outside Reasonix home")
		return report, nil
	}
	report.add("location", "passed", path, "credential path is inside Reasonix home")

	info, err := os.Lstat(path)
	exists := err == nil
	if os.IsNotExist(err) {
		report.add("file_type", "passed", path, "credential file does not exist yet")
	} else if err != nil {
		report.add("file_type", "failed", path, classifyCredentialAccessError(err))
	} else if info.Mode()&os.ModeSymlink != 0 {
		report.add("file_type", "failed", path, "credential path is a symbolic link")
	} else if !info.Mode().IsRegular() {
		report.add("file_type", "failed", path, "credential path is not a regular file")
	} else {
		report.add("file_type", "passed", path, "regular file")
	}

	ownerCurrent, readonly, reparse := false, false, false
	if exists {
		owner, current, ro, rp, inspectErr := credentialPlatformInspect(path, info)
		ownerCurrent, readonly, reparse = current, ro, rp
		if inspectErr != nil {
			report.add("owner", "unknown", path, inspectErr.Error())
		} else if current {
			report.add("owner", "passed", path, owner)
		} else {
			report.add("owner", "failed", path, owner)
		}
		if reparse {
			report.add("reparse_point", "failed", path, "credential file is a reparse point")
		} else {
			report.add("reparse_point", "passed", path, "not a reparse point")
		}
		if readonly {
			report.add("writable", "failed", path, "credential file is read-only")
		} else {
			report.add("writable", "unknown", path, "not read-only; effective write and replace access has not been tested")
		}
		if !info.Mode().IsRegular() || reparse {
			report.add("read", "not_checked", path, "refusing to read a link or special file")
		} else if _, readErr := os.ReadFile(path); readErr != nil {
			report.add("read", "failed", path, classifyCredentialAccessError(readErr))
		} else {
			report.add("read", "passed", path, "credential file is readable")
		}
	} else {
		report.add("owner", "not_checked", path, "credential file does not exist")
		report.add("read", "not_checked", path, "credential file does not exist")
		report.add("writable", "not_checked", path, "credential file does not exist")
		report.add("reparse_point", "not_checked", path, "credential file does not exist")
	}

	pending, pendingErr := pendingModelCredentialTransactionCount()
	report.PendingTransactions = pending
	if pendingErr != nil {
		report.add("transactions", "unknown", modelCredentialTransactionDir(), pendingErr.Error())
	} else if pending > 0 {
		report.add("transactions", "failed", modelCredentialTransactionDir(), fmt.Sprintf("%d unfinished model credential transaction(s)", pending))
	} else {
		report.add("transactions", "passed", modelCredentialTransactionDir(), "no unfinished model credential transactions")
	}

	var repairPathErr error
	if opts.Repair && exists {
		repairPathErr = validateCredentialRepairPath(home, path, info)
	}
	if opts.Probe && repairPathErr == nil {
		probeCredentialDirectory(&report, path)
	} else {
		report.add("directory_replace_probe", "not_checked", filepath.Dir(path), "run with --probe to test create and rename")
	}

	if opts.Repair {
		if repairPathErr != nil {
			report.add("repair", "failed", path, repairPathErr.Error())
		} else if !exists || info == nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || reparse || !ownerCurrent {
			report.add("repair", "failed", path, "repair requires a current-user-owned regular file inside Reasonix home")
		} else if opts.DryRun {
			report.Actions = append(report.Actions, "would clear read-only state and add current-user read/write/replace access where required")
			report.add("repair", "passed", path, "repair preview only")
		} else {
			actions, repairErr := repairCredentialTarget(home, path, info)
			if repairErr != nil {
				report.add("repair", "failed", path, repairErr.Error())
			} else {
				report.Actions = append(report.Actions, actions...)
				report.add("repair", "passed", path, "credential read/write access verified; replacement was not attempted")
			}
		}
	}
	return report, nil
}

func probeCredentialDirectory(report *CredentialDiagnosticReport, path string) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		report.add("directory_replace_probe", "failed", dir, classifyCredentialAccessError(err))
		return
	}
	tmp, err := os.CreateTemp(dir, ".reasonix-credential-probe-*")
	if err != nil {
		report.add("directory_replace_probe", "failed", dir, classifyCredentialAccessError(err))
		return
	}
	from := tmp.Name()
	_ = tmp.Close()
	to := from + ".renamed"
	defer os.Remove(from)
	defer os.Remove(to)
	if err := os.Rename(from, to); err != nil {
		report.add("directory_replace_probe", "failed", dir, classifyCredentialAccessError(err))
		return
	}
	report.add("directory_replace_probe", "passed", dir, "temporary create and atomic rename succeeded; the existing .env was not replaced")
}

func probeCredentialTarget(path string) error {
	// Opening without CREATE/TRUNC tests access without reading or rewriting
	// secrets. In particular it cannot overwrite a concurrent slot publication.
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	return f.Close()
}

func validateCredentialRepairPath(home, path string, expected os.FileInfo) error {
	home, err := filepath.Abs(home)
	if err != nil {
		return err
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return err
	}
	if !pathWithinRoot(home, path) {
		return fmt.Errorf("credential path is outside Reasonix home")
	}
	// Include the home itself: Lstat on .env alone misses linked directories.
	for current := path; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		_, _, _, reparse, err := credentialPlatformInspect(current, info)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || reparse {
			return fmt.Errorf("repair refuses linked paths or reparse points")
		}
		if current == path && (!info.Mode().IsRegular() || !os.SameFile(expected, info)) {
			return fmt.Errorf("credential file identity changed")
		}
		if current == home {
			break
		}
	}
	return nil
}

func repairCredentialTarget(home, path string, expected os.FileInfo) ([]string, error) {
	if err := validateCredentialRepairPath(home, path, expected); err != nil {
		return nil, err
	}
	unlock, err := LockUserCredentialEdits()
	if err != nil {
		return nil, err
	}
	defer unlock()
	if err := validateCredentialRepairPath(home, path, expected); err != nil {
		return nil, err
	}
	return credentialPlatformRepair(path, expected, func() error {
		if err := validateCredentialRepairPath(home, path, expected); err != nil {
			return err
		}
		return probeCredentialTarget(path)
	})
}

func classifyCredentialAccessError(err error) string {
	if err == nil {
		return ""
	}
	if os.IsPermission(err) {
		return "current user does not have the required access"
	}
	return err.Error()
}
