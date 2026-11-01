package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"unicode/utf8"
)

// A binding email is the Xray client identity for one user on one inbound.
// The owner keeps its historical username; the suffix is the URL-safe base64
// encoding of the inbound tag. Because managedUserName rejects '+' and the
// raw base64 alphabet contains no '+', the first separator is unambiguous.
func bindingEmail(user, tag string) string {
	return user + "+" + base64.RawURLEncoding.EncodeToString([]byte(tag))
}

// parseBindingEmail accepts only the canonical form produced by bindingEmail.
// Rejecting non-canonical base64 and invalid tags prevents a hand-written
// email such as "alice+tag@example.com" from being mistaken for a binding.
func parseBindingEmail(email string) (owner, tag string, ok bool) {
	index := strings.IndexByte(email, '+')
	if index <= 0 || index == len(email)-1 {
		return "", "", false
	}
	owner = email[:index]
	if !managedUserName.MatchString(owner) {
		return "", "", false
	}
	suffix := email[index+1:]
	if strings.ContainsRune(suffix, '+') {
		return "", "", false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(suffix)
	if err != nil || len(decoded) == 0 {
		return "", "", false
	}
	if !utf8.Valid(decoded) {
		return "", "", false
	}
	if base64.RawURLEncoding.EncodeToString(decoded) != suffix {
		return "", "", false
	}
	tag = string(decoded)
	if validateNodeTag(tag) != nil {
		return "", "", false
	}
	return owner, tag, true
}

// bindingOwner maps a binding email back to the user shown in Telegram.
// Legacy and custom emails are returned unchanged.
func bindingOwner(email string) string {
	if owner, _, ok := parseBindingEmail(email); ok {
		return owner
	}
	return email
}

func bindingTag(email string) (string, bool) {
	_, tag, ok := parseBindingEmail(email)
	return tag, ok
}

func sortedUniqueStrings(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

// managedClientEmails groups every managed client email by its owner. Custom
// emails that are neither a managed username nor a binding are ignored.
func managedClientEmails(root map[string]any) (map[string][]string, error) {
	inbounds, err := usableInbounds(root)
	if err != nil {
		return nil, err
	}
	groups := make(map[string][]string)
	for _, in := range inbounds {
		list, err := clientList(in)
		if err != nil {
			return nil, err
		}
		for _, value := range list {
			client, ok := getMap(value)
			if !ok {
				return nil, errors.New("Invalid client format")
			}
			email, _ := client["email"].(string)
			if email == "" {
				continue
			}
			owner := bindingOwner(email)
			if owner == email && !managedUserName.MatchString(email) {
				continue
			}
			groups[owner] = append(groups[owner], email)
		}
	}
	for owner, emails := range groups {
		groups[owner] = sortedUniqueStrings(emails)
	}
	return groups, nil
}

func managedClientEmailsFromConfig(configPath string) (map[string][]string, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, err
	}
	var root map[string]any
	if err = json.Unmarshal(data, &root); err != nil {
		return nil, err
	}
	return managedClientEmails(root)
}

// bindingEmailAliases returns the plain owner name plus every binding email
// the user currently owns in the configuration, the suspended store, and the
// traffic ledger. A per-user runtime reset must clear all of them: statsquery
// performs substring matching, so "user>>>alice>>>" never matches a binding
// such as "user>>>alice+VEN...>>>".
func bindingEmailAliases(configPath string, state trafficStore, user string) ([]string, error) {
	if !managedUserName.MatchString(user) {
		return nil, errors.New("Invalid user identifier")
	}
	aliases := []string{user}
	groups, err := managedClientEmailsFromConfig(configPath)
	if err != nil {
		return nil, fmt.Errorf("read client bindings: %w", err)
	}
	aliases = append(aliases, groups[user]...)
	store, _, err := readSuspendedStore(configPath)
	if err != nil {
		return nil, err
	}
	if account, ok := store.Users[user]; ok {
		for tag, client := range account.Nodes {
			email, _ := client["email"].(string)
			if email == "" {
				email = bindingEmail(user, tag)
			}
			owner, _, binding := parseBindingEmail(email)
			if email != user && (!binding || owner != user) {
				continue
			}
			aliases = append(aliases, email)
		}
	}
	for email := range state.Bindings {
		if owner, _, ok := parseBindingEmail(email); ok && owner == user {
			aliases = append(aliases, email)
		}
	}
	return sortedUniqueStrings(aliases), nil
}
