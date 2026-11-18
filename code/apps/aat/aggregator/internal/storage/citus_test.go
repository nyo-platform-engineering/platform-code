package storage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestCitusInventoryRejectsFullCopiesAndColocatedReplicas(t *testing.T) {
	valid := []CitusMember{}
	for i, pair := range [][2]string{{"a", "b"}, {"b", "c"}, {"c", "a"}} {
		for _, node := range pair {
			unit := string(rune('1'+i)) + node
			valid = append(valid, CitusMember{Unit: unit, Node: node, Group: i + 1, Host: unit, API: "http://" + unit + ":8008"})
		}
	}
	check := func(members []CitusMember, wantError bool) {
		t.Helper()
		raw, _ := json.Marshal(members)
		path := filepath.Join(t.TempDir(), "members.json")
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		_, err := ReadCitusMembers(path)
		if (err != nil) != wantError {
			t.Fatalf("inventory error: %v; expected error=%v", err, wantError)
		}
	}
	check(valid, false)
	sameNode := append([]CitusMember(nil), valid...)
	sameNode[1].Node = sameNode[0].Node
	check(sameNode, true)
	fullCopy := append([]CitusMember(nil), valid...)
	fullCopy[2].Node = "a"
	check(fullCopy, true)
	extra := append(append([]CitusMember(nil), valid...), CitusMember{Unit: "extra", Node: "a", Group: 1, Host: "extra", API: "http://extra:8008"})
	check(extra, true)
}
