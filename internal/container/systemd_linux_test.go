//go:build linux

package container

import (
	"strings"
	"testing"
	"time"
)

func TestUnitStatusNeverTreatsLiveSupervisorAsInactive(t *testing.T) {
	base := "LoadState=loaded\nActiveState=active\nResult=success\nMainPID=42\nExecMainCode=0\nExecMainStatus=0\n"
	for _, input := range []string{
		base,
		strings.Replace(base, "ActiveState=active", "ActiveState=deactivating", 1),
		strings.Replace(base, "ActiveState=active", "ActiveState=failed", 1),
		strings.Replace(base, "MainPID=42", "MainPID=0", 1),
	} {
		status, err := parseUnitStatus(input)
		if err != nil || !status.live() {
			t.Fatalf("live unit could be reclaimed: %+v, %v", status, err)
		}
	}
	missing := "LoadState=not-found\nActiveState=inactive\nResult=success\nMainPID=0\nExecMainCode=0\nExecMainStatus=0\n"
	status, err := parseUnitStatus(missing)
	if err != nil || status.live() {
		t.Fatalf("missing unit is not inactive: %+v, %v", status, err)
	}
	for _, invalid := range []string{
		"", base + "MainPID=0\n",
		strings.Replace(base, "LoadState=loaded\n", "", 1),
		strings.Replace(base, "ActiveState=active", "ActiveState=unknown", 1),
		strings.Replace(base, "MainPID=42", "MainPID=-1", 1),
		strings.Replace(base, "ExecMainStatus=0", "ExecMainStatus=256", 1),
		strings.Replace(base, "ExecMainCode=0", "ExecMainCode=9", 1),
		strings.Replace(base, "LoadState=loaded", "LoadState=not-found", 1),
	} {
		if _, err := parseUnitStatus(invalid); err == nil {
			t.Fatalf("accepted ambiguous unit status: %q", invalid)
		}
	}
}

func TestSchedulingGraceExpiresAcrossBootsAndClockChanges(t *testing.T) {
	now := time.Now().UTC()
	boot := "11111111-1111-1111-1111-111111111111"
	base := Record{State: StateStarting, CreatedAt: now.Add(-time.Second), BootID: boot}
	missing := unitStatus{LoadState: "not-found", ActiveState: "inactive"}
	if !schedulingPendingAt(base, missing, boot, now) {
		t.Fatal("normal concurrent scheduling was treated as abandoned")
	}
	for _, change := range []func(*Record){
		func(record *Record) { record.BootID = "22222222-2222-2222-2222-222222222222" },
		func(record *Record) { record.CreatedAt = now.Add(time.Hour) },
		func(record *Record) { record.CreatedAt = now.Add(-detachedStartupLimit) },
		func(record *Record) { record.State = StateStopping },
		func(record *Record) { record.StartedAt = &now },
	} {
		record := base
		change(&record)
		if schedulingPendingAt(record, missing, boot, now) {
			t.Fatalf("abandoned scheduling kept its grace period: %+v", record)
		}
	}
	legacy := base
	legacy.BootID = ""
	if !schedulingPendingAt(legacy, missing, boot, now) {
		t.Fatal("legacy record lost normal scheduling protection")
	}
}
