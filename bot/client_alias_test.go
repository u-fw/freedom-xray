package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestClientListFieldMatchesXrayPrecedence(t *testing.T) {
	cases := []struct {
		name     string
		settings map[string]any
		field    string
		count    int
	}{
		{"users", map[string]any{"users": []any{"user"}}, "users", 1},
		{"null clients", map[string]any{"clients": nil, "users": []any{"user"}}, "users", 1},
		{"clients win", map[string]any{"clients": []any{"client"}, "users": []any{"user", "other"}}, "clients", 1},
		{"empty clients win", map[string]any{"clients": []any{}, "users": []any{"user"}}, "clients", 0},
		{"no lists", map[string]any{}, "clients", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := map[string]any{"settings": tc.settings}
			_, field, list, err := clientListField(in)
			if err != nil || field != tc.field || len(list) != tc.count {
				t.Fatalf("field %q, list %v, error %v", field, list, err)
			}
			other := "users"
			if field == "users" {
				other = "clients"
			}
			before, present := tc.settings[other]
			if err := setClientList(in, []any{"replacement"}); err != nil {
				t.Fatal(err)
			}
			if got, exists := tc.settings[other]; exists != present || !reflect.DeepEqual(got, before) {
				t.Fatalf("inactive field %q changed: %v", other, got)
			}
			if !reflect.DeepEqual(tc.settings[field], []any{"replacement"}) {
				t.Fatal("replacement was not applied to the effective field")
			}
		})
	}
	for _, settings := range []map[string]any{{"clients": "invalid"}, {"clients": nil, "users": "invalid"}} {
		if _, err := clientList(map[string]any{"settings": settings}); err == nil {
			t.Fatal("accepted a non-array effective client list")
		}
	}
}

func TestManagedUsersPreserveEffectiveAliases(t *testing.T) {
	root := testConfig(t)
	raw := root["inbounds"].([]any)[1].(map[string]any)["settings"].(map[string]any)
	raw["users"], raw["clients"] = raw["clients"], nil
	cdn := root["inbounds"].([]any)[2].(map[string]any)["settings"].(map[string]any)
	shadow := []any{map[string]any{"id": "shadow-id", "email": "shadow"}}
	cdn["users"] = shadow
	store := suspendedStore{Users: map[string]suspendedAccount{}}
	if stats, err := applyBindingEmailMigration(root, &store, bindingMigrate); err != nil || stats.Rewritten != 2 {
		t.Fatalf("migration: %+v %v", stats, err)
	}
	directory, err := directoryFromConfig(root)
	if err != nil || len(directory.Users) != 1 {
		t.Fatalf("directory contains inactive users: %+v %v", directory, err)
	}
	for _, change := range []UserChange{
		{Kind: "add", User: "phone", Node: "all"},
		{Kind: "rename", User: "phone", NewName: "mobile"},
		{Kind: "rotate", User: "mobile"},
	} {
		if _, err := applyDirectoryUserChange(root, &directory, change, true); err != nil {
			t.Fatal(err)
		}
	}
	for _, action := range []string{"disable", "enable"} {
		if _, err := applyUserSuspension(root, &store, UserChange{Kind: action, User: "mobile"}); err != nil {
			t.Fatal(err)
		}
	}
	for _, change := range []UserChange{{Kind: "unassign", User: "mobile", Node: "raw"}, {Kind: "delete", User: "mobile"}} {
		if _, err := applyDirectoryUserChange(root, &directory, change, true); err != nil {
			t.Fatal(err)
		}
	}
	if raw["clients"] != nil || len(raw["users"].([]any)) != 1 || !reflect.DeepEqual(cdn["users"], shadow) || len(cdn["clients"].([]any)) != 1 {
		t.Fatalf("CRUD changed the inactive aliases: raw=%v cdn=%v", raw, cdn)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	data, _ := json.Marshal(root)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	nodes, err := LoadXrayInbounds(path)
	if err != nil || len(nodes) != 2 || len(nodes[0].Clients) != 1 || len(nodes[1].Clients) != 1 || nodes[0].Clients[0].Email != bindingEmail("admin", "raw") || nodes[1].Clients[0].Email != bindingEmail("admin", "cdn") {
		t.Fatalf("node client view does not match effective lists: %+v %v", nodes, err)
	}
	report, err := inspectNodeDeletion(path, "raw")
	if err != nil || report.Clients != 1 {
		t.Fatalf("wrong deletion impact: %+v %v", report, err)
	}
}

func TestAliasPolicyAndNativeTrafficOwners(t *testing.T) {
	root := agentFixtureRoot(t)
	root["inbounds"] = append(root["inbounds"].([]any),
		map[string]any{"tag": "vmess", "protocol": "vmess", "settings": map[string]any{"clients": []any{map[string]any{"email": "active", "level": json.Number("3")}}, "users": []any{map[string]any{"email": "shadow", "level": json.Number("7")}}}},
		map[string]any{"tag": "trojan", "protocol": "trojan", "settings": map[string]any{"clients": nil, "users": []any{map[string]any{"email": "fallback", "level": json.Number("4")}}}},
	)
	owners, err := nativeAccountOwners(root)
	if err != nil || !owners["active"] || !owners["fallback"] || owners["shadow"] {
		t.Fatalf("wrong native identities: %v %v", owners, err)
	}
	records, err := nativeInboundRecords(root)
	if err != nil || records[1].ClientCount == nil || *records[1].ClientCount != 1 || records[2].ClientCount == nil || *records[2].ClientCount != 1 {
		t.Fatalf("wrong inbound counts: %+v %v", records, err)
	}
	enableStatsPolicy(root)
	levels := root["policy"].(map[string]any)["levels"].(map[string]any)
	if levels["3"] == nil || levels["4"] == nil || levels["7"] != nil {
		t.Fatalf("statistics levels do not match active accounts: %v", levels)
	}
}
