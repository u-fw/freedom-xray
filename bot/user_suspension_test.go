package main

import (
	"encoding/json"
	"testing"
)

func TestSuspensionRoundtrip(t *testing.T) {
	root := testConfig(t)
	store := suspendedStore{Users: map[string]suspendedAccount{}}
	if _, err := applyManagedUserChange(root, UserChange{Kind: "add", User: "phone", Node: "all"}); err != nil {
		t.Fatal(err)
	}
	original, _ := json.Marshal(root)
	message, err := applyUserSuspension(root, &store, UserChange{Kind: "disable", User: "phone"})
	if err != nil {
		t.Fatal(err)
	}
	if message == "" || len(store.Users) != 1 {
		t.Fatal("suspension not stored")
	}
	for _, tag := range []string{"raw", "cdn"} {
		if len(clientsIn(t, root, tag)) != 1 {
			t.Fatalf("user still active on %s", tag)
		}
	}
	if _, err := applyUserSuspension(root, &store, UserChange{Kind: "disable", User: "phone"}); err == nil {
		t.Fatal("duplicate disable succeeded")
	}
	if _, err := applyUserSuspension(root, &store, UserChange{Kind: "enable", User: "phone"}); err != nil {
		t.Fatal(err)
	}
	if len(store.Users) != 0 {
		t.Fatal("restore did not clear state")
	}
	next, _ := json.Marshal(root)
	if string(original) != string(next) {
		t.Fatalf("user not restored: %s / %s", original, next)
	}
}

func TestSuspensionGuards(t *testing.T) {
	root := testConfig(t)
	store := suspendedStore{Users: map[string]suspendedAccount{}}
	if _, err := applyUserSuspension(root, &store, UserChange{Kind: "disable", User: "admin"}); err == nil {
		t.Fatal("last user suspended")
	}
	if _, err := applyManagedUserChange(root, UserChange{Kind: "add", User: "phone", Node: "raw"}); err != nil {
		t.Fatal(err)
	}
	if _, err := applyUserSuspension(root, &store, UserChange{Kind: "disable", User: "phone"}); err != nil {
		t.Fatal(err)
	}
	// Removing the original node must prevent partial restoration.
	inbounds := root["inbounds"].([]any)
	root["inbounds"] = append(inbounds[:1], inbounds[2:]...)
	if _, err := applyUserSuspension(root, &store, UserChange{Kind: "enable", User: "phone"}); err == nil {
		t.Fatal("partially restored user")
	}
	if len(store.Users) != 1 {
		t.Fatal("state lost on failed restore")
	}
}
