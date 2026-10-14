package config

import (
	"encoding/json"
	"testing"
)

func TestTmpfsCapacitiesValidatePersistAndInherit(t *testing.T) {
	for _, test := range []struct {
		name     string
		tmp, shm int64
		valid    bool
	}{
		{"legacy", 0, 0, true},
		{"positive bytes", 1, 1, true},
		{"custom", 32 << 20, 96 << 20, true},
		{"maximum", 128 << 30, 128 << 30, true},
		{"negative tmp", -1, 0, false},
		{"negative shm", 0, -1, false},
		{"oversized tmp", (128 << 30) + 1, 0, false},
		{"oversized shm", 0, (128 << 30) + 1, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := Config{Command: []string{"true"}, Memory: 8 << 20, TmpSize: test.tmp, ShmSize: test.shm}
			if err := cfg.ValidateExecution(); (err == nil) != test.valid {
				t.Fatalf("validation: %v", err)
			}
			if !test.valid {
				return
			}
			data, err := json.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			var saved Config
			if err := json.Unmarshal(data, &saved); err != nil {
				t.Fatal(err)
			}
			tmp, shm := saved.TmpfsSizes()
			wantTmp, wantShm := test.tmp, test.shm
			if wantTmp == 0 {
				wantTmp = 16 << 20
			}
			if wantShm == 0 {
				wantShm = 64 << 20
			}
			if tmp != wantTmp || shm != wantShm {
				t.Fatalf("saved capacities %d/%d", tmp, shm)
			}
			exec := (Exec{Command: []string{"echo"}}).Apply(saved)
			if gotTmp, gotShm := exec.TmpfsSizes(); gotTmp != tmp || gotShm != shm {
				t.Fatal("exec lost mount capacities")
			}
		})
	}
}
