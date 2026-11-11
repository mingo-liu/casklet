package config

import (
	"encoding/json"
	"testing"
)

func TestTmpfsCapacitiesValidatePersistAndInherit(t *testing.T) {
	tests := []struct {
		name     string
		tmp, shm int64
		valid    bool
	}{
		{name: "legacy", tmp: 0, shm: 0, valid: true},
		{name: "positive bytes", tmp: 1, shm: 1, valid: true},
		{name: "custom", tmp: 32 << 20, shm: 96 << 20, valid: true},
		{name: "maximum", tmp: 128 << 30, shm: 128 << 30, valid: true},
		{name: "negative tmp", tmp: -1, shm: 0, valid: false},
		{name: "negative shm", tmp: 0, shm: -1, valid: false},
		{name: "oversized tmp", tmp: (128 << 30) + 1, shm: 0, valid: false},
		{name: "oversized shm", tmp: 0, shm: (128 << 30) + 1, valid: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Config{Command: []string{"true"}, Memory: 8 << 20, TmpSize: tt.tmp, ShmSize: tt.shm}
			if err := cfg.ValidateExecution(); (err == nil) != tt.valid {
				t.Fatalf("validation: %v", err)
			}
			if !tt.valid {
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
			wantTmp, wantShm := tt.tmp, tt.shm
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
