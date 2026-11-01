package main

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestBindingEmailRoundTripAndCanonicalParsing(t *testing.T) {
	for _, tag := range []string{"raw", "xhttp-reality", "node.a_b@c-1"} {
		email := bindingEmail("alice", tag)
		owner, decoded, ok := parseBindingEmail(email)
		if !ok || owner != "alice" || decoded != tag {
			t.Fatalf("%s round trip: owner=%q tag=%q ok=%v", email, owner, decoded, ok)
		}
		if bindingOwner(email) != "alice" {
			t.Fatalf("%s owner lookup failed", email)
		}
	}
	for _, email := range []string{
		"alice",
		"alice+",
		"+cmF3",
		"alice+cmF3+cmF3",
		"bad>>>name+cmF3",
		"alice+cmF3=",
		"alice+cmF3@example.com",
		"alice+" + base64.RawURLEncoding.EncodeToString([]byte("/bad")),
		"alice+" + base64.RawURLEncoding.EncodeToString([]byte{0xff}),
	} {
		if owner, tag, ok := parseBindingEmail(email); ok {
			t.Fatalf("%q was accepted as owner=%q tag=%q", email, owner, tag)
		}
		if bindingOwner(email) != email {
			t.Fatalf("legacy email %q was rewritten to %q", email, bindingOwner(email))
		}
	}
}

func TestManagedClientEmailsGroupsByOwner(t *testing.T) {
	root := testConfig(t)
	if _, err := applyManagedUserChange(root, UserChange{Kind: "add", User: "phone", Node: "all"}); err != nil {
		t.Fatal(err)
	}
	groups, err := managedClientEmails(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups["phone"]) != 2 {
		t.Fatalf("phone bindings: %+v", groups["phone"])
	}
	if len(groups["admin"]) != 1 {
		t.Fatalf("admin bindings: %+v", groups["admin"])
	}
	for owner, emails := range groups {
		for _, email := range emails {
			if bindingOwner(email) != owner {
				t.Fatalf("%q grouped under %q", email, owner)
			}
		}
	}
}

func TestBindingEmailAliasesCoverConfigStoreAndLedger(t *testing.T) {
	dir := t.TempDir()
	config := dir + "/config.json"
	root := testConfig(t)
	if _, err := applyManagedUserChange(root, UserChange{Kind: "add", User: "phone", Node: "all"}); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	if err = writeAtomically(config, data, 0600); err != nil {
		t.Fatal(err)
	}
	store := suspendedStore{Users: map[string]suspendedAccount{"ghost": {UUID: "x", Nodes: map[string]map[string]any{"raw": {"email": "ghost"}}}}}
	raw, err := marshalSuspendedStore(store)
	if err != nil {
		t.Fatal(err)
	}
	if err = writeAtomically(suspendedStorePath(config), raw, 0600); err != nil {
		t.Fatal(err)
	}
	state := trafficStore{Bindings: map[string]trafficCounter{bindingEmail("phone", "raw"): {}}}
	aliases, err := bindingEmailAliases(config, state, "phone")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(aliases, ",")
	for _, wanted := range []string{"phone", bindingEmail("phone", "raw"), bindingEmail("phone", "cdn")} {
		if !strings.Contains(joined, wanted) {
			t.Fatalf("missing alias %q in %v", wanted, aliases)
		}
	}
}
